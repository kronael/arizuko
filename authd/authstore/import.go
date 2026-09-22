package authstore

// import.go writes a whole auth.db into the entities this package reads: the
// destination half of the one-way data move (spec 6/37 step 7). The tool that
// runs it knows the old schema and nothing about items; this file knows items
// and nothing about the old schema; a Snapshot of plain values is all that
// crosses. A FAMILY IS DERIVED, not read: the old table has no family row.

import (
	"fmt"
	"strings"

	"github.com/kronael/arizuko/objstore"
)

// A Snapshot is one auth.db as plain values. Every field is the stored string,
// and an empty one means the column was NULL — which is exactly what an absent
// attribute means on the other side.
type Snapshot struct {
	Keys       []KeyRow
	Users      []UserRow
	Identities []IdentityRow
	Tokens     []TokenRow
}

// KeyRow is one signing key. Active names the key that signs; at most one may.
type KeyRow struct {
	Kid       string
	PrivPEM   string
	PubPEM    string
	CreatedAt string
	RetiredAt string
	Active    bool
}

// UserRow is one canonical account.
type UserRow struct {
	UserID    string
	Name      string
	CreatedAt string
}

// IdentityRow is one provider login linked to an account.
type IdentityRow struct {
	UserID      string
	Provider    string
	ProviderSub string
	LinkedAt    string
}

// TokenRow is one issued refresh token with both of its old tombstones.
// RevokedAt is per token here and per family on the other side.
type TokenRow struct {
	TokenHash string
	FamilyID  string
	Sub       string
	Scope     string
	Aud       string
	IssuedAt  string
	ExpiresAt string
	UsedAt    string
	RevokedAt string
}

// Counts is what an import wrote, per entity, for the operator to check against
// the source.
type Counts struct {
	Keys       int
	Slot       int
	Users      int
	Identities int
	Tokens     int
	Families   int
}

// Import writes snap into the entities. It is idempotent — every write replaces
// the item at its coordinate — so a failed run is re-runnable. Run it with the
// daemon stopped. It refuses a snapshot it cannot represent: two active signing
// keys, or a family without exactly one unspent token, would put a second
// signer or a second live credential into the store.
func (s *Store) Import(snap Snapshot) (Counts, error) {
	writes, counts, err := importWrites(snap)
	if err != nil {
		return Counts{}, err
	}
	// The writes are independent replacements, so a chunk that lands and a
	// chunk that does not leave a partial import a re-run finishes.
	for len(writes) > 0 {
		n := min(len(writes), objstore.MaxBatchActions)
		committed, err := s.s.GuardedBatch(ctx(), nil, writes[:n])
		if err != nil {
			return Counts{}, err
		}
		if !committed {
			return Counts{}, fmt.Errorf("the import batch was refused; nothing in it conditions "+
				"on anything, so the store rejected %d unconditional writes", n)
		}
		writes = writes[n:]
	}
	return counts, nil
}

// importWrites turns a snapshot into the writes that represent it, or names why
// it cannot be represented.
func importWrites(snap Snapshot) ([]objstore.Write, Counts, error) {
	var (
		writes []objstore.Write
		counts Counts
		active []string
	)
	for _, k := range snap.Keys {
		attrs := map[string]string{
			"priv_pem": k.PrivPEM, "pub_pem": k.PubPEM, "created_at": k.CreatedAt,
		}
		if k.RetiredAt != "" {
			attrs["retired_at"] = k.RetiredAt
		}
		writes = append(writes, objstore.Write{
			Op: objstore.Put, Entity: SigningKeys, Key: key(k.Kid), Attrs: attrs,
		})
		counts.Keys++
		if k.Active {
			active = append(active, k.Kid)
		}
	}
	if len(active) > 1 {
		return nil, Counts{}, fmt.Errorf("%d signing keys are active (%s); the slot holds one, "+
			"and picking one of them would be choosing a signer", len(active), strings.Join(active, ", "))
	}
	if len(active) == 1 {
		writes = append(writes, objstore.Write{
			Op: objstore.Put, Entity: SigningKeySlot, Key: key(activeSlot),
			Attrs: map[string]string{"kid": active[0]},
		})
		counts.Slot = 1
	}
	for _, u := range snap.Users {
		writes = append(writes, objstore.Write{
			Op: objstore.Put, Entity: AuthUsers, Key: key(u.UserID),
			Attrs: map[string]string{"name": u.Name, "created_at": u.CreatedAt},
		})
		counts.Users++
	}
	for _, i := range snap.Identities {
		writes = append(writes, objstore.Write{
			Op: objstore.Put, Entity: OAuthIdentities, Key: key(i.Provider + ":" + i.ProviderSub),
			Attrs: map[string]string{"user_id": i.UserID, "linked_at": i.LinkedAt},
		})
		counts.Identities++
	}
	tokenWrites, familyWrites, err := refreshWrites(snap.Tokens)
	if err != nil {
		return nil, Counts{}, err
	}
	counts.Tokens = len(tokenWrites)
	counts.Families = len(familyWrites)
	return append(append(writes, familyWrites...), tokenWrites...), counts, nil
}

// refreshWrites turns the old one-row-per-token table into tokens plus the
// families they belong to.
func refreshWrites(rows []TokenRow) (tokens, families []objstore.Write, err error) {
	byFamily := map[string][]TokenRow{}
	order := []string{}
	for _, r := range rows {
		if _, seen := byFamily[r.FamilyID]; !seen {
			order = append(order, r.FamilyID)
		}
		byFamily[r.FamilyID] = append(byFamily[r.FamilyID], r)
		attrs := map[string]string{
			"family_id": r.FamilyID, "sub": r.Sub, "scope": r.Scope, "aud": r.Aud,
			"issued_at": r.IssuedAt, "expires_at": r.ExpiresAt,
		}
		if r.UsedAt != "" {
			attrs["used_at"] = r.UsedAt
		}
		tokens = append(tokens, objstore.Write{
			Op: objstore.Put, Entity: RefreshTokens, Key: key(r.TokenHash), Attrs: attrs,
		})
	}
	var broken []string
	for _, fam := range order {
		w, ferr := familyWrite(fam, byFamily[fam])
		if ferr != nil {
			broken = append(broken, ferr.Error())
			continue
		}
		families = append(families, w)
	}
	// Every broken family is reported at once: an operator deciding what to do
	// about the data needs the whole picture, not the first one the loop met.
	if len(broken) > 0 {
		return nil, nil, fmt.Errorf("%d refresh families cannot be represented:\n  %s",
			len(broken), strings.Join(broken, "\n  "))
	}
	return tokens, families, nil
}

// familyWrite derives one family item from the tokens that carry its id. The
// CURRENT token is the one with no `used_at`, exactly one per family by
// construction; anything else is a lineage that forked or died mid-rotation,
// refused by name rather than choosing which credential to call live.
func familyWrite(fam string, rows []TokenRow) (objstore.Write, error) {
	var current []TokenRow
	started, revoked := "", ""
	for _, r := range rows {
		if r.UsedAt == "" {
			current = append(current, r)
		}
		if started == "" || r.IssuedAt < started {
			started = r.IssuedAt
		}
		// The earliest revoke is when the family died; a later one is a second
		// kill of something already dead.
		if r.RevokedAt != "" && (revoked == "" || r.RevokedAt < revoked) {
			revoked = r.RevokedAt
		}
	}
	if len(current) != 1 {
		return objstore.Write{}, fmt.Errorf("family %s holds %d unspent tokens of %d, want exactly 1",
			fam, len(current), len(rows))
	}
	head := current[0]
	attrs := map[string]string{
		"sub": head.Sub, "scope": head.Scope, "aud": head.Aud,
		"started_at": started, "renewed_at": head.IssuedAt, "expires_at": head.ExpiresAt,
		"rotations": fmt.Sprint(len(rows)),
	}
	if revoked != "" {
		attrs["revoked_at"] = revoked
	}
	return objstore.Write{
		Op: objstore.Put, Entity: RefreshFamilies, Key: key(fam), Attrs: attrs,
	}, nil
}

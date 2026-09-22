package authstore

// refresh_tokens.go holds every read and write of a login's rotation lineage.
// `refresh_tokens` holds one item per issued token, sorted by its hash, with
// the `used_at` tombstone that makes a rotation one-time. `refresh_families`
// holds ONE item per lineage, sorted by the family id, with `revoked_at` — the
// tombstone that makes a family killable — plus the two numbers a session
// listing needs. A family is one item, so revocation is a single guarded
// write (spec 6/40 §"What authd asks"), and a successor is a member of its
// family, so a dead family has no live member to leak (BUGS F36).

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"
	"uuid"

	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/obs"
	"github.com/kronael/arizuko/resreg/resources"
)

// The three ways a refresh write reports a store that cannot answer. None is a
// lost race: a race is committed=false, which the caller reads as won=false.
var (
	errStartRefused = errors.New("a fresh refresh family collided with an existing one")
	errNoFamily     = errors.New("the refresh family has no item; nothing can rotate into it")
	errBadRotations = errors.New("the refresh family's rotation count is not a number")
)

// RefreshToken is one refresh token as the daemon reads it. Used and Revoked
// are reported rather than filtered: reuse and a dead family are different
// answers.
type RefreshToken struct {
	Family  string
	Sub     string
	Scope   []string
	Aud     string
	Expires time.Time
	Used    bool
	Revoked bool
}

// StartRefreshFamily stores a new refresh token starting a FRESH family: raw is
// returned to the caller, only the hash is persisted. The family item and its
// first token go in one batch, because a token whose family is missing is a
// credential nothing can revoke.
func (s *Store) StartRefreshFamily(sub string, scope []string, aud string, ttl time.Duration) (string, error) {
	raw, err := genRefresh()
	if err != nil {
		return "", err
	}
	fam := newFamilyID()
	issued, expires := now(), time.Now().Add(ttl).UTC().Format(time.RFC3339)
	committed, err := s.s.GuardedBatch(ctx(), nil, []objstore.Write{
		{
			Op: objstore.Put, Entity: RefreshFamilies, Key: key(fam),
			Attrs: map[string]string{
				"sub": sub, "scope": strings.Join(scope, ","), "aud": aud,
				"started_at": issued, "renewed_at": issued, "expires_at": expires,
				"rotations": "1",
			},
			Cond: objstore.Cond{Item: objstore.ItemAbsent},
		},
		refreshTokenPut(HashToken(raw), fam, sub, scope, aud, issued, expires),
	})
	if err != nil {
		return "", err
	}
	if !committed {
		return "", errStartRefused
	}
	obs.RecordTokenMint("refresh")
	return raw, nil
}

// RefreshTokenBySecret reads the token a presented secret addresses, plus the
// family it belongs to. It applies no liveness predicate: refusing to find a
// spent token would make reuse indistinguishable from a token that never
// existed. A token whose family item is missing is a state nothing writes, so
// it is reported REVOKED and logged loud.
func (s *Store) RefreshTokenBySecret(raw string) (RefreshToken, bool) {
	it, ok, err := s.point(RefreshTokens, HashToken(raw))
	if err != nil {
		slog.Error("refresh token lookup failed; the presented token is reported unknown",
			"err", err)
		return RefreshToken{}, false
	}
	if !ok {
		return RefreshToken{}, false
	}
	r := RefreshToken{
		Family: it.Attrs["family_id"],
		Sub:    it.Attrs["sub"],
		Aud:    it.Attrs["aud"],
	}
	if scope := it.Attrs["scope"]; scope != "" {
		r.Scope = strings.Split(scope, ",")
	}
	r.Expires, _ = time.Parse(time.RFC3339, it.Attrs["expires_at"])
	_, r.Used = it.Attrs["used_at"]

	fam, ok, err := s.point(RefreshFamilies, r.Family, "revoked_at")
	switch {
	case err != nil:
		slog.Error("refresh family lookup failed; the presented token is reported revoked",
			"family", r.Family, "err", err)
		r.Revoked = true
	case !ok:
		slog.Error("refresh token has no family item; it is reported revoked and mints nothing",
			"family", r.Family, "sub", r.Sub)
		r.Revoked = true
	default:
		_, r.Revoked = fam.Attrs["revoked_at"]
	}
	return r, true
}

// ClaimRefreshToken atomically claims a refresh token: exactly one of N
// concurrent redeems wins. Losers see won=false and must NOT rotate. The guard
// has TWO conjuncts (BUGS F36): `used_at` Absent on the TOKEN picks the single
// winner, and `revoked_at` Absent on the FAMILY refuses a token whose family
// died between the caller's lookup and this write.
func (s *Store) ClaimRefreshToken(raw string) (won bool, err error) {
	hash := HashToken(raw)
	it, ok, err := s.point(RefreshTokens, hash, "family_id")
	if err != nil || !ok {
		return false, err
	}
	return s.s.GuardedBatch(ctx(),
		[]objstore.Check{familyLive(it.Attrs["family_id"])},
		[]objstore.Write{claimWrite(hash)})
}

// ClaimAndRotateRefresh claims raw and, on a win, writes its successor and the
// family's new counters in the SAME batch (BUGS F36): a revoke that landed
// first refuses the whole batch, so the successor is never written rather
// than written and then orphaned. won=false means someone else claimed it or
// the family is already dead; both are reuse from the caller's side.
func (s *Store) ClaimAndRotateRefresh(raw, fam, sub string, scope []string, aud string, ttl time.Duration) (newRaw string, won bool, err error) {
	newRaw, err = genRefresh()
	if err != nil {
		return "", false, err
	}
	rotations, err := s.familyRotations(fam)
	if err != nil {
		return "", false, err
	}
	issued, expires := now(), time.Now().Add(ttl).UTC().Format(time.RFC3339)
	committed, err := s.s.GuardedBatch(ctx(), nil, []objstore.Write{
		{
			Op: objstore.Update, Entity: RefreshFamilies, Key: key(fam),
			Attrs: map[string]string{
				"sub": sub, "scope": strings.Join(scope, ","), "aud": aud,
				"renewed_at": issued, "expires_at": expires,
				"rotations": strconv.Itoa(rotations + 1),
			},
			Cond: objstore.Cond{Where: []objstore.Cmp{
				{Attr: "revoked_at", Op: objstore.Absent},
				{Attr: "rotations", Op: objstore.Eq, Value: strconv.Itoa(rotations)},
			}},
		},
		claimWrite(HashToken(raw)),
		refreshTokenPut(HashToken(newRaw), fam, sub, scope, aud, issued, expires),
	})
	if err != nil || !committed {
		return "", false, err
	}
	obs.RecordTokenMint("refresh")
	return newRaw, true, nil
}

// RevokeFamily kills a refresh family: reuse detection, logout, the REST face
// and the dashboard all take this one writer. It reports whether it KILLED
// something; the /v1 face 404s on killed=false, because an operator mistyping
// an id during an incident must not be told the session is dead.
func (s *Store) RevokeFamily(familyID string) (killed bool, err error) {
	return s.s.GuardedBatch(ctx(), nil, []objstore.Write{{
		Op: objstore.Update, Entity: RefreshFamilies, Key: key(familyID),
		Attrs: map[string]string{"revoked_at": now()},
		Cond: objstore.Cond{Where: []objstore.Cmp{
			{Attr: "revoked_at", Op: objstore.Absent},
		}},
	}})
}

// maxSessionsLimit caps one page, matching audit's own ceiling.
const maxSessionsLimit = 200

// SessionsByFamily lists one row per family — a login and everything it
// rotated into. An empty sub means every family. The two numbers are
// attributes the rotation sets in the batch it was making anyway (spec 6/40
// §"What authd asks", item 1). The order is applied here: the sort key is the
// family id because revocation addresses one, and a family's POSITION changes
// on every rotation while its identity does not.
func (s *Store) SessionsByFamily(sub string, limit int, at time.Time) ([]resources.SessionsRow, error) {
	if limit <= 0 || limit > maxSessionsLimit {
		limit = 50
	}
	q := objstore.Query{Entity: RefreshFamilies, Partition: Partition}
	if sub != "" {
		q.Where = []objstore.Cmp{{Attr: "sub", Op: objstore.Eq, Value: sub}}
	}
	items, err := s.s.Query(ctx(), q)
	if err != nil {
		return nil, err
	}
	out := make([]resources.SessionsRow, 0, len(items))
	for _, it := range items {
		_, revoked := it.Attrs["revoked_at"]
		rotations, _ := strconv.Atoi(it.Attrs["rotations"])
		out = append(out, resources.SessionsRow{
			FamilyID:  it.Key.Sort,
			Sub:       it.Attrs["sub"],
			Scope:     it.Attrs["scope"],
			Aud:       it.Attrs["aud"],
			StartedAt: it.Attrs["started_at"],
			RenewedAt: it.Attrs["renewed_at"],
			ExpiresAt: it.Attrs["expires_at"],
			Rotations: rotations,
			Status:    sessionStatus(revoked, it.Attrs["expires_at"], at),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return renewedAt(out[i]).After(renewedAt(out[j]))
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// renewedAt reads a row's renewal instant. An unparseable one sorts last, so a
// damaged attribute cannot push a row to the front of the operator's page.
func renewedAt(r resources.SessionsRow) time.Time {
	t, err := time.Parse(time.RFC3339, r.RenewedAt)
	if err != nil {
		return time.Time{}
	}
	return t
}

// familyRotations reads how many tokens a family has issued.
func (s *Store) familyRotations(fam string) (int, error) {
	it, ok, err := s.point(RefreshFamilies, fam, "rotations")
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, errNoFamily
	}
	n, err := strconv.Atoi(it.Attrs["rotations"])
	if err != nil {
		return 0, errBadRotations
	}
	return n, nil
}

// familyLive is the condition "this family has not been killed", stated on an
// item the batch does not modify.
func familyLive(fam string) objstore.Check {
	return objstore.Check{
		Entity: RefreshFamilies, Key: key(fam),
		Cond: objstore.Cond{Item: objstore.ItemPresent, Where: []objstore.Cmp{
			{Attr: "revoked_at", Op: objstore.Absent},
		}},
	}
}

// claimWrite is the compare-and-set that spends one token: set used_at, but
// only on a token that carries none.
func claimWrite(hash string) objstore.Write {
	return objstore.Write{
		Op: objstore.Update, Entity: RefreshTokens, Key: key(hash),
		Attrs: map[string]string{"used_at": now()},
		Cond: objstore.Cond{Where: []objstore.Cmp{
			{Attr: "used_at", Op: objstore.Absent},
		}},
	}
}

// refreshTokenPut writes one token into a family. It refuses to overwrite: a
// hash that is already there is a token already issued, never one to replace.
func refreshTokenPut(hash, fam, sub string, scope []string, aud, issued, expires string) objstore.Write {
	return objstore.Write{
		Op: objstore.Put, Entity: RefreshTokens, Key: key(hash),
		Attrs: map[string]string{
			"family_id": fam, "sub": sub, "scope": strings.Join(scope, ","),
			"aud": aud, "issued_at": issued, "expires_at": expires,
		},
		Cond: objstore.Cond{Item: objstore.ItemAbsent},
	}
}

// sessionStatus collapses the tombstones plus the clock into one word. Revoked
// outranks expired: how a session ended is what an incident review asks.
func sessionStatus(revoked bool, expiresAt string, at time.Time) string {
	if revoked {
		return "revoked"
	}
	if exp, err := time.Parse(time.RFC3339, expiresAt); err == nil && !at.Before(exp) {
		return "expired"
	}
	return "active"
}

// newFamilyID names a fresh lineage.
func newFamilyID() string { return uuid.New().String() }

// genRefresh mints the raw secret a refresh token is addressed by.
func genRefresh() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

package store

// invites.go holds every read and write of `invites`: one item per live invite,
// minted once and consumed up to max_uses times. Nothing enforces that yet —
// onboarding.go's header says which guard does not cover this package and why.
//
// THE PARTITION IS store.OwnerOnbod, for onboarding.go's reason and not a
// second one: `invites` is a registered resreg resource whose owner subsystem
// IS this string, so a different spelling here would hide every invite from
// `arizuko export` without any read failing.
//
// THE SORT KEY IS `ref`, which the table already had as its primary key: the
// hash-at-rest handle every read surface hands out and the one thing a
// redemption resolves a raw bearer to (I1). Every live ref is 64 bytes — 4 of 4
// on sloth, the only instance holding any — and that width is deliberately NOT
// declared: OrderedSort is for a key a read COMPARES BY ORDER, and nothing here
// does. The only key predicate is an exact match, and the export's `ORDER BY
// ref` is the coordinate's own order at any width. Declaring it would be a
// length check wearing an ordering mechanism's clothes.
//
// WHAT THE CONSTANT COSTS is a whole-collection read for the list and for the
// export. 4 invites fleet-wide, measured 2026-09-07 across all three snapshots.
// An issuer is not the partition because the list has an OPTIONAL issuer filter
// and the redemption path holds a ref and no issuer at all.
//
// THE REDEMPTION IS A COMPARE-AND-SWAP, not an in-place increment, and that is
// what makes concurrent redemptions race correctly. `used_count = used_count +
// 1 WHERE used_count < max_uses` is one SQL statement; over the interface it is
// a read, a check in Go, and a conditional write whose condition is THE VALUE
// THE READ SAW. A caller that loses that condition lost the race, and losing is
// ErrInviteUnavailable — the same answer the statement's zero rows gave.
//
// `invite_redemptions` is its own entity, in invite_redemptions.go, and it goes
// in on THIS transaction: consumeInvite is one commit carrying the counter's
// guarded batch, the redemption and the audit row, so a redemption cannot exist
// for an invite whose counter did not move.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/objstore"
)

// Invite is one invite row. It carries no raw-token field — the bearer is
// generated, hashed, and returned exactly once by CreateInvite (mirrors
// route_tokens' RouteToken, which likewise has no token field). Every other
// accessor works from Ref, the row's hash-at-rest primary key.
type Invite struct {
	Ref         string
	TargetGlob  string
	IssuedBySub string
	IssuedAt    time.Time
	ExpiresAt   *time.Time
	MaxUses     int
	UsedCount   int
}

// ErrInviteUnavailable is returned by ConsumeInvite when the token is
// missing, exhausted, or expired.
var ErrInviteUnavailable = errors.New("invite unavailable")

// ErrInviteRefUnknown is returned when a ref matches no live invite.
var ErrInviteRefUnknown = errors.New("invite ref not found")

// The attributes an invite item carries. `ref` is NOT among them: it IS the
// sort key. `expires_at` is ABSENT on an invite minted with no --expires, which
// is what the COALESCE-on-read / nil-on-write hook pair on the resource used to
// buy — absent is the only null this model has.
const (
	attrInvTargetGlob  = "target_glob"
	attrInvIssuedBySub = "issued_by_sub"
	attrInvIssuedAt    = "issued_at"
	attrInvExpiresAt   = "expires_at"
	attrInvMaxUses     = "max_uses"
	attrInvUsedCount   = "used_count"
)

// Both instants are declared: the list orders by issued_at and the redemption
// compares expires_at, and both are byte comparisons (onboarding.go's header
// says why 20 and what a short value would do).
func init() {
	objstore.OrderedAttr(Invites, attrInvIssuedAt, onbodTSWidth)
	objstore.OrderedAttr(Invites, attrInvExpiresAt, onbodTSWidth)
}

// inviteKey is the coordinate of one invite.
func inviteKey(ref string) (objstore.Key, error) {
	if ref == "" {
		return objstore.Key{}, errors.New("invite carries no ref, which is its coordinate")
	}
	return objstore.Key{Partition: OwnerOnbod, Sort: ref}, nil
}

// inviteFrom renders one stored item as the row a caller reads. The ref comes
// back out of the sort key, which is where it was written.
//
// The two COUNTS are read strictly — a value that is not a number is an error —
// because they are the redemption's bound: a max_uses that read as 0 would
// refuse every redemption and a used_count that read as 0 would allow an
// unlimited number. The two INSTANTS are read leniently, which is what the scan
// this replaces did: an unparseable one leaves the zero time, and for
// expires_at that reads as "already expired", which fails closed.
func inviteFrom(it objstore.Item) (*Invite, error) {
	maxUses, err := intAttr(Invites, it, attrInvMaxUses)
	if err != nil {
		return nil, err
	}
	usedCount, err := intAttr(Invites, it, attrInvUsedCount)
	if err != nil {
		return nil, err
	}
	inv := &Invite{
		Ref:         it.Key.Sort,
		TargetGlob:  it.Attrs[attrInvTargetGlob],
		IssuedBySub: it.Attrs[attrInvIssuedBySub],
		MaxUses:     maxUses,
		UsedCount:   usedCount,
	}
	inv.IssuedAt, _ = time.Parse(time.RFC3339, it.Attrs[attrInvIssuedAt])
	if expires, ok := it.Attrs[attrInvExpiresAt]; ok {
		t, _ := time.Parse(time.RFC3339, expires)
		inv.ExpiresAt = &t
	}
	return inv, nil
}

// invitePut renders a whole invite as the item that stores it — the single
// renderer the mint, the legacy backfill, the archive restore and the migration
// tool all write through. An empty string attribute is left off and an int is
// always written, which is byte for byte what resreg's own entity path does
// (resreg/entity.go itemFor); a Go int has no unset, so 0 is a value the row
// states rather than one it omits.
func invitePut(inv Invite) (objstore.Write, error) {
	key, err := inviteKey(inv.Ref)
	if err != nil {
		return objstore.Write{}, err
	}
	attrs := map[string]string{
		attrInvMaxUses:   strconv.Itoa(inv.MaxUses),
		attrInvUsedCount: strconv.Itoa(inv.UsedCount),
	}
	setAttr(attrs, attrInvTargetGlob, inv.TargetGlob)
	setAttr(attrs, attrInvIssuedBySub, inv.IssuedBySub)
	setAttr(attrs, attrInvIssuedAt, onbodTS(inv.IssuedAt))
	if inv.ExpiresAt != nil {
		attrs[attrInvExpiresAt] = onbodTS(*inv.ExpiresAt)
	}
	return objstore.Write{
		Op: objstore.Put, Entity: Invites, Key: key, Attrs: attrs,
	}, nil
}

// BackfillInviteRefs carries rows from a freshly-migrated invites_legacy TABLE
// (the pre-I1 plaintext `token TEXT PRIMARY KEY` shape) into the invites entity.
// SQLite has no sha256(), so the SQL migration that produces invites_legacy only
// reshapes the table; this Go step does the actual hashing, one time, then drops
// invites_legacy. Idempotent: a no-op once invites_legacy is gone, which is the
// state on every live instance and on every database created since the table
// became an entity. Called from store.migrate (Open/OpenMem/Migrate all route
// through it) and onbod's openOwnedDB — every opener that runs store's invites
// migrations also runs this.
//
// The read stays SQL because its subject IS a table no entity has; the writes and
// the DROP ride the read's own transaction.
func BackfillInviteRefs(db *sql.DB) error {
	var exists int
	err := db.QueryRow(`SELECT 1 FROM sqlite_master WHERE type='table' AND name='invites_legacy'`).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // the commit below is the success path

	rows, err := tx.Query(`SELECT token, target_glob, issued_by_sub, issued_at, expires_at, max_uses, used_count FROM invites_legacy`)
	if err != nil {
		return fmt.Errorf("read invites_legacy: %w", err)
	}
	var legacy []Invite
	for rows.Next() {
		var (
			inv             Invite
			token, issuedAt string
			expiresAt       sql.NullString
		)
		if err := rows.Scan(&token, &inv.TargetGlob, &inv.IssuedBySub, &issuedAt,
			&expiresAt, &inv.MaxUses, &inv.UsedCount); err != nil {
			rows.Close()
			return fmt.Errorf("scan invites_legacy row: %w", err)
		}
		inv.Ref = TokenRef(token)
		inv.IssuedAt, _ = time.Parse(time.RFC3339, issuedAt)
		if expiresAt.Valid && expiresAt.String != "" {
			t, _ := time.Parse(time.RFC3339, expiresAt.String)
			inv.ExpiresAt = &t
		}
		legacy = append(legacy, inv)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close() // must close before reusing tx for the writes below

	writes := make([]objstore.Write, 0, len(legacy))
	for _, inv := range legacy {
		w, err := invitePut(inv)
		if err != nil {
			return fmt.Errorf("carry invite forward: %w", err)
		}
		writes = append(writes, w)
	}
	if _, err := applyChunks(context.Background(), "carry invites forward", writes,
		func(ctx context.Context, chunk []objstore.Write) (bool, error) {
			return New(db).WriteEntitiesInTx(ctx, tx, nil, chunk...)
		}); err != nil {
		return err
	}
	if _, err := tx.Exec(`DROP TABLE invites_legacy`); err != nil {
		return fmt.Errorf("drop invites_legacy: %w", err)
	}
	return tx.Commit()
}

// MintInvite validates an invite's fields and generates its bearer, returning
// the row to store and the raw token to hand back exactly once.
//
// It is separate from the write so the two callers that mint one — this
// package's audited CreateInvite and onbod's REST handler, which maps its own
// HTTP status codes onto these failures — share the rule about what a valid
// invite is rather than each holding a copy.
func MintInvite(targetGlob, issuedBySub string, maxUses int, expiresAt *time.Time) (Invite, string, error) {
	if targetGlob == "" {
		return Invite{}, "", errors.New("target_glob required")
	}
	// Strip trailing slash (subworld-create mode) before validating folder.
	check := strings.TrimSuffix(targetGlob, "/")
	if check == "" {
		check = "/"
	}
	if check != "/" && !groupfolder.IsValidFolder(check) {
		return Invite{}, "", fmt.Errorf("invalid target_glob %q", targetGlob)
	}
	if issuedBySub == "" {
		return Invite{}, "", errors.New("issued_by_sub required")
	}
	if maxUses < 1 {
		maxUses = 1
	}
	token := core.GenHexToken()
	return Invite{
		Ref:         TokenRef(token),
		TargetGlob:  targetGlob,
		IssuedBySub: issuedBySub,
		IssuedAt:    time.Now().UTC(),
		ExpiresAt:   expiresAt,
		MaxUses:     maxUses,
	}, token, nil
}

// InsertInviteTx stores a minted invite on a transaction the CALLER owns, so
// the mutation and the audit row that records it commit together.
//
// The write is create-if-absent and its refusal is an ERROR: a ref is 256 bits
// of hash, so a collision is not a race to absorb — it means two callers minted
// the same bearer, and overwriting would revoke the first one's live invite.
func (s *Store) InsertInviteTx(ctx context.Context, tx *sql.Tx, inv Invite) error {
	w, err := invitePut(inv)
	if err != nil {
		return err
	}
	w.Cond = objstore.Cond{Item: objstore.ItemAbsent}
	stored, err := s.WriteEntitiesInTx(ctx, tx, nil, w)
	if err != nil {
		return fmt.Errorf("store the invite: %w", err)
	}
	if !stored {
		return fmt.Errorf("an invite already exists at ref %s", inv.Ref)
	}
	return nil
}

// CreateInvite mints an invite and returns the row plus the raw bearer —
// shown to the caller exactly once, never stored (mirrors route_tokens'
// GenRouteToken + InsertRouteToken split).
func (s *Store) CreateInvite(targetGlob, issuedBySub string, maxUses int, expiresAt *time.Time) (*Invite, string, error) {
	inv, token, err := MintInvite(targetGlob, issuedBySub, maxUses, expiresAt)
	if err != nil {
		return nil, "", err
	}
	if err := s.runAudited(func(tx *sql.Tx) (audit.Event, error) {
		err := s.InsertInviteTx(context.Background(), tx, inv)
		return audit.Event{
			Category: audit.CategoryAuthN,
			Action:   "invite.create",
			Actor:    "user:" + issuedBySub,
			ActorSub: issuedBySub,
			Surface:  audit.SurfaceGateway,
			Resource: "invites/" + inv.Ref[:8],
			Outcome:  audit.OutcomeOK,
			ParamsSummary: map[string]any{
				"target_glob": targetGlob,
				"max_uses":    inv.MaxUses,
			},
		}, err
	}); err != nil {
		return nil, "", err
	}
	return &inv, token, nil
}

// readInvite is the point lookup behind every ref-addressed operation, so
// "which invite is this" cannot mean one thing to the reader and another to the
// redemption. A ref that matches nothing is ErrInviteRefUnknown.
func (s *Store) readInvite(ctx context.Context, ref string) (*Invite, error) {
	key, err := inviteKey(ref)
	if err != nil {
		return nil, err
	}
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: Invites, Partition: key.Partition,
		Key: objstore.KeyPredicate{Op: objstore.ExactKey, Value: key.Sort},
	})
	if err != nil {
		return nil, fmt.Errorf("read invite %s: %w", ref, err)
	}
	if len(items) == 0 {
		return nil, ErrInviteRefUnknown
	}
	return inviteFrom(items[0])
}

// GetInvite resolves the raw bearer token presented at redemption to its row.
func (s *Store) GetInvite(token string) (*Invite, error) {
	return s.readInvite(context.Background(), TokenRef(token))
}

// ListInvites reads every invite, newest first, optionally narrowed to one
// issuer. The order is an ATTRIBUTE's rather than the coordinate's — a ref is a
// hash and sorts by nothing a person means — so it is applied here over a
// whole-collection read; the file header bounds what that costs.
func (s *Store) ListInvites(forIssuer string) ([]Invite, error) {
	var where []objstore.Cmp
	if forIssuer != "" {
		where = append(where, objstore.Cmp{
			Attr: attrInvIssuedBySub, Op: objstore.Eq, Value: forIssuer})
	}
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: Invites, Partition: OwnerOnbod, Where: where,
	})
	if err != nil {
		return nil, fmt.Errorf("read the invites: %w", err)
	}
	out := make([]Invite, 0, len(items))
	for _, it := range items {
		inv, err := inviteFrom(it)
		if err != nil {
			return nil, err
		}
		out = append(out, *inv)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].IssuedAt.After(out[j].IssuedAt) })
	return out, nil
}

// RevokeInviteByRefTx deletes the invite identified by ref (TokenRef) on a
// transaction the CALLER owns — ref IS the coordinate (I1), so this is a direct
// delete, no resolve step. Unknown ref → ErrInviteRefUnknown, so a caller cannot
// mistake a no-op for a revocation; the condition is what the RowsAffected check
// it replaces was.
func (s *Store) RevokeInviteByRefTx(ctx context.Context, tx *sql.Tx, ref string) error {
	key, err := inviteKey(ref)
	if err != nil {
		return err
	}
	revoked, err := s.WriteEntitiesInTx(ctx, tx, nil, objstore.Write{
		Op: objstore.Delete, Entity: Invites, Key: key,
		Cond: objstore.Cond{Item: objstore.ItemPresent},
	})
	if err != nil {
		return fmt.Errorf("revoke invite %s: %w", ref, err)
	}
	if !revoked {
		return ErrInviteRefUnknown
	}
	return nil
}

// RevokeInviteByRef is RevokeInviteByRefTx with a transaction and an audit row
// of its own, for the CLI and the chat command, which hold neither.
func (s *Store) RevokeInviteByRef(ref string) error {
	return s.runAudited(func(tx *sql.Tx) (audit.Event, error) {
		if err := s.RevokeInviteByRefTx(context.Background(), tx, ref); err != nil {
			return audit.Event{}, err
		}
		return audit.Event{
			Category: audit.CategoryAuthN,
			Action:   "invite.revoke",
			Actor:    "system",
			Surface:  audit.SurfaceGateway,
			Resource: "invites/" + ref[:min(len(ref), 8)],
			Outcome:  audit.OutcomeOK,
		}, nil
	})
}

// ConsumeInvite atomically claims one use (guarding max_uses and expiry)
// and inserts an acl admin row in one transaction. Concurrent callers race on
// the counter's compare-and-swap — only the first max_uses succeed. Requires
// invites + acl in ONE DB; the split topology (acl in routd.db, invites in
// onbod.db) uses ConsumeInviteNoGrant + a separate routd-side acl write instead.
func (s *Store) ConsumeInvite(token, userSub string) (*Invite, error) {
	return s.consumeInvite(token, userSub, true)
}

// ConsumeInviteNoGrant is the split twin: same atomic claim + audit, but it
// does NOT write the acl grant — acl is routd-OWNED, so the caller writes that
// row to routd.db separately (AddACLRow, which audits into routd.db's own
// audit_log).
func (s *Store) ConsumeInviteNoGrant(token, userSub string) (*Invite, error) {
	return s.consumeInvite(token, userSub, false)
}

// usedCountWrite is the compare-and-swap that moves an invite's counter, and
// the single renderer of it: a redemption and its reversal have to agree about
// what a lost race looks like.
//
// The condition is the value the caller's own read saw, so anybody who moved
// the counter in between refuses this write. That is what `used_count =
// used_count + 1 WHERE used_count < max_uses` did in one statement, split
// across a read and a write because the interface has no in-place increment for
// a string attribute (objstore.Increment says why).
func usedCountWrite(key objstore.Key, was, next int) objstore.Write {
	return objstore.Write{
		Op: objstore.Update, Entity: Invites, Key: key,
		Attrs: map[string]string{attrInvUsedCount: strconv.Itoa(next)},
		Cond: objstore.Cond{Where: []objstore.Cmp{
			{Attr: attrInvUsedCount, Op: objstore.Eq, Value: strconv.Itoa(was)},
		}},
	}
}

// RestoreInvite reverses a ConsumeInviteNoGrant — releases one use — when the
// caller's downstream grant write (to routd.db in the split, a separate DB so
// the two can't share a tx) fails. Without this the invite is burned with no
// grant → the user is permanently locked out with no admin access.
//
// It goes through the counter PROTOCOL rather than a compare-and-swap of its
// own, because a lost race here must not become a lost decrement: the statement
// it replaces was atomic and never gave up, and Increment re-reads and retries,
// which is the same guarantee. The `used_count != 0` guard is the statement's
// `used_count > 0` — the counter is never negative, so "not zero" is the same
// set and needs no argument about byte order.
func (s *Store) RestoreInvite(token string) error {
	key, err := inviteKey(TokenRef(token))
	if err != nil {
		return err
	}
	// A refusal is NOT an error: it means the counter is already zero, which is
	// what the statement's `used_count > 0` said about a row it did not touch.
	if _, _, err := s.incrementEntity(context.Background(), Invites, key,
		attrInvUsedCount, -1,
		[]objstore.Cmp{{Attr: attrInvUsedCount, Op: objstore.Ne, Value: "0"}}, nil); err != nil {
		return fmt.Errorf("release a use of invite %s: %w", key.Sort, err)
	}
	return nil
}

func (s *Store) consumeInvite(token, userSub string, grantACL bool) (*Invite, error) {
	if userSub == "" {
		return nil, errors.New("user_sub required")
	}
	ctx := context.Background()
	ref := TokenRef(token)
	key, err := inviteKey(ref)
	if err != nil {
		return nil, err
	}
	// The checks the statement's WHERE held, run HERE over the row this call
	// read. They do not decide the race — the counter's condition below does —
	// they decide whether this caller has anything to race for.
	inv, err := s.readInvite(ctx, ref)
	if err != nil {
		if errors.Is(err, ErrInviteRefUnknown) {
			return nil, ErrInviteUnavailable
		}
		return nil, err
	}
	now := time.Now().UTC()
	if inv.UsedCount >= inv.MaxUses {
		return nil, ErrInviteUnavailable
	}
	if inv.ExpiresAt != nil && !inv.ExpiresAt.After(now) {
		return nil, ErrInviteUnavailable
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck // the commit below is the success path

	claimed, err := s.WriteEntitiesInTx(ctx, tx, nil,
		usedCountWrite(key, inv.UsedCount, inv.UsedCount+1))
	if err != nil {
		return nil, err
	}
	if !claimed {
		// Somebody else moved the counter between this call's read and its
		// write, so this caller lost — the same answer the statement gave by
		// updating no row. The deferred rollback undoes nothing, because a
		// refused batch stops before it writes.
		return nil, ErrInviteUnavailable
	}
	// The row the caller gets back is the row AFTER the claim, which is what
	// the statement's RETURNING handed over; the audit summary reads it.
	inv.UsedCount++

	// The two halves of a redemption, split on the trailing slash.
	//
	// Subgroup-create invites (trailing slash) grant no folder access here — the
	// folder does not exist until the username picker names it, so there is no
	// scope to grant admin over. What they DO carry is the parent subtree the
	// operator invited this caller into, and that fact has to be recorded: it is
	// the authority handleCreateWorld derives the parent folder from. It used to
	// ride in the `pending_target` cookie, which the caller owns and could forge
	// to name any tenant's subtree (BUGS F50). The item goes in regardless of
	// grantACL — invite_redemptions is onbod-owned, so it lands in the same DB
	// the invites item just moved in, split or not.
	if strings.HasSuffix(inv.TargetGlob, "/") {
		at, err := redemptionNowStamp()
		if err != nil {
			return nil, err
		}
		w, err := redemptionWrite(userSub, at, inv.TargetGlob)
		if err != nil {
			return nil, err
		}
		recorded, err := s.WriteEntitiesInTx(ctx, tx, nil, w)
		if err != nil {
			return nil, err
		}
		if !recorded {
			return nil, fmt.Errorf("record %s's redemption: position %s is taken, so two "+
				"redemptions were minted at one instant with one suffix", userSub, at)
		}
	} else if grantACL {
		// The grant is `acl`'s own write, rendered by `acl`'s own writer, so the
		// coordinate and the three attributes are the ones AddACLRow and the
		// manifest apply already write. Create-if-absent is what `INSERT OR
		// IGNORE` said: redeeming a second invite onto a glob the caller already
		// holds must not restamp who granted it or when.
		//
		// It rides in THIS transaction, which is the single-database topology's
		// whole point — the claim and the grant commit together. The split
		// topology cannot: `acl` is routd-owned and lives in another file, so
		// onbod calls ConsumeInviteNoGrant and writes the row through AddACLRow
		// against routd.db instead.
		w, err := aclPut(aclDefaults(core.ACLRow{
			Principal: userSub,
			Action:    "admin",
			Scope:     inv.TargetGlob,
			Effect:    "allow",
			GrantedBy: "invite",
		}))
		if err != nil {
			return nil, err
		}
		if _, err := s.WriteEntitiesInTx(ctx, tx, nil, w); err != nil {
			return nil, err
		}
	}
	if err := audit.EmitInTx(ctx, s.Obj(), tx, audit.Event{
		Category: audit.CategoryAuthN,
		Action:   "invite.consume",
		Actor:    "user:" + userSub,
		ActorSub: userSub,
		Surface:  audit.SurfaceGateway,
		Resource: "invites/" + ref[:8],
		Outcome:  audit.OutcomeOK,
		ParamsSummary: map[string]any{
			"target_glob": inv.TargetGlob,
			"used_count":  inv.UsedCount,
		},
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return inv, nil
}

// --- the archive lane (spec 5/8 "Secret and token values") ----------------

// ArchiveInviteRow is one row of the archive's invites document: InvitesRow's
// shape (resreg/resources/invites.go) with Ref emitted — the config
// manifest hides it (yaml:"-") because Ref is the hash-at-rest PK, but the
// archive's UPSERT lane needs it as the conflict key.
type ArchiveInviteRow struct {
	Ref         string `yaml:"ref"`
	TargetGlob  string `yaml:"target_glob"`
	IssuedBySub string `yaml:"issued_by_sub"`
	IssuedAt    string `yaml:"issued_at"`
	ExpiresAt   string `yaml:"expires_at,omitempty"`
	MaxUses     int    `yaml:"max_uses"`
	UsedCount   int    `yaml:"used_count"`
}

// archiveInvitePut renders one archive row as the item that stores it. It goes
// through the raw attributes rather than through Invite, because the archive
// carries the instants as the TEXT they are stored as and a round trip through
// time.Parse would re-render — and silently rewrite — a value the document
// holds verbatim.
func archiveInvitePut(r ArchiveInviteRow) (objstore.Write, error) {
	key, err := inviteKey(r.Ref)
	if err != nil {
		return objstore.Write{}, err
	}
	attrs := map[string]string{
		attrInvMaxUses:   strconv.Itoa(r.MaxUses),
		attrInvUsedCount: strconv.Itoa(r.UsedCount),
	}
	setAttr(attrs, attrInvTargetGlob, r.TargetGlob)
	setAttr(attrs, attrInvIssuedBySub, r.IssuedBySub)
	setAttr(attrs, attrInvIssuedAt, r.IssuedAt)
	setAttr(attrs, attrInvExpiresAt, r.ExpiresAt)
	return objstore.Write{
		Op: objstore.Put, Entity: Invites, Key: key, Attrs: attrs,
	}, nil
}

// readArchiveInvites is the whole collection as the archive spells it, in ref
// order — the coordinate's own order, so the driver supplies it.
func (s *Store) readArchiveInvites(ctx context.Context) ([]ArchiveInviteRow, error) {
	items, err := s.Obj().Query(ctx, objstore.Query{Entity: Invites, Partition: OwnerOnbod})
	if err != nil {
		return nil, fmt.Errorf("query invites: %w", err)
	}
	out := make([]ArchiveInviteRow, 0, len(items))
	for _, it := range items {
		maxUses, err := intAttr(Invites, it, attrInvMaxUses)
		if err != nil {
			return nil, err
		}
		usedCount, err := intAttr(Invites, it, attrInvUsedCount)
		if err != nil {
			return nil, err
		}
		out = append(out, ArchiveInviteRow{
			Ref:         it.Key.Sort,
			TargetGlob:  it.Attrs[attrInvTargetGlob],
			IssuedBySub: it.Attrs[attrInvIssuedBySub],
			IssuedAt:    it.Attrs[attrInvIssuedAt],
			ExpiresAt:   it.Attrs[attrInvExpiresAt],
			MaxUses:     maxUses,
			UsedCount:   usedCount,
		})
	}
	return out, nil
}

// ExportInvites reads every invite with its ref hash-at-rest coordinate.
func (s *Store) ExportInvites(ctx context.Context) ([]ArchiveInviteRow, error) {
	rows, err := s.readArchiveInvites(ctx)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return rows, nil
}

// ImportInvites writes by ref (the coordinate) and never replaces — matching
// ImportRouteTokens' reasoning: an invite's content never legitimately changes
// once issued (used_count moves via redemption, a separate imperative path, not
// via archive restore), so ignore-if-present is equivalent to UPSERT here.
// Performs NO gating — same split of responsibility as ImportRouteTokens.
//
// The already-present set is read first, for ImportOnboarding's reason: a batch
// is all-or-nothing, so one create-if-absent that lost would refuse every row
// riding with it, where the INSERT OR IGNORE this replaces skipped one and
// carried on.
func (s *Store) ImportInvites(ctx context.Context, rows []ArchiveInviteRow) (int, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	have, err := s.readArchiveInvites(ctx)
	if err != nil {
		return 0, err
	}
	seen := make(map[string]bool, len(have)+len(rows))
	for _, r := range have {
		seen[r.Ref] = true
	}
	writes := make([]objstore.Write, 0, len(rows))
	for _, r := range rows {
		if seen[r.Ref] {
			continue
		}
		seen[r.Ref] = true
		w, err := archiveInvitePut(r)
		if err != nil {
			return 0, fmt.Errorf("insert invite %s: %w", r.Ref, err)
		}
		w.Cond = objstore.Cond{Item: objstore.ItemAbsent}
		writes = append(writes, w)
	}
	if _, err := s.applyInChunks(ctx, "import the invites", writes); err != nil {
		return 0, err
	}
	return len(rows), nil
}

// CountInvites counts every invite — the proven-empty-target gate's onbod-side
// twin of CountRouteTokens.
func (s *Store) CountInvites(ctx context.Context) (int, error) {
	rows, err := s.readArchiveInvites(ctx)
	return len(rows), err
}

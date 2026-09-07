package authstore

// refresh_tokens.go holds every read and write of a login's rotation lineage.
//
// TWO entities, and the second one is what the port ADDS. `refresh_tokens`
// holds one item per issued token, sorted by its hash, carrying the `used_at`
// tombstone that makes a rotation one-time. `refresh_families` holds ONE item
// per lineage, sorted by the family id, carrying `revoked_at` — the tombstone
// that makes a family killable — plus the two numbers a session listing needs.
//
// A FAMILY IS A THING. Revocation used to be a conditional UPDATE across every
// row a login ever rotated through: an unbounded set-wide write, which is the
// shape no key-value engine carries and the one operation on this table that
// did not port (spec 6/40 §"What authd asks", item 2). As one field on one item
// it is a single guarded write, and it is the model arizuko would want under
// SQL too — `revoked_at` on 1 item instead of on the 72 rows one live login has
// rotated through.
//
// It also makes BUGS F36's worst outcome unrepresentable rather than merely
// prevented. A successor minted into a family that was killed in the gap used
// to be a row with `revoked_at` NULL, live for 30 days; now a successor is a
// member of the family it belongs to, and a dead family has no live member to
// leak — there is no second place for the revoked state to be missing from.

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

// The three ways a refresh write reports a store that cannot answer. None of
// them is a lost race: a race is committed=false, which the caller reads as
// won=false and never as a fault.
var (
	errStartRefused = errors.New("a fresh refresh family collided with an existing one")
	errNoFamily     = errors.New("the refresh family has no item; nothing can rotate into it")
	errBadRotations = errors.New("the refresh family's rotation count is not a number")
)

// RefreshToken is one refresh token as the daemon reads it: the lineage it
// belongs to, what it may mint, and the two tombstones. Used and Revoked are
// reported rather than filtered — the caller distinguishes reuse from a dead
// family, and they are different answers.
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
// family it belongs to. It applies no liveness predicate at all — an expired,
// spent or revoked token comes back with its tombstones set, because refusing
// to find one would make reuse indistinguishable from a token that never
// existed.
//
// A token whose family item is missing is reported REVOKED and logged loud. It
// is a state nothing writes — every token is inserted in the same batch as its
// family — so it means the store is damaged, and the safe reading of a
// credential whose lineage cannot be found is that it does not mint.
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
// concurrent redeems wins. Losers see won=false and must NOT rotate — a second
// live successor would fork the family and reuse-detection would never fire.
//
// The guard still has TWO conjuncts, each closing a different half of BUGS F36,
// and the family entity is what moved one of them from a column to a Check:
// `used_at` Absent on the TOKEN picks the single winner, and `revoked_at`
// Absent on the FAMILY refuses a token whose family died between the caller's
// lookup and this write. Without the second, a logout committing in that gap is
// not seen and a successor is minted into a family the user just killed — a
// revoke that commits BEFORE the batch opens is not an interleave, so the claim
// itself has to look. A Check is precisely a condition on an item the batch does
// not modify, and the driver takes the same lock for it that the write would.
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
// family's new counters in the SAME batch — the ordering fix for BUGS F36.
//
// Separate writes with the grants re-snapshot between them let a competing
// redeem's family revoke commit in the gap: it revoked the rows that existed at
// that moment, the winner then inserted a successor with no tombstone, and a
// family killed for reuse walked away with a live 30-day credential.
//
// One batch makes that interleave inexpressible. The family's own write carries
// both conditions — `revoked_at` Absent and the rotation count it read — so a
// revoke that landed first refuses the whole batch, and the successor is never
// written rather than written and then orphaned.
//
// won=false means "someone else claimed it, or the family is already dead";
// both are reuse from the caller's side, and both leave the store untouched.
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
// and the dashboard all take this one writer, so no second write can drift from
// its `revoked_at` Absent guard.
//
// It reports whether it KILLED something, not how many rows it touched. The row
// count never was the answer to "did this work" — re-revoking a dead family
// legitimately changed zero rows — and now there is one item to change, so a
// count could only ever be 0 or 1. killed=false is the honest answer every
// caller already needed: the /v1 face 404s on it and the dashboard says "no
// live session", because an operator mistyping an id during an incident must
// not be told the session is dead.
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

// SessionsByFamily lists one row per family — a login and everything it rotated
// into, not one row per token. An empty sub means every family.
//
// It reads the FAMILY entity, which is what makes the two numbers answerable at
// all. They used to be correlated subqueries over every row sharing a family id
// — `COUNT(*)` for the rotations and `MIN(issued_at)` for the start — and no
// key-value engine answers a subquery. Both are known at write time by the
// rotation that already writes the successor, so they are attributes the
// rotation sets in the batch it was making anyway; the denormalisation costs
// nothing (spec 6/40 §"What authd asks", item 1).
//
// The order is applied here rather than by the engine. The sort key is the
// family id, because revocation addresses one family by that id; a family's
// POSITION in this listing changes on every rotation while its identity does
// not, so a key that carried the order would have to move the item on each
// refresh. Reading the partition and ordering the parsed instants is what that
// buys, and it is bounded by the number of logins an instance has ever made —
// 7 families across three live instances, measured 2026-09-07.
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

// renewedAt reads a row's renewal instant. An unparseable one sorts last rather
// than at the top, so a damaged attribute cannot push a row to the front of the
// operator's page.
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
// outranks expired: how a session ended is what an incident review asks, and a
// family killed a day before its natural expiry must not be reported as having
// merely timed out.
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

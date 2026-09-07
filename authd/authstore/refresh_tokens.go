package authstore

// refresh_tokens.go holds every read and write of `refresh_tokens`: one row per
// issued token, chained into a family by family_id, and the tombstone columns
// (used_at, revoked_at) that make a rotation one-time and a family killable.

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"strings"
	"time"
	"uuid"

	"github.com/kronael/arizuko/obs"
	"github.com/kronael/arizuko/resreg/resources"
)

// RefreshToken is one refresh row as the daemon reads it: the lineage it
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
// returned to the caller, only the hash is persisted.
func (s *Store) StartRefreshFamily(sub string, scope []string, aud string, ttl time.Duration) (string, error) {
	raw, err := addToFamily(s.db, "", sub, scope, aud, ttl)
	if err != nil {
		return "", err
	}
	obs.RecordTokenMint("refresh")
	return raw, nil
}

// addToFamily writes one refresh row into family fam (empty = new family) and
// returns the raw token. It does NOT record the mint metric: on the rotation
// path the caller must commit first, and a counter incremented by a rolled-back
// transaction is a lie.
func addToFamily(x execer, fam, sub string, scope []string, aud string, ttl time.Duration) (string, error) {
	raw, err := genRefresh()
	if err != nil {
		return "", err
	}
	if fam == "" {
		fam = newFamilyID()
	}
	exp := time.Now().Add(ttl)
	_, err = x.Exec(
		`INSERT INTO refresh_tokens (token_hash, family_id, sub, scope, aud, issued_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		HashToken(raw), fam, sub, strings.Join(scope, ","), aud, now(), exp.Format(time.RFC3339))
	if err != nil {
		return "", err
	}
	return raw, nil
}

// RefreshTokenBySecret reads the row a presented secret addresses. It applies
// no liveness predicate at all — an expired, spent or revoked token comes back
// with its tombstones set, because refusing to find one would make reuse
// indistinguishable from a token that never existed.
func (s *Store) RefreshTokenBySecret(raw string) (RefreshToken, bool) {
	var r RefreshToken
	var scope, exp string
	var used, revoked sql.NullString
	err := s.db.QueryRow(
		`SELECT family_id, sub, scope, aud, expires_at, used_at, revoked_at
		 FROM refresh_tokens WHERE token_hash = ?`, HashToken(raw)).
		Scan(&r.Family, &r.Sub, &scope, &r.Aud, &exp, &used, &revoked)
	if err != nil {
		return RefreshToken{}, false
	}
	if scope != "" {
		r.Scope = strings.Split(scope, ",")
	}
	r.Expires, _ = time.Parse(time.RFC3339, exp)
	r.Used = used.Valid
	r.Revoked = revoked.Valid
	return r, true
}

// ClaimRefreshToken atomically claims a refresh token: the compare-and-set means
// exactly one of N concurrent redeems wins. Losers see won=false and must NOT
// rotate — a second live successor would fork the family and reuse-detection would
// never fire.
//
// The guard has TWO conjuncts, each closing a different half of BUGS F36:
// `used_at IS NULL` picks the single winner, and `revoked_at IS NULL` refuses a
// token whose family died between the caller's lookup and this statement. Without
// the second, a logout committing in that gap is not seen and a successor is minted
// into a family the user just killed — a revoke that commits BEFORE the transaction
// opens is not an interleave, so the claim itself has to look.
func (s *Store) ClaimRefreshToken(raw string) (won bool, err error) {
	return claimRefresh(s.db, raw)
}

// claimRefresh is the claim on a caller-chosen surface, so ClaimAndRotateRefresh
// can run it inside its transaction.
func claimRefresh(x execer, raw string) (won bool, err error) {
	res, err := x.Exec(
		`UPDATE refresh_tokens SET used_at = ?
		  WHERE token_hash = ? AND used_at IS NULL AND revoked_at IS NULL`,
		now(), HashToken(raw))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// ClaimAndRotateRefresh claims raw and, on a win, inserts its successor in the SAME
// transaction — the ordering fix for BUGS F36.
//
// Two separate Exec calls with the grants re-snapshot between them let a
// competing redeem's family revoke commit in the gap: it revoked the rows that
// existed at that moment, the winner then inserted a successor with revoked_at
// NULL, and a family killed for reuse walked away with a live 30-day credential.
//
// Inside one transaction that interleave is not expressible. SQLite has a single
// writer, so a concurrent revoke either commits before the transaction opens — and
// claimRefresh's second conjunct refuses the claim — or waits and revokes the
// successor with the rest of the family.
//
// won=false means "someone else claimed it, or the family is already dead"; both
// are reuse from the caller's side, and both leave the DB untouched.
func (s *Store) ClaimAndRotateRefresh(raw, fam, sub string, scope []string, aud string, ttl time.Duration) (newRaw string, won bool, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback()
	won, err = claimRefresh(tx, raw)
	if err != nil || !won {
		return "", false, err
	}
	newRaw, err = addToFamily(tx, fam, sub, scope, aud, ttl)
	if err != nil {
		return "", false, err
	}
	if err := tx.Commit(); err != nil {
		return "", false, err
	}
	obs.RecordTokenMint("refresh")
	return newRaw, true, nil
}

// RevokeFamily kills a refresh family on the store's own handle: reuse
// detection and logout both take this one.
func (s *Store) RevokeFamily(familyID string) (int64, error) {
	return revokeFamily(s.db, familyID)
}

// RevokeFamilyTx kills a refresh family on a caller-owned transaction, so an
// operator revoke and the audit row resreg writes commit together or not at all.
func RevokeFamilyTx(tx *sql.Tx, familyID string) (int64, error) {
	return revokeFamily(tx, familyID)
}

// revokeFamily is the ONE writer that kills a refresh family. Every caller —
// reuse detection, logout, the REST face and the dashboard — reaches this
// statement, so no second UPDATE can drift from its `revoked_at IS NULL` guard.
// That guard is what makes re-revoking an already-dead family report zero rows
// rather than a fresh success.
func revokeFamily(x execer, familyID string) (int64, error) {
	res, err := x.Exec(
		`UPDATE refresh_tokens SET revoked_at = ? WHERE family_id = ? AND revoked_at IS NULL`,
		now(), familyID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// maxSessionsLimit caps one page, matching audit's own ceiling.
const maxSessionsLimit = 200

// SessionsByFamily projects ONE row per family off refresh_tokens — a login and
// everything it rotated into, not one row per token. An empty sub means every
// family.
//
// The family's CURRENT row is the one with `used_at IS NULL`, and there is
// exactly one per family by construction: StartRefreshFamily starts a family
// with a single unused row, and each rotation marks the presented row used while
// inserting one unused successor — atomically, since F36. That invariant is
// what lets this be a plain WHERE instead of a window function, and
// TestSessionsRowPerFamilyInvariant pins it.
//
// `token_hash` appears in no SELECT list here. The `sub` filter is a bound
// parameter, never interpolated.
func (s *Store) SessionsByFamily(sub string, limit int, at time.Time) ([]resources.SessionsRow, error) {
	if limit <= 0 || limit > maxSessionsLimit {
		limit = 50
	}
	rows, err := s.db.Query(
		`SELECT r.family_id, r.sub, r.scope, r.aud, r.issued_at, r.expires_at, r.revoked_at,
		        (SELECT COUNT(*)        FROM refresh_tokens f WHERE f.family_id = r.family_id),
		        (SELECT MIN(issued_at)  FROM refresh_tokens f WHERE f.family_id = r.family_id)
		   FROM refresh_tokens r
		  WHERE r.used_at IS NULL AND (? = '' OR r.sub = ?)
		  ORDER BY r.issued_at DESC
		  LIMIT ?`, sub, sub, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []resources.SessionsRow{}
	for rows.Next() {
		var (
			r         resources.SessionsRow
			revokedAt sql.NullString
		)
		if err := rows.Scan(&r.FamilyID, &r.Sub, &r.Scope, &r.Aud, &r.RenewedAt,
			&r.ExpiresAt, &revokedAt, &r.Rotations, &r.StartedAt); err != nil {
			return nil, err
		}
		r.Status = sessionStatus(revokedAt.Valid, r.ExpiresAt, at)
		out = append(out, r)
	}
	return out, rows.Err()
}

// sessionStatus collapses the tombstone columns plus the clock into one word.
// Revoked outranks expired: how a session ended is what an incident review
// asks, and a family killed a day before its natural expiry must not be
// reported as having merely timed out.
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

// genRefresh mints the raw secret a refresh row is addressed by.
func genRefresh() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

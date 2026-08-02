package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type AuthUser struct {
	ID          int64
	Sub         string
	Name        string
	CreatedAt   time.Time
	LinkedToSub string // empty = canonical; non-empty = points at canonical sub
}

func (s *Store) CreateAuthUser(sub, username, name string) error {
	_, err := s.db.Exec(
		`INSERT INTO user_profiles (sub, username, name, created_at)
		 VALUES (?, ?, ?, ?)`,
		sub, username, name, time.Now().Format(time.RFC3339),
	)
	return err
}

const authUserCols = `id, sub, name, created_at, linked_to_sub`

func scanAuthUser(r rowScanner) (AuthUser, bool) {
	var u AuthUser
	var created string
	var linked sql.NullString
	if err := r.Scan(&u.ID, &u.Sub, &u.Name, &created, &linked); err != nil {
		return u, false
	}
	u.CreatedAt, _ = time.Parse(time.RFC3339, created)
	if linked.Valid {
		u.LinkedToSub = linked.String
	}
	return u, true
}

func (s *Store) AuthUserBySub(sub string) (AuthUser, bool) {
	return scanAuthUser(s.db.QueryRow(
		`SELECT `+authUserCols+` FROM user_profiles WHERE sub = ?`, sub))
}

// CanonicalSub returns sub if it's canonical, else the sub it's linked to.
// Unknown subs pass through unchanged. Single resolve point: every JWT
// mint runs through this so downstream sees only canonical subs.
func (s *Store) CanonicalSub(sub string) string {
	u, ok := s.AuthUserBySub(sub)
	if !ok || u.LinkedToSub == "" {
		return sub
	}
	return u.LinkedToSub
}

var errInvalidLink = errors.New("invalid link target")

// LinkSubToCanonical attaches newSub to canonical. canonical must already
// exist in user_profiles and itself be canonical (no chains). If newSub is
// new it is inserted; if it exists it is updated.
func (s *Store) LinkSubToCanonical(newSub, name, canonical string) error {
	if newSub == "" || canonical == "" || newSub == canonical {
		return errInvalidLink
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var canonicalLink sql.NullString
	if err := tx.QueryRowContext(ctx,
		`SELECT linked_to_sub FROM user_profiles WHERE sub = ?`, canonical,
	).Scan(&canonicalLink); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errInvalidLink
		}
		return err
	}
	if canonicalLink.Valid && canonicalLink.String != "" {
		return errInvalidLink
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO user_profiles (sub, username, name, created_at, linked_to_sub)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(sub) DO UPDATE SET linked_to_sub = excluded.linked_to_sub, name = excluded.name`,
		newSub, newSub, name, time.Now().Format(time.RFC3339), canonical,
	); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) LinkedSubs(canonical string) []string {
	rows, err := s.db.Query(
		`SELECT sub FROM user_profiles WHERE linked_to_sub = ? ORDER BY sub`, canonical)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var x string
		if err := rows.Scan(&x); err == nil && x != "" {
			out = append(out, x)
		}
	}
	return out
}

// User-folder grants live in the `acl` table (post-0053). See
// store/acl.go for `AddACLRow`, `RemoveACLRow`, `UserScopes`, `ListACL`.
// Role / JID-claim membership lives in `acl_membership`; see
// store/membership.go.

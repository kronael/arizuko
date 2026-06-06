package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/kronael/arizuko/audit"
)

type AuthUser struct {
	ID          int64
	Sub         string
	Username    string
	Hash        string
	Name        string
	CreatedAt   time.Time
	LinkedToSub string // empty = canonical; non-empty = points at canonical sub
}

type AuthSession struct {
	TokenHash string
	UserSub   string
	ExpiresAt time.Time
	CreatedAt time.Time
}

func (s *Store) CreateAuthUser(sub, username, hash, name string) error {
	_, err := s.db.Exec(
		`INSERT INTO auth_users (sub, username, hash, name, created_at)
		 VALUES (?, ?, ?, ?, ?)`,
		sub, username, hash, name, time.Now().Format(time.RFC3339),
	)
	return err
}

const authUserCols = `id, sub, username, hash, name, created_at, linked_to_sub`

func scanAuthUser(r rowScanner) (AuthUser, bool) {
	var u AuthUser
	var created string
	var linked sql.NullString
	if err := r.Scan(&u.ID, &u.Sub, &u.Username, &u.Hash, &u.Name, &created, &linked); err != nil {
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
		`SELECT `+authUserCols+` FROM auth_users WHERE sub = ?`, sub))
}

func (s *Store) AuthUserByUsername(username string) (AuthUser, bool) {
	return scanAuthUser(s.db.QueryRow(
		`SELECT `+authUserCols+` FROM auth_users WHERE username = ?`, username))
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
// exist in auth_users and itself be canonical (no chains). If newSub is
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
		`SELECT linked_to_sub FROM auth_users WHERE sub = ?`, canonical,
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
		`INSERT INTO auth_users (sub, username, hash, name, created_at, linked_to_sub)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(sub) DO UPDATE SET linked_to_sub = excluded.linked_to_sub, name = excluded.name`,
		newSub, newSub, "", name, time.Now().Format(time.RFC3339), canonical,
	); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) LinkedSubs(canonical string) []string {
	rows, err := s.db.Query(
		`SELECT sub FROM auth_users WHERE linked_to_sub = ? ORDER BY sub`, canonical)
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

func (s *Store) CreateAuthSession(tokenHash, userSub string, expiresAt time.Time) error {
	return s.runAudited(func(tx *sql.Tx) (audit.Event, error) {
		_, err := tx.Exec(
			`INSERT INTO auth_sessions (token_hash, user_sub, expires_at, created_at)
			 VALUES (?, ?, ?, ?)`,
			tokenHash, userSub, expiresAt.Format(time.RFC3339), time.Now().Format(time.RFC3339),
		)
		return audit.Event{
			Category: audit.CategoryAuthN,
			Action:   "token.mint",
			Actor:    "user:" + userSub,
			ActorSub: userSub,
			Surface:  audit.SurfaceGateway,
			Resource: "sessions/" + tokenHash[:min(len(tokenHash), 8)],
			Outcome:  audit.OutcomeOK,
			ParamsSummary: map[string]any{
				"expires_at": expiresAt.Format(time.RFC3339),
			},
		}, err
	})
}

func (s *Store) AuthSession(tokenHash string) (AuthSession, bool) {
	var a AuthSession
	var expires, created string
	err := s.db.QueryRow(
		`SELECT token_hash, user_sub, expires_at, created_at FROM auth_sessions WHERE token_hash = ?`,
		tokenHash,
	).Scan(&a.TokenHash, &a.UserSub, &expires, &created)
	if err != nil {
		return a, false
	}
	a.ExpiresAt, _ = time.Parse(time.RFC3339, expires)
	a.CreatedAt, _ = time.Parse(time.RFC3339, created)
	return a, true
}

func (s *Store) DeleteAuthSession(tokenHash string) error {
	return s.runAudited(func(tx *sql.Tx) (audit.Event, error) {
		_, err := tx.Exec(`DELETE FROM auth_sessions WHERE token_hash = ?`, tokenHash)
		return audit.Event{
			Category: audit.CategoryAuthN,
			Action:   "logout",
			Actor:    "system",
			Surface:  audit.SurfaceGateway,
			Resource: "sessions/" + tokenHash[:min(len(tokenHash), 8)],
			Outcome:  audit.OutcomeOK,
		}, err
	})
}

// User-folder grants live in the `acl` table (post-0053). See
// store/acl.go for `AddACLRow`, `RemoveACLRow`, `UserScopes`, `ListACL`.
// Role / JID-claim membership lives in `acl_membership`; see
// store/membership.go.

package store

import (
	"database/sql"
	"errors"
	"time"
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
	c, ok := s.AuthUserBySub(canonical)
	if !ok || c.LinkedToSub != "" {
		return errInvalidLink
	}
	if existing, ok := s.AuthUserBySub(newSub); ok {
		if existing.LinkedToSub == canonical {
			return nil
		}
		_, err := s.db.Exec(
			`UPDATE auth_users SET linked_to_sub = ?, name = ? WHERE sub = ?`,
			canonical, name, newSub)
		return err
	}
	_, err := s.db.Exec(
		`INSERT INTO auth_users (sub, username, hash, name, created_at, linked_to_sub)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		newSub, newSub, "", name, time.Now().Format(time.RFC3339), canonical,
	)
	return err
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
	_, err := s.db.Exec(
		`INSERT INTO auth_sessions (token_hash, user_sub, expires_at, created_at)
		 VALUES (?, ?, ?, ?)`,
		tokenHash, userSub, expiresAt.Format(time.RFC3339), time.Now().Format(time.RFC3339),
	)
	return err
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
	_, err := s.db.Exec(`DELETE FROM auth_sessions WHERE token_hash = ?`, tokenHash)
	return err
}

// UserGroups returns the grant patterns for sub. Operator is implicit: a
// user with a `**` row just has `**` in the list and auth.MatchGroups
// handles it. Empty slice = no access.
func (s *Store) UserGroups(sub string) []string {
	rows, err := s.db.Query(
		`SELECT folder FROM user_groups WHERE user_sub = ? ORDER BY folder`, sub)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var folders []string
	for rows.Next() {
		var f string
		rows.Scan(&f)
		if f != "" {
			folders = append(folders, f)
		}
	}
	return folders
}

type Grant struct {
	Sub       string
	Pattern   string
	GrantedAt string
}

func (s *Store) Grant(sub, pattern string) (bool, error) {
	res, err := s.db.Exec(
		`INSERT OR IGNORE INTO user_groups (user_sub, folder, granted_at)
		 VALUES (?, ?, datetime('now'))`, sub, pattern)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *Store) Ungrant(sub, pattern string) (int64, error) {
	res, err := s.db.Exec(
		`DELETE FROM user_groups WHERE user_sub = ? AND folder = ?`, sub, pattern)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func (s *Store) Grants(sub string) ([]Grant, error) {
	rows, err := s.db.Query(
		`SELECT user_sub, folder, COALESCE(granted_at, '')
		 FROM user_groups WHERE (? = '' OR user_sub = ?)
		 ORDER BY user_sub, folder`, sub, sub)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Grant
	for rows.Next() {
		var g Grant
		if err := rows.Scan(&g.Sub, &g.Pattern, &g.GrantedAt); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

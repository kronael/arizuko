package store

import (
	"database/sql"
	"time"
)

type AuthUser struct {
	ID        int64
	Sub       string
	Username  string
	Hash      string
	Name      string
	CreatedAt time.Time
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

func (s *Store) AuthUserBySub(sub string) (AuthUser, bool) {
	var u AuthUser
	var created string
	err := s.db.QueryRow(
		`SELECT id, sub, username, hash, name, created_at FROM auth_users WHERE sub = ?`, sub,
	).Scan(&u.ID, &u.Sub, &u.Username, &u.Hash, &u.Name, &created)
	if err != nil {
		return u, false
	}
	u.CreatedAt, _ = time.Parse(time.RFC3339, created)
	return u, true
}

func (s *Store) AuthUserByUsername(username string) (AuthUser, bool) {
	var u AuthUser
	var created string
	err := s.db.QueryRow(
		`SELECT id, sub, username, hash, name, created_at FROM auth_users WHERE username = ?`, username,
	).Scan(&u.ID, &u.Sub, &u.Username, &u.Hash, &u.Name, &created)
	if err != nil {
		return u, false
	}
	u.CreatedAt, _ = time.Parse(time.RFC3339, created)
	return u, true
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

// UserGroups returns the folders a user may access. Nil = operator (has
// `**` row, unrestricted). Empty slice = no access. Non-empty = pass to
// auth.MatchGroups.
func (s *Store) UserGroups(sub string) *[]string {
	rows, err := s.db.Query(
		`SELECT folder FROM user_groups WHERE user_sub = ? ORDER BY folder`, sub)
	if err != nil {
		empty := []string{}
		return &empty
	}
	defer rows.Close()
	folders := []string{}
	for rows.Next() {
		var f string
		rows.Scan(&f)
		if f == "**" {
			return nil
		}
		if f != "" {
			folders = append(folders, f)
		}
	}
	return &folders
}

type Grant struct {
	Sub       string
	Pattern   string
	GrantedAt string
}

// Grant inserts (sub, pattern) into user_groups if absent. Returns true
// if a new row was created, false if it already existed.
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

// Ungrant removes (sub, pattern) from user_groups. Returns rows affected.
func (s *Store) Ungrant(sub, pattern string) (int64, error) {
	res, err := s.db.Exec(
		`DELETE FROM user_groups WHERE user_sub = ? AND folder = ?`, sub, pattern)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// Grants lists user_groups rows. Filter by sub if non-empty.
func (s *Store) Grants(sub string) ([]Grant, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if sub != "" {
		rows, err = s.db.Query(
			`SELECT user_sub, folder, COALESCE(granted_at, '')
			 FROM user_groups WHERE user_sub = ?
			 ORDER BY user_sub, folder`, sub)
	} else {
		rows, err = s.db.Query(
			`SELECT user_sub, folder, COALESCE(granted_at, '')
			 FROM user_groups ORDER BY user_sub, folder`)
	}
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

package store

import "time"

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

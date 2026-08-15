package store

import "time"

func (s *Store) CreateAuthUser(sub, username, name string) error {
	_, err := s.db.Exec(
		`INSERT INTO user_profiles (sub, username, name, created_at)
		 VALUES (?, ?, ?, ?)`,
		sub, username, name, time.Now().Format(time.RFC3339),
	)
	return err
}

// User-folder grants live in the `acl` table; see store/acl.go. Role /
// JID-claim membership lives in `acl_membership`; see store/membership.go.

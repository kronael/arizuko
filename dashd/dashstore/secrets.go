package dashstore

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// SecretKey names a stored secret without its value. The /dash/me surfaces
// mirror GitHub SSH keys — names visible, secrets opaque — so these reads take
// the key and the date and never touch the value column. store.ListSecrets
// answers the same question but decrypts every row on the way out, which is
// authority this page has no use for.
type SecretKey struct {
	Key       string `json:"key"`
	CreatedAt string `json:"created_at"`
}

// UserSecretKeys lists every secret the user has set, by name.
func (s *Store) UserSecretKeys(sub string) ([]SecretKey, error) {
	return s.secretKeys(
		`SELECT key, created_at FROM secrets
		 WHERE scope_kind = 'user' AND scope_id = ?
		 ORDER BY key`, sub)
}

// UserEnvKeys lists which of the named env-profile keys the user has set. The
// caller passes the profile's fixed key list, so a key the profile does not
// define cannot appear on the page even if a row exists for it.
func (s *Store) UserEnvKeys(sub string, keys []string) ([]SecretKey, error) {
	placeholders := make([]string, len(keys))
	args := make([]any, 0, len(keys)+1)
	args = append(args, sub)
	for i, k := range keys {
		placeholders[i] = "?"
		args = append(args, k)
	}
	q := fmt.Sprintf(
		`SELECT key, created_at FROM secrets
		 WHERE scope_kind = 'user' AND scope_id = ? AND key IN (%s)
		 ORDER BY key`,
		strings.Join(placeholders, ","))
	return s.secretKeys(q, args...)
}

// ErrSecretMissing reports that the user has no row for that key. The PATCH
// surfaces are update-only and answer 404 on it; a create goes through POST.
var ErrSecretMissing = errors.New("secret not set")

// UserSecretExists reports whether the user has set that key, without opening
// the value — the existence check both PATCH surfaces make before they reseal.
// It returns ErrSecretMissing when the row is absent, so the caller separates
// "never set" from "could not ask".
func (s *Store) UserSecretExists(sub, key string) error {
	var one int
	err := s.db.QueryRow(
		`SELECT 1 FROM secrets WHERE scope_kind = 'user' AND scope_id = ? AND key = ?`,
		sub, key).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrSecretMissing
	}
	return err
}

// secretKeys collects a key/created_at query. A row that fails to scan is
// skipped: one unreadable row must not hide the rest of a user's keys from
// them.
func (s *Store) secretKeys(q string, args ...any) ([]SecretKey, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []SecretKey{}
	for rows.Next() {
		var it SecretKey
		if err := rows.Scan(&it.Key, &it.CreatedAt); err != nil {
			continue
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

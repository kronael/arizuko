package dashstore

import (
	"errors"
	"slices"

	"github.com/kronael/arizuko/store"
)

// SecretKey names a stored secret without its value. The /dash/me surfaces
// mirror GitHub SSH keys — names visible, secrets opaque — so these reads take
// the key and the date and never touch the value attribute. store.ListSecrets
// answers the same question but decrypts every row on the way out, which is
// authority this page has no use for; store.SecretKeysIn leaves the sealed blob
// in the engine by projecting it away.
type SecretKey struct {
	Key       string `json:"key"`
	CreatedAt string `json:"created_at"`
}

// UserSecretKeys lists every secret the user has set, by name.
func (s *Store) UserSecretKeys(sub string) ([]SecretKey, error) {
	return s.secretKeys(sub, nil)
}

// UserEnvKeys lists which of the named env-profile keys the user has set. The
// caller passes the profile's fixed key list, so a key the profile does not
// define cannot appear on the page even if a row exists for it.
func (s *Store) UserEnvKeys(sub string, keys []string) ([]SecretKey, error) {
	return s.secretKeys(sub, keys)
}

// ErrSecretMissing reports that the user has no row for that key. The PATCH
// surfaces are update-only and answer 404 on it; a create goes through POST.
var ErrSecretMissing = errors.New("secret not set")

// UserSecretExists reports whether the user has set that key, without opening
// the value — the existence check both PATCH surfaces make before they reseal.
// It returns ErrSecretMissing when the key is absent, so the caller separates
// "never set" from "could not ask".
func (s *Store) UserSecretExists(sub, key string) error {
	items, err := s.secretKeys(sub, []string{key})
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return ErrSecretMissing
	}
	return nil
}

// secretKeys reads one user's key names in key order, keeping only the names in
// `only` when the caller passes a list. The order is the coordinate's — `key` is
// the last of the three fields secrets are addressed by — so nothing sorts here.
func (s *Store) secretKeys(sub string, only []string) ([]SecretKey, error) {
	rows, err := s.st().SecretKeysIn(store.ScopeUser, sub)
	if err != nil {
		return nil, err
	}
	items := []SecretKey{}
	for _, r := range rows {
		if only != nil && !slices.Contains(only, r.Key) {
			continue
		}
		items = append(items, SecretKey{Key: r.Key, CreatedAt: r.CreatedAt})
	}
	return items, nil
}

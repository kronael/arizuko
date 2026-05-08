package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// SecretScope is the kind of scope a secret is bound to: a folder path glob
// or an auth user sub. See specs/5/32-tenant-self-service.md §Secrets.
type SecretScope string

const (
	ScopeFolder SecretScope = "folder"
	ScopeUser   SecretScope = "user"

	// Folder path "root" is the catch-all parent walked to last by
	// FolderSecretsResolved. Concrete folders override it.
	rootFolder = "root"
)

var ErrSecretCipherNotConfigured = errors.New("secrets cipher not configured")
var ErrSecretNotFound = errors.New("secret not found")

type Secret struct {
	ScopeKind SecretScope
	ScopeID   string
	Key       string
	Value     string
	CreatedAt time.Time
}

func newSecretCipher(authSecret string) (cipher.AEAD, error) {
	if authSecret == "" {
		return nil, errors.New("auth_secret required for secrets cipher")
	}
	key := sha256.Sum256([]byte(authSecret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// encryptSecret serializes `nonce(12B) || ciphertext` for at-rest storage.
func (s *Store) encryptSecret(plaintext string) ([]byte, error) {
	if s.secretCipher == nil {
		return nil, ErrSecretCipherNotConfigured
	}
	nonce := make([]byte, s.secretCipher.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	ct := s.secretCipher.Seal(nil, nonce, []byte(plaintext), nil)
	return append(nonce, ct...), nil
}

func (s *Store) decryptSecret(blob []byte) (string, error) {
	if s.secretCipher == nil {
		return "", ErrSecretCipherNotConfigured
	}
	ns := s.secretCipher.NonceSize()
	if len(blob) < ns {
		return "", fmt.Errorf("secret blob too short: %d < %d", len(blob), ns)
	}
	nonce, ct := blob[:ns], blob[ns:]
	pt, err := s.secretCipher.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}

func validateScope(scope SecretScope, scopeID, key string) error {
	if scope != ScopeFolder && scope != ScopeUser {
		return fmt.Errorf("invalid scope_kind: %q", scope)
	}
	if scopeID == "" {
		return errors.New("scope_id required")
	}
	if key == "" {
		return errors.New("key required")
	}
	return nil
}

func (s *Store) SetSecret(scope SecretScope, scopeID, key, plaintext string) error {
	if err := validateScope(scope, scopeID, key); err != nil {
		return err
	}
	enc, err := s.encryptSecret(plaintext)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO secrets (scope_kind, scope_id, key, enc_value, created_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(scope_kind, scope_id, key) DO UPDATE SET
		   enc_value = excluded.enc_value,
		   created_at = excluded.created_at`,
		string(scope), scopeID, key, enc, time.Now().UTC().Format(time.RFC3339),
	)
	return err
}

func (s *Store) GetSecret(scope SecretScope, scopeID, key string) (Secret, error) {
	if err := validateScope(scope, scopeID, key); err != nil {
		return Secret{}, err
	}
	if s.secretCipher == nil {
		return Secret{}, ErrSecretCipherNotConfigured
	}
	var blob []byte
	var createdAt string
	err := s.db.QueryRow(
		`SELECT enc_value, created_at FROM secrets
		 WHERE scope_kind = ? AND scope_id = ? AND key = ?`,
		string(scope), scopeID, key,
	).Scan(&blob, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Secret{}, ErrSecretNotFound
	}
	if err != nil {
		return Secret{}, err
	}
	pt, err := s.decryptSecret(blob)
	if err != nil {
		return Secret{}, err
	}
	t, _ := time.Parse(time.RFC3339, createdAt)
	return Secret{ScopeKind: scope, ScopeID: scopeID, Key: key, Value: pt, CreatedAt: t}, nil
}

func (s *Store) ListSecrets(scope SecretScope, scopeID string) ([]Secret, error) {
	if scope != ScopeFolder && scope != ScopeUser {
		return nil, fmt.Errorf("invalid scope_kind: %q", scope)
	}
	if scopeID == "" {
		return nil, errors.New("scope_id required")
	}
	if s.secretCipher == nil {
		return nil, ErrSecretCipherNotConfigured
	}
	rows, err := s.db.Query(
		`SELECT key, enc_value, created_at FROM secrets
		 WHERE scope_kind = ? AND scope_id = ?
		 ORDER BY key ASC`,
		string(scope), scopeID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Secret
	for rows.Next() {
		var key string
		var blob []byte
		var createdAt string
		if err := rows.Scan(&key, &blob, &createdAt); err != nil {
			return nil, err
		}
		pt, err := s.decryptSecret(blob)
		if err != nil {
			return nil, err
		}
		t, _ := time.Parse(time.RFC3339, createdAt)
		out = append(out, Secret{ScopeKind: scope, ScopeID: scopeID, Key: key, Value: pt, CreatedAt: t})
	}
	return out, rows.Err()
}

func (s *Store) DeleteSecret(scope SecretScope, scopeID, key string) error {
	if err := validateScope(scope, scopeID, key); err != nil {
		return err
	}
	_, err := s.db.Exec(
		`DELETE FROM secrets WHERE scope_kind = ? AND scope_id = ? AND key = ?`,
		string(scope), scopeID, key,
	)
	return err
}

// folderAncestors returns folder paths for resolution, deepest first, ending with "root".
// e.g. "atlas/eng/sre" → ["atlas/eng/sre", "atlas/eng", "atlas", "root"].
func folderAncestors(folder string) []string {
	folder = strings.Trim(folder, "/")
	if folder == "" || folder == rootFolder {
		return []string{rootFolder}
	}
	parts := strings.Split(folder, "/")
	out := make([]string, 0, len(parts)+1)
	for i := len(parts); i > 0; i-- {
		out = append(out, strings.Join(parts[:i], "/"))
	}
	out = append(out, rootFolder)
	return out
}

// FolderSecretsResolved walks `folder` up through each parent to the "root"
// catch-all and returns key=value with deepest-wins precedence.
func (s *Store) FolderSecretsResolved(folder string) (map[string]string, error) {
	if s.secretCipher == nil {
		return nil, ErrSecretCipherNotConfigured
	}
	paths := folderAncestors(folder)
	if len(paths) == 0 {
		return map[string]string{}, nil
	}
	args := make([]any, 0, len(paths)+1)
	args = append(args, string(ScopeFolder))
	for _, p := range paths {
		args = append(args, p)
	}
	ph := "(" + strings.TrimSuffix(strings.Repeat("?,", len(paths)), ",") + ")"
	rows, err := s.db.Query(
		`SELECT scope_id, key, enc_value FROM secrets
		 WHERE scope_kind = ? AND scope_id IN `+ph,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// paths is deep→shallow; depth = (len-1) - i. Deepest value wins per key.
	depthOf := make(map[string]int, len(paths))
	for i, p := range paths {
		depthOf[p] = len(paths) - 1 - i
	}
	type kv struct {
		val   string
		depth int
	}
	best := map[string]kv{}
	for rows.Next() {
		var scopeID, key string
		var blob []byte
		if err := rows.Scan(&scopeID, &key, &blob); err != nil {
			return nil, err
		}
		pt, err := s.decryptSecret(blob)
		if err != nil {
			return nil, err
		}
		d := depthOf[scopeID]
		if cur, ok := best[key]; !ok || d > cur.depth {
			best[key] = kv{val: pt, depth: d}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(best))
	for k, v := range best {
		out[k] = v.val
	}
	return out, nil
}

func (s *Store) UserSecrets(userSub string) (map[string]string, error) {
	if userSub == "" {
		return nil, errors.New("user_sub required")
	}
	if s.secretCipher == nil {
		return nil, ErrSecretCipherNotConfigured
	}
	rows, err := s.db.Query(
		`SELECT key, enc_value FROM secrets
		 WHERE scope_kind = ? AND scope_id = ?`,
		string(ScopeUser), userSub,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var key string
		var blob []byte
		if err := rows.Scan(&key, &blob); err != nil {
			return nil, err
		}
		pt, err := s.decryptSecret(blob)
		if err != nil {
			return nil, err
		}
		out[key] = pt
	}
	return out, rows.Err()
}

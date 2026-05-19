package store

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const encPrefix = "v1:"

func encryptValue(key *[32]byte, plaintext string) (string, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ct := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return encPrefix + base64.StdEncoding.EncodeToString(ct), nil
}

func decryptValue(key *[32]byte, encoded string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(encoded, encPrefix))
	if err != nil {
		return "", fmt.Errorf("decrypt base64: %w", err)
	}
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	ns := gcm.NonceSize()
	if len(data) < ns {
		return "", errors.New("ciphertext too short")
	}
	plain, err := gcm.Open(nil, data[:ns], data[ns:], nil)
	if err != nil {
		return "", fmt.Errorf("decrypt gcm: %w", err)
	}
	return string(plain), nil
}

// SecretScope is the kind of scope a secret is bound to.
type SecretScope string

const (
	ScopeFolder SecretScope = "folder"
	ScopeUser   SecretScope = "user"

	rootFolder = "root"
)

var ErrSecretNotFound = errors.New("secret not found")

type Secret struct {
	ScopeKind SecretScope
	ScopeID   string
	Key       string
	Value     string
	CreatedAt time.Time
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

func (s *Store) decode(value string) (string, error) {
	if s.secretKey == nil {
		return value, nil
	}
	if !strings.HasPrefix(value, encPrefix) {
		return "", ErrSecretNotFound
	}
	return decryptValue(s.secretKey, value)
}

func (s *Store) encode(value string) (string, error) {
	if s.secretKey == nil {
		return value, nil
	}
	return encryptValue(s.secretKey, value)
}

func (s *Store) SetSecret(scope SecretScope, scopeID, key, value string) error {
	if err := validateScope(scope, scopeID, key); err != nil {
		return err
	}
	stored, err := s.encode(value)
	if err != nil {
		return fmt.Errorf("encrypt secret: %w", err)
	}
	_, err = s.db.Exec(
		`INSERT INTO secrets (scope_kind, scope_id, key, value, created_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(scope_kind, scope_id, key) DO UPDATE SET
		   value = excluded.value,
		   created_at = excluded.created_at`,
		string(scope), scopeID, key, stored, time.Now().UTC().Format(time.RFC3339),
	)
	return err
}

func (s *Store) GetSecret(scope SecretScope, scopeID, key string) (Secret, error) {
	if err := validateScope(scope, scopeID, key); err != nil {
		return Secret{}, err
	}
	var value, createdAt string
	err := s.db.QueryRow(
		`SELECT value, created_at FROM secrets
		 WHERE scope_kind = ? AND scope_id = ? AND key = ?`,
		string(scope), scopeID, key,
	).Scan(&value, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Secret{}, ErrSecretNotFound
	}
	if err != nil {
		return Secret{}, err
	}
	plain, err := s.decode(value)
	if errors.Is(err, ErrSecretNotFound) {
		return Secret{}, ErrSecretNotFound
	}
	if err != nil {
		return Secret{}, fmt.Errorf("decrypt: %w", err)
	}
	t, _ := time.Parse(time.RFC3339, createdAt)
	return Secret{ScopeKind: scope, ScopeID: scopeID, Key: key, Value: plain, CreatedAt: t}, nil
}

func (s *Store) ListSecrets(scope SecretScope, scopeID string) ([]Secret, error) {
	if scope != ScopeFolder && scope != ScopeUser {
		return nil, fmt.Errorf("invalid scope_kind: %q", scope)
	}
	if scopeID == "" {
		return nil, errors.New("scope_id required")
	}
	rows, err := s.db.Query(
		`SELECT key, value, created_at FROM secrets
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
		var key, value, createdAt string
		if err := rows.Scan(&key, &value, &createdAt); err != nil {
			return nil, err
		}
		plain, err := s.decode(value)
		if errors.Is(err, ErrSecretNotFound) {
			continue // plaintext row after key set — skip (purged on next startup)
		}
		if err != nil {
			return nil, fmt.Errorf("decrypt %s: %w", key, err)
		}
		t, _ := time.Parse(time.RFC3339, createdAt)
		out = append(out, Secret{ScopeKind: scope, ScopeID: scopeID, Key: key, Value: plain, CreatedAt: t})
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

// PurgeUnencryptedSecrets deletes all plaintext secret rows (those without the
// "v1:" prefix). Called on startup when a key is set — operators re-enter secrets.
func (s *Store) PurgeUnencryptedSecrets(ctx context.Context) error {
	if s.secretKey == nil {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM secrets WHERE value NOT LIKE 'v1:%'`)
	return err
}

// folderAncestors returns folder paths deepest first, ending with "root".
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

// FolderSecretsResolved walks folder up through parents with deepest-wins precedence.
func (s *Store) FolderSecretsResolved(folder string) (map[string]string, error) {
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
		`SELECT scope_id, key, value FROM secrets WHERE scope_kind = ? AND scope_id IN `+ph,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
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
		var scopeID, key, value string
		if err := rows.Scan(&scopeID, &key, &value); err != nil {
			return nil, err
		}
		plain, err := s.decode(value)
		if errors.Is(err, ErrSecretNotFound) {
			continue // stale plaintext row — skip
		}
		if err != nil {
			return nil, fmt.Errorf("decrypt %s: %w", key, err)
		}
		d := depthOf[scopeID]
		if cur, ok := best[key]; !ok || d > cur.depth {
			best[key] = kv{val: plain, depth: d}
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

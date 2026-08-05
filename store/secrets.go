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
	"strings"
	"time"

	"github.com/kronael/arizuko/audit"
)

// secretV2Prefix marks an AES-256-GCM-encrypted secret value (spec 6/Y
// "Encryption at rest"). Stored form: "v2:" + base64(nonce||ciphertext).
// A value without this prefix is legacy plaintext (v1), read as-is.
const secretV2Prefix = "v2:"

// seal encrypts plaintext under the active key (secretKeys[0]). Only valid when
// the keyring is non-empty.
func (s *Store) seal(plaintext string) (string, error) {
	gcm, err := s.gcm(s.secretKeys[0])
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return secretV2Prefix + base64.StdEncoding.EncodeToString(ct), nil
}

// open decrypts a "v2:" value, trying each keyring key until one authenticates
// (so a value sealed under a now-retired key still reads during rotation).
// Legacy plaintext (no prefix) is returned as-is.
func (s *Store) open(stored string) (string, error) {
	if len(s.secretKeys) == 0 || !strings.HasPrefix(stored, secretV2Prefix) {
		return stored, nil
	}
	raw, err := base64.StdEncoding.DecodeString(stored[len(secretV2Prefix):])
	if err != nil {
		return "", err
	}
	for _, key := range s.secretKeys {
		gcm, err := s.gcm(key)
		if err != nil {
			return "", err
		}
		if len(raw) < gcm.NonceSize() {
			return "", errors.New("secret ciphertext too short")
		}
		if pt, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil); err == nil {
			return string(pt), nil
		}
	}
	return "", errors.New("secret decrypt failed: no configured key authenticates this value")
}

func (s *Store) gcm(key [32]byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// storeValue returns the at-rest form of plaintext: encrypted when the keyring
// is configured, plaintext otherwise (CLI/test paths without a key).
func (s *Store) storeValue(plaintext string) (string, error) {
	if len(s.secretKeys) == 0 {
		return plaintext, nil
	}
	return s.seal(plaintext)
}

// SecretScope is the kind of scope a secret is bound to: a folder path glob
// or an auth user sub. See specs/8/Y-secret-broker.md.
//
// v1: plaintext storage. Operator trusts disk + FS permissions.
// Encryption at rest deferred.
type SecretScope string

const (
	ScopeFolder SecretScope = "folder"
	ScopeUser   SecretScope = "user"

	// Folder path "root" is the catch-all parent walked to last by
	// FolderSecretsResolved. Concrete folders override it.
	rootFolder = "root"
)

var ErrSecretNotFound = errors.New("secret not found")

// EnvProfileKeys are the AI model credential names that belong to users only,
// never folders. The operator platform fallback lives in host .env via
// container/runner.go:readSecrets(). Folder-scope rows for these keys are
// rejected at the store layer so the distinction is enforced uniformly.
var EnvProfileKeys = map[string]struct{}{
	"ANTHROPIC_API_KEY":       {},
	"CLAUDE_CODE_OAUTH_TOKEN": {},
	"OPENAI_API_KEY":          {},
	"CODEX_API_KEY":           {},
}

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
	if scope == ScopeFolder {
		if _, ok := EnvProfileKeys[key]; ok {
			return fmt.Errorf("%s: env-profile key must be scope_kind=user (set via /dash/me/env)", key)
		}
	}
	return nil
}

func (s *Store) SetSecret(scope SecretScope, scopeID, key, value string) error {
	if err := validateScope(scope, scopeID, key); err != nil {
		return err
	}
	stored, err := s.storeValue(value)
	if err != nil {
		return err
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO secrets (scope_kind, scope_id, key, value, created_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(scope_kind, scope_id, key) DO UPDATE SET
		   value = excluded.value,
		   created_at = excluded.created_at`,
		string(scope), scopeID, key, stored, time.Now().UTC().Format(time.RFC3339),
	); err != nil {
		return err
	}
	folder := ""
	if scope == ScopeFolder {
		folder = scopeID
	}
	// The row names the secret and whether it sealed. It MUST NOT carry the value:
	// an audit trail an operator reads is not a place to put the thing the trail
	// exists to protect.
	actor, actorSub, surface := s.auditIdentity()
	if err := audit.EmitInTx(ctx, tx, audit.Event{
		Category: audit.CategorySecret,
		Action:   "secret.set",
		Actor:    actor,
		ActorSub: actorSub,
		Surface:  surface,
		Resource: fmt.Sprintf("secrets/%s/%s/%s", scope, scopeID, key),
		Scope:    string(scope),
		Folder:   folder,
		Outcome:  audit.OutcomeOK,
		ParamsSummary: map[string]any{
			"encrypted": len(s.secretKeys) > 0,
		},
	}); err != nil {
		return err
	}
	return tx.Commit()
}

// PutSecretRow seals and upserts one secret WITHOUT emitting an audit_log row —
// the audit-free twin of SetSecret used by the FS-mounted daemons that write
// secrets into routd.db directly (dashd's per-user env/secret pages, the CLI).
// The at-rest encoding is identical to SetSecret (storeValue → "v2:" seal when a
// keyring is set), so reads through FolderSecretsResolved decrypt the same way.
// routd itself uses the audited SetSecret.
func (s *Store) PutSecretRow(scope SecretScope, scopeID, key, value string) error {
	if err := validateScope(scope, scopeID, key); err != nil {
		return err
	}
	stored, err := s.storeValue(value)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO secrets (scope_kind, scope_id, key, value, created_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(scope_kind, scope_id, key) DO UPDATE SET
		   value = excluded.value,
		   created_at = excluded.created_at`,
		string(scope), scopeID, key, stored, time.Now().UTC().Format(time.RFC3339))
	return err
}

// DeleteSecretRow removes one secret WITHOUT emitting an audit_log row — the
// audit-free twin of DeleteSecret used by the FS-mounted daemons that delete
// secrets from routd.db directly (dashd, the CLI). Returns ErrSecretNotFound when
// no row matched so the HTTP layer can 404. routd itself uses DeleteSecret.
func (s *Store) DeleteSecretRow(scope SecretScope, scopeID, key string) error {
	if err := validateScope(scope, scopeID, key); err != nil {
		return err
	}
	res, err := s.db.Exec(
		`DELETE FROM secrets WHERE scope_kind = ? AND scope_id = ? AND key = ?`,
		string(scope), scopeID, key)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrSecretNotFound
	}
	return nil
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
	value, err = s.open(value)
	if err != nil {
		return Secret{}, err
	}
	t, _ := time.Parse(time.RFC3339, createdAt)
	return Secret{ScopeKind: scope, ScopeID: scopeID, Key: key, Value: value, CreatedAt: t}, nil
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
		value, err = s.open(value)
		if err != nil {
			return nil, err
		}
		t, _ := time.Parse(time.RFC3339, createdAt)
		out = append(out, Secret{ScopeKind: scope, ScopeID: scopeID, Key: key, Value: value, CreatedAt: t})
	}
	return out, rows.Err()
}

func (s *Store) DeleteSecret(scope SecretScope, scopeID, key string) error {
	if err := validateScope(scope, scopeID, key); err != nil {
		return err
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx,
		`DELETE FROM secrets WHERE scope_kind = ? AND scope_id = ? AND key = ?`,
		string(scope), scopeID, key,
	)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrSecretNotFound
	}
	folder := ""
	if scope == ScopeFolder {
		folder = scopeID
	}
	actor, actorSub, surface := s.auditIdentity()
	if err := audit.EmitInTx(ctx, tx, audit.Event{
		Category: audit.CategorySecret,
		Action:   "secret.delete",
		Actor:    actor,
		ActorSub: actorSub,
		Surface:  surface,
		Resource: fmt.Sprintf("secrets/%s/%s/%s", scope, scopeID, key),
		Scope:    string(scope),
		Folder:   folder,
		Outcome:  audit.OutcomeOK,
	}); err != nil {
		return err
	}
	return tx.Commit()
}

// EncryptPlaintextSecrets encrypts every legacy-plaintext row in place under
// the configured key (spec 6/Y "Encryption at rest"). Idempotent: rows already
// "v2:" are skipped. No-op without a key. Never deletes — it replaces the old
// PurgeUnencryptedSecrets, which DELETEd plaintext rows and so wiped every
// secret on each startup (writes never wrote the prefix it kept).
func (s *Store) EncryptPlaintextSecrets(ctx context.Context) error {
	if len(s.secretKeys) == 0 {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT rowid, value FROM secrets WHERE value NOT LIKE 'v2:%'`)
	if err != nil {
		return err
	}
	type todo struct {
		rowid int64
		value string
	}
	var pending []todo
	for rows.Next() {
		var t todo
		if err := rows.Scan(&t.rowid, &t.value); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, t := range pending {
		sealed, err := s.seal(t.value)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE secrets SET value = ? WHERE rowid = ?`, sealed, t.rowid); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ArchiveSecretRow is one row of the archive's routd.secrets.yaml document
// (spec 5/8 "Secret and token values"): SecretsRow's shape
// (resreg/resources/secrets.go) plus Value — the encrypted-at-rest blob the
// config manifest deliberately omits. Value travels exactly as stored
// ("v2:"+base64(nonce||ciphertext), or legacy plaintext) — never decrypted
// for transport.
type ArchiveSecretRow struct {
	ScopeKind string `yaml:"scope_kind"`
	ScopeID   string `yaml:"scope_id"`
	Key       string `yaml:"key"`
	Value     string `yaml:"value"`
	CreatedAt string `yaml:"created_at,omitempty"`
}

// ExportSecretRows reads every secrets row, blob included, for the archive's
// routd.secrets.yaml document. Unlike ListSecrets/GetSecret it does NOT
// decrypt — the blob travels as-is (spec 5/8: "encrypted secret blobs travel
// as-is. SECRETS_KEY never enters the archive").
func (s *Store) ExportSecretRows(ctx context.Context) ([]ArchiveSecretRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT scope_kind, scope_id, key, value, created_at FROM secrets ORDER BY scope_kind, scope_id, key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ArchiveSecretRow
	for rows.Next() {
		var r ArchiveSecretRow
		if err := rows.Scan(&r.ScopeKind, &r.ScopeID, &r.Key, &r.Value, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ValidateAndImportSecrets validates EVERY row's blob against the configured
// keyring BEFORE writing any of them, then UPSERTs by (scope_kind, scope_id,
// key) — never DELETE+INSERT, matching secrets' existing SkipApplyRebuild
// discipline (spec 5/8 "Secret and token values": "One DB can hold blobs
// sealed under different retired keys... a single probe row proves nothing
// about the rest"). Refuses the WHOLE batch on the first validation failure,
// naming the failing (scope_kind, scope_id, key) without echoing the value —
// no row is written when any row fails. Uses the existing package-private
// open() (the keyring probe SetSecret/GetSecret already rely on); no new
// decrypt surface is exported — the plaintext this returns internally is
// discarded immediately, never returned to the caller.
func (s *Store) ValidateAndImportSecrets(ctx context.Context, rows []ArchiveSecretRow) (int, error) {
	for _, r := range rows {
		if _, err := s.open(r.Value); err != nil {
			return 0, fmt.Errorf("secret %s/%s/%s: %w", r.ScopeKind, r.ScopeID, r.Key, err)
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	for _, r := range rows {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO secrets (scope_kind, scope_id, key, value, created_at)
			 VALUES (?, ?, ?, ?, ?)
			 ON CONFLICT(scope_kind, scope_id, key) DO UPDATE SET
			   value = excluded.value,
			   created_at = excluded.created_at`,
			r.ScopeKind, r.ScopeID, r.Key, r.Value, r.CreatedAt,
		); err != nil {
			return 0, fmt.Errorf("secret %s/%s/%s: %w", r.ScopeKind, r.ScopeID, r.Key, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(rows), nil
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
	paths := folderAncestors(folder)
	if len(paths) == 0 {
		return map[string]string{}, nil
	}
	args := make([]any, 0, len(paths)+1)
	args = append(args, string(ScopeFolder))
	for _, p := range paths {
		args = append(args, p)
	}
	rows, err := s.db.Query(
		`SELECT scope_id, key, value FROM secrets
		 WHERE scope_kind = ? AND scope_id IN (`+sqlPH(len(paths))+`)`,
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
		var scopeID, key, value string
		if err := rows.Scan(&scopeID, &key, &value); err != nil {
			return nil, err
		}
		d := depthOf[scopeID]
		if cur, ok := best[key]; !ok || d > cur.depth {
			plain, err := s.open(value)
			if err != nil {
				return nil, err
			}
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

// FolderSecretsResolvedForUser resolves the folder-ancestry secret set for
// `folder` (FolderSecretsResolved) then overlays the caller's user-scoped
// secrets: a user's own key WINS over the same folder key. This is the BYOA
// override — a web-chat user supplies ANTHROPIC_API_KEY for themselves and it
// shadows the folder default at spawn. Empty userSub returns the folder set
// unchanged (a system/timed trigger has no user to override with).
func (s *Store) FolderSecretsResolvedForUser(folder, userSub string) (map[string]string, error) {
	out, err := s.FolderSecretsResolved(folder)
	if err != nil {
		return nil, err
	}
	if userSub == "" {
		return out, nil
	}
	rows, err := s.db.Query(
		`SELECT key, value FROM secrets WHERE scope_kind = ? AND scope_id = ?`,
		string(ScopeUser), userSub)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		plain, err := s.open(value)
		if err != nil {
			return nil, err
		}
		out[key] = plain
	}
	return out, rows.Err()
}

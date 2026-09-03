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
	"regexp"
	"strings"
	"time"

	"github.com/kronael/arizuko/audit"
)

// secretV2Prefix marks an AES-256-GCM-encrypted secret value (spec 5/14
// "Encryption at rest"). Stored form: "v2:" + base64(nonce||ciphertext).
// A value without this prefix is a legacy plaintext row (v1) that a build
// before BUGS S2 wrote. The read path still opens it. No write path creates
// one.
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

// ErrNoSecretsKey reports that the caller tried to write a secret before an
// operator configured SECRETS_KEY.
var ErrNoSecretsKey = errors.New("SECRETS_KEY is not configured: refusing to write a secret in plaintext")

// storeValue returns the at-rest form of plaintext. Every write path in this
// package calls it, so it is the one place that enforces encryption at rest.
// An empty keyring returns ErrNoSecretsKey. It MUST NOT return the plaintext:
// that fallback made an un-keyed daemon persist every secret readable on disk,
// and each healthcheck still reported the daemon healthy (BUGS S2).
func (s *Store) storeValue(plaintext string) (string, error) {
	if len(s.secretKeys) == 0 {
		return "", ErrNoSecretsKey
	}
	return s.seal(plaintext)
}

// SecretScope is the kind of scope a secret is bound to: a folder path glob
// or an auth user sub. See specs/5/14-credentials.md.
type SecretScope string

const (
	ScopeFolder SecretScope = "folder"
	ScopeUser   SecretScope = "user"

	// ScopeMissing is AUDIT-ONLY: it never names a stored row (SetSecret still
	// admits only folder/user), it is the third member of the `secret_use_log`
	// scope_kind vocabulary — the tool asked for a key that resolved to nothing
	// (spec 5/13 § Audit: scope_kind ∈ {user, folder, missing}).
	ScopeMissing SecretScope = "missing"

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
	"ANTHROPIC_API_KEY": {},
	// Two names, two readers, one credential. claude reads
	// CLAUDE_CODE_OAUTH_TOKEN; pi reads ANTHROPIC_OAUTH_TOKEN and prefers it
	// over the API key. A name absent from this map never reaches a container,
	// so pi had no credential at all on an instance that holds one.
	"CLAUDE_CODE_OAUTH_TOKEN": {},
	"ANTHROPIC_OAUTH_TOKEN":   {},
	"OPENAI_API_KEY":          {},
	"CODEX_API_KEY":           {},
}

var secretKeyRE = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// ValidSecretKey accepts uppercase ENV-style identifiers only — the one rule
// the CLI and dashd's /dash/me pages both apply to a secret's name.
func ValidSecretKey(key string) bool { return secretKeyRE.MatchString(key) }

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

// CountPlaintextSecrets counts the legacy rows EncryptPlaintextSecrets would
// seal. It backs `arizuko secret seal-legacy --dry-run`, so the operator sees
// the blast radius BEFORE rewriting the column every credential lives in.
func (s *Store) CountPlaintextSecrets(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM secrets WHERE value NOT LIKE 'v2:%'`).Scan(&n)
	return n, err
}

// EncryptPlaintextSecrets encrypts every legacy-plaintext row in place under the
// configured key (spec 5/14) and returns how many it sealed. Idempotent. Never
// deletes — it replaces PurgeUnencryptedSecrets, which DELETEd plaintext rows and
// so wiped every secret on each startup.
//
// Refuses without a key rather than reporting a silent success: its whole job is to
// seal. It ran nowhere at all until 2026-08-15 (BUGS L16) — a caller that cannot
// tell "sealed nothing, there was nothing to seal" from "sealed nothing, there was
// no key" is how a migration goes unnoticed for months.
func (s *Store) EncryptPlaintextSecrets(ctx context.Context) (int, error) {
	if len(s.secretKeys) == 0 {
		return 0, ErrNoSecretsKey
	}
	rows, err := s.db.QueryContext(ctx, `SELECT rowid, value FROM secrets WHERE value NOT LIKE 'v2:%'`)
	if err != nil {
		return 0, err
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
			return 0, err
		}
		pending = append(pending, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(pending) == 0 {
		return 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	for _, t := range pending {
		sealed, err := s.seal(t.value)
		if err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE secrets SET value = ? WHERE rowid = ?`, sealed, t.rowid); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(pending), nil
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
// keyring BEFORE writing any of them, then UPSERTs by (scope_kind, scope_id, key)
// — never DELETE+INSERT, matching secrets' SkipApplyRebuild discipline: one DB can
// hold blobs sealed under different retired keys, so a single probe row proves
// nothing about the rest. Refuses the WHOLE batch on the first failure, naming the
// failing key without echoing the value. The plaintext open() yields is discarded
// immediately.
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

// FolderSecretsResolvedForUser resolves the folder-ancestry secret set then
// overlays the caller's user-scoped secrets: a user's own key WINS. This is the
// BYOA override. Empty userSub returns the folder set unchanged.
//
// The second return names the SCOPE each key came from. This is the ONLY point
// where user-vs-folder provenance exists — the overlay collapses both into one map
// — so secret_use_log gets it from here rather than from a second lookup that
// could disagree (spec 5/13 § Audit).
func (s *Store) FolderSecretsResolvedForUser(folder, userSub string) (map[string]string, map[string]SecretScope, error) {
	out, err := s.FolderSecretsResolved(folder)
	if err != nil {
		return nil, nil, err
	}
	scopes := make(map[string]SecretScope, len(out))
	for k := range out {
		scopes[k] = ScopeFolder
	}
	if userSub == "" {
		return out, scopes, nil
	}
	rows, err := s.db.Query(
		`SELECT key, value FROM secrets WHERE scope_kind = ? AND scope_id = ?`,
		string(ScopeUser), userSub)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, nil, err
		}
		plain, err := s.open(value)
		if err != nil {
			return nil, nil, err
		}
		out[key] = plain
		scopes[key] = ScopeUser
	}
	return out, scopes, rows.Err()
}

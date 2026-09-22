package store

// secrets.go holds every read and write of `secrets`: one item per sealed
// value, addressed by (scope_kind, scope_id, key).
//
// The partition is OwnerRoutd because resreg's entity path writes these items
// too (resreg.SubsystemRoutd IS store.OwnerRoutd); a second spelling selects
// nothing, silently. The scope leads the coordinate, so one scope's secrets are
// a begins_with; `key` is the last field, so that read IS `ORDER BY key`.
//
// `value` and the four surrogate-OAuth attributes are written only here and
// are absent from the resource's RowType (routd/resource_columns_test.go
// rowTypeGaps): a manifest that carried them would carry a live credential.
// Every upsert is a MERGE, because a Put would erase the OAuth state beside the
// blob. `created_at` is core.TS at 30 bytes, the width resreg's entity path
// stamps (BUGS L97).

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// The attributes a secret item carries. The three coordinate fields are NOT
// among them: storing the coordinate twice is two facts that can disagree. The
// last four are the surrogate-OAuth half (spec 5/15), written only by
// secrets_oauth.go; a pasted PAT carries none of them.
const (
	attrSecValue      = "value"
	attrSecCreatedAt  = "created_at"
	attrSecProvider   = "provider"
	attrSecRefreshVal = "refresh_val"
	attrSecExpiresAt  = "expires_at"
	attrSecScopeList  = "scope_list"
)

// secretKey is the coordinate of one secret. A value carrying the separator is
// REFUSED: two secrets would address one item. resreg/entity.go sortKeyFor
// makes the same refusal over the same three fields.
func secretKey(scope SecretScope, scopeID, key string) (objstore.Key, error) {
	parts := []string{string(scope), scopeID, key}
	for i, v := range parts {
		if strings.Contains(v, KeySep) {
			return objstore.Key{}, fmt.Errorf("secret carries %s=%q, and that byte separates "+
				"the three fields of its coordinate (scope_kind,scope_id,key)",
				[]string{"scope_kind", "scope_id", "key"}[i], v)
		}
	}
	return objstore.Key{
		Partition: OwnerRoutd,
		Sort:      strings.Join(parts, KeySep),
	}, nil
}

// secretScopePrefix is the leading fragment every one-scope read selects on.
// The trailing separator keeps folder `a` from reaching folder `ab`.
func secretScopePrefix(scope SecretScope, scopeID string) string {
	return string(scope) + KeySep + scopeID + KeySep
}

// secretKeyOf reads the `key` back out of one item's coordinate. A coordinate
// that does not split into three is a LOUD fault: a resolution reading it would
// hand a container the wrong credential.
func secretKeyOf(it objstore.Item) (string, error) {
	parts := strings.Split(it.Key.Sort, KeySep)
	if len(parts) != 3 {
		return "", fmt.Errorf("secret item %q splits into %d parts and its coordinate "+
			"(scope_kind,scope_id,key) has 3", it.Key.Sort, len(parts))
	}
	return parts[2], nil
}

// secretsIn reads ONE scope's secrets, in key order.
func (s *Store) secretsIn(ctx context.Context, scope SecretScope, scopeID string) ([]objstore.Item, error) {
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: Secrets, Partition: OwnerRoutd,
		Key: objstore.KeyPredicate{Op: objstore.PrefixKey, Value: secretScopePrefix(scope, scopeID)},
	})
	if err != nil {
		return nil, fmt.Errorf("read secrets of %s/%s: %w", scope, scopeID, err)
	}
	return items, nil
}

// secretUpsert renders the Merge a metadata upsert is (the file header says
// why not a Put).
func secretUpsert(scope SecretScope, scopeID, key string,
	owned map[string]string) (objstore.Write, error) {
	k, err := secretKey(scope, scopeID, key)
	if err != nil {
		return objstore.Write{}, err
	}
	return objstore.Write{Op: objstore.Merge, Entity: Secrets, Key: k, Attrs: owned}, nil
}

// putSecret writes one upsert.
func (s *Store) putSecret(ctx context.Context, scope SecretScope, scopeID, key string,
	owned map[string]string) error {
	w, err := secretUpsert(scope, scopeID, key, owned)
	if err != nil {
		return err
	}
	return s.WriteEntities(ctx, nil, w)
}

// sealedAttrs is what a pasted secret owns: the blob and the instant it landed.
func (s *Store) sealedAttrs(value string) (map[string]string, error) {
	stored, err := s.storeValue(value)
	if err != nil {
		return nil, err
	}
	return map[string]string{attrSecValue: stored, attrSecCreatedAt: core.NowTS()}, nil
}

// secretDelete renders the write that removes one secret. The ItemPresent
// condition is what turns a missing item into ErrSecretNotFound.
func secretDelete(scope SecretScope, scopeID, key string) (objstore.Write, error) {
	k, err := secretKey(scope, scopeID, key)
	if err != nil {
		return objstore.Write{}, err
	}
	return objstore.Write{
		Op: objstore.Delete, Entity: Secrets, Key: k,
		Cond: objstore.Cond{Item: objstore.ItemPresent},
	}, nil
}

// deleteSecret removes one secret and reports ErrSecretNotFound when there was
// none, with `also` riding the same batch.
func (s *Store) deleteSecret(ctx context.Context, scope SecretScope, scopeID, key string,
	also ...objstore.Write) error {
	w, err := secretDelete(scope, scopeID, key)
	if err != nil {
		return err
	}
	gone, err := s.TryWriteEntities(ctx, nil, append([]objstore.Write{w}, also...)...)
	if err != nil {
		return err
	}
	if !gone {
		return ErrSecretNotFound
	}
	return nil
}

// secretV2Prefix marks an AES-256-GCM value: "v2:" + base64(nonce||ciphertext)
// (spec 5/14). A value without it is legacy plaintext from before BUGS S2: the
// read path opens it, no write path creates one.
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

// open decrypts a "v2:" value, trying each keyring key so a value sealed under
// a retired key still reads. Legacy plaintext is returned as-is.
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

// storeValue returns the at-rest form of plaintext; every write path calls it.
// It MUST NOT return the plaintext on an empty keyring: that fallback persisted
// every secret readable on disk while healthchecks stayed green (BUGS S2).
func (s *Store) storeValue(plaintext string) (string, error) {
	if len(s.secretKeys) == 0 {
		return "", ErrNoSecretsKey
	}
	return s.seal(plaintext)
}

// SecretScope is the kind of scope a secret is bound to: a folder path glob
// or an auth user sub (specs/5/14-credentials.md).
type SecretScope string

// SecretScopeFolder is the `scope_kind` of a folder-scoped secret, as a plain
// string because folder_scope.go holds a key fragment, not a typed scope.
const SecretScopeFolder = "folder"

const (
	ScopeFolder SecretScope = SecretScopeFolder
	ScopeUser   SecretScope = "user"

	// ScopeMissing is AUDIT-ONLY: it never names a stored row; it is the third
	// `secret_use_log` scope_kind (spec 5/13 § Audit).
	ScopeMissing SecretScope = "missing"

	// rootFolder is the catch-all parent FolderSecretsResolved walks to last.
	rootFolder = "root"
)

var ErrSecretNotFound = errors.New("secret not found")

// EnvProfileKeys are the AI model credential names that belong to users only,
// never folders; folder rows are rejected here so the rule is enforced once.
var EnvProfileKeys = map[string]struct{}{
	"ANTHROPIC_API_KEY": {},
	// claude reads CLAUDE_CODE_OAUTH_TOKEN; pi reads ANTHROPIC_OAUTH_TOKEN. A
	// name absent from this map never reaches a container.
	"CLAUDE_CODE_OAUTH_TOKEN": {},
	"ANTHROPIC_OAUTH_TOKEN":   {},
	"OPENAI_API_KEY":          {},
	"CODEX_API_KEY":           {},
}

var secretKeyRE = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// ValidSecretKey accepts uppercase ENV-style identifiers only, the one rule the
// CLI and dashd's /dash/me pages both apply.
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

// SetSecret seals one secret and records the act, in ONE batch (spec 6/40).
func (s *Store) SetSecret(scope SecretScope, scopeID, key, value string) error {
	if err := validateScope(scope, scopeID, key); err != nil {
		return err
	}
	owned, err := s.sealedAttrs(value)
	if err != nil {
		return err
	}
	w, err := secretUpsert(scope, scopeID, key, owned)
	if err != nil {
		return err
	}
	folder := ""
	if scope == ScopeFolder {
		folder = scopeID
	}
	// The row MUST NOT carry the value.
	actor, actorSub, surface := s.auditIdentity()
	_, rec := audit.WriteFor(audit.Event{
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
	})
	return s.WriteEntities(context.Background(), nil, w, rec)
}

// PutSecretRow is the audit-free twin of SetSecret, for the FS-mounted writers
// (dashd, the CLI). Same at-rest encoding.
func (s *Store) PutSecretRow(scope SecretScope, scopeID, key, value string) error {
	if err := validateScope(scope, scopeID, key); err != nil {
		return err
	}
	owned, err := s.sealedAttrs(value)
	if err != nil {
		return err
	}
	return s.putSecret(context.Background(), scope, scopeID, key, owned)
}

// DeleteSecretRow is the audit-free twin of DeleteSecret. ErrSecretNotFound
// lets the HTTP layer 404.
func (s *Store) DeleteSecretRow(scope SecretScope, scopeID, key string) error {
	if err := validateScope(scope, scopeID, key); err != nil {
		return err
	}
	return s.deleteSecret(context.Background(), scope, scopeID, key)
}

func (s *Store) GetSecret(scope SecretScope, scopeID, key string) (Secret, error) {
	if err := validateScope(scope, scopeID, key); err != nil {
		return Secret{}, err
	}
	k, err := secretKey(scope, scopeID, key)
	if err != nil {
		return Secret{}, err
	}
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: Secrets, Partition: OwnerRoutd,
		Key: objstore.KeyPredicate{Op: objstore.ExactKey, Value: k.Sort},
	})
	if err != nil {
		return Secret{}, fmt.Errorf("read secret %s/%s/%s: %w", scope, scopeID, key, err)
	}
	if len(items) == 0 {
		return Secret{}, ErrSecretNotFound
	}
	value, err := s.open(items[0].Attrs[attrSecValue])
	if err != nil {
		return Secret{}, err
	}
	t, _ := time.Parse(core.TSLayout, items[0].Attrs[attrSecCreatedAt])
	return Secret{ScopeKind: scope, ScopeID: scopeID, Key: key, Value: value, CreatedAt: t}, nil
}

// ListSecrets returns one scope's secrets, decrypted, in key order.
func (s *Store) ListSecrets(scope SecretScope, scopeID string) ([]Secret, error) {
	if scope != ScopeFolder && scope != ScopeUser {
		return nil, fmt.Errorf("invalid scope_kind: %q", scope)
	}
	if scopeID == "" {
		return nil, errors.New("scope_id required")
	}
	items, err := s.secretsIn(context.Background(), scope, scopeID)
	if err != nil {
		return nil, err
	}
	var out []Secret
	for _, it := range items {
		key, err := secretKeyOf(it)
		if err != nil {
			return nil, err
		}
		value, err := s.open(it.Attrs[attrSecValue])
		if err != nil {
			return nil, err
		}
		t, _ := time.Parse(core.TSLayout, it.Attrs[attrSecCreatedAt])
		out = append(out, Secret{ScopeKind: scope, ScopeID: scopeID, Key: key, Value: value, CreatedAt: t})
	}
	return out, nil
}

// SecretKeyRow names one stored secret WITHOUT its value.
type SecretKeyRow struct {
	Key       string
	CreatedAt string
}

// SecretKeysIn lists the key names one scope holds, in key order. `value` is
// not in the projection, so the blob is never read or decrypted: authority the
// pages asking this question have no use for.
func (s *Store) SecretKeysIn(scope SecretScope, scopeID string) ([]SecretKeyRow, error) {
	if scope != ScopeFolder && scope != ScopeUser {
		return nil, fmt.Errorf("invalid scope_kind: %q", scope)
	}
	if scopeID == "" {
		return nil, errors.New("scope_id required")
	}
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: Secrets, Partition: OwnerRoutd,
		Key:     objstore.KeyPredicate{Op: objstore.PrefixKey, Value: secretScopePrefix(scope, scopeID)},
		Project: []string{attrSecCreatedAt},
	})
	if err != nil {
		return nil, fmt.Errorf("read the secret keys of %s/%s: %w", scope, scopeID, err)
	}
	out := make([]SecretKeyRow, 0, len(items))
	for _, it := range items {
		key, err := secretKeyOf(it)
		if err != nil {
			return nil, err
		}
		out = append(out, SecretKeyRow{Key: key, CreatedAt: it.Attrs[attrSecCreatedAt]})
	}
	return out, nil
}

// DeleteSecret removes one secret and records the act, in ONE batch.
func (s *Store) DeleteSecret(scope SecretScope, scopeID, key string) error {
	if err := validateScope(scope, scopeID, key); err != nil {
		return err
	}
	folder := ""
	if scope == ScopeFolder {
		folder = scopeID
	}
	actor, actorSub, surface := s.auditIdentity()
	_, rec := audit.WriteFor(audit.Event{
		Category: audit.CategorySecret,
		Action:   "secret.delete",
		Actor:    actor,
		ActorSub: actorSub,
		Surface:  surface,
		Resource: fmt.Sprintf("secrets/%s/%s/%s", scope, scopeID, key),
		Scope:    string(scope),
		Folder:   folder,
		Outcome:  audit.OutcomeOK,
	})
	return s.deleteSecret(context.Background(), scope, scopeID, key, rec)
}

// plaintextSecrets reads every item whose blob is NOT sealed: a partition read
// and a filter, because no index answers a prefix test on attribute text.
func (s *Store) plaintextSecrets(ctx context.Context) ([]objstore.Item, error) {
	items, err := s.Obj().Query(ctx, objstore.Query{Entity: Secrets, Partition: OwnerRoutd})
	if err != nil {
		return nil, fmt.Errorf("read secrets: %w", err)
	}
	out := make([]objstore.Item, 0, len(items))
	for _, it := range items {
		if !strings.HasPrefix(it.Attrs[attrSecValue], secretV2Prefix) {
			out = append(out, it)
		}
	}
	return out, nil
}

// CountPlaintextSecrets counts the legacy rows EncryptPlaintextSecrets would
// seal; it backs `arizuko secret seal-legacy --dry-run`.
func (s *Store) CountPlaintextSecrets(ctx context.Context) (int, error) {
	items, err := s.plaintextSecrets(ctx)
	if err != nil {
		return 0, err
	}
	return len(items), nil
}

// EncryptPlaintextSecrets seals every legacy-plaintext row in place (spec 5/14)
// and returns how many it sealed. Idempotent, chunked, never deletes. It
// refuses without a key rather than reporting zero: a silent zero hid that it
// ran nowhere until 2026-08-15 (BUGS L16).
func (s *Store) EncryptPlaintextSecrets(ctx context.Context) (int, error) {
	if len(s.secretKeys) == 0 {
		return 0, ErrNoSecretsKey
	}
	pending, err := s.plaintextSecrets(ctx)
	if err != nil {
		return 0, err
	}
	writes := make([]objstore.Write, 0, len(pending))
	for _, it := range pending {
		sealed, err := s.seal(it.Attrs[attrSecValue])
		if err != nil {
			return 0, err
		}
		writes = append(writes, objstore.Write{
			Op: objstore.Update, Entity: Secrets, Key: it.Key,
			Attrs: map[string]string{attrSecValue: sealed},
		})
	}
	if len(writes) == 0 {
		return 0, nil
	}
	return s.applyInChunks(ctx, "seal legacy secrets", writes)
}

// ArchiveSecretRow is one row of the archive's routd.secrets.yaml document
// (spec 5/8): SecretsRow's shape plus Value, which travels exactly as stored,
// never decrypted.
type ArchiveSecretRow struct {
	ScopeKind string `yaml:"scope_kind"`
	ScopeID   string `yaml:"scope_id"`
	Key       string `yaml:"key"`
	Value     string `yaml:"value"`
	CreatedAt string `yaml:"created_at,omitempty"`
}

// ExportSecretRows reads every secrets row, blob included, for the archive. It
// does NOT decrypt (spec 5/8: SECRETS_KEY never enters the archive). The
// coordinate order IS `ORDER BY scope_kind, scope_id, key`.
func (s *Store) ExportSecretRows(ctx context.Context) ([]ArchiveSecretRow, error) {
	items, err := s.Obj().Query(ctx, objstore.Query{Entity: Secrets, Partition: OwnerRoutd})
	if err != nil {
		return nil, fmt.Errorf("read secrets: %w", err)
	}
	out := make([]ArchiveSecretRow, 0, len(items))
	for _, it := range items {
		parts := strings.Split(it.Key.Sort, KeySep)
		if len(parts) != 3 {
			return nil, fmt.Errorf("secret item %q splits into %d parts and its coordinate "+
				"(scope_kind,scope_id,key) has 3", it.Key.Sort, len(parts))
		}
		out = append(out, ArchiveSecretRow{
			ScopeKind: parts[0],
			ScopeID:   parts[1],
			Key:       parts[2],
			Value:     it.Attrs[attrSecValue],
			CreatedAt: it.Attrs[attrSecCreatedAt],
		})
	}
	return out, nil
}

// ValidateAndImportSecrets validates EVERY row's blob against the keyring
// BEFORE writing any, then upserts: never delete+insert, because one DB can
// hold blobs sealed under different retired keys (SkipApplyRebuild). The first
// failure refuses the whole batch, naming the key and not the value. The
// archive's own created_at is kept.
func (s *Store) ValidateAndImportSecrets(ctx context.Context, rows []ArchiveSecretRow) (int, error) {
	for _, r := range rows {
		if _, err := s.open(r.Value); err != nil {
			return 0, fmt.Errorf("secret %s/%s/%s: %w", r.ScopeKind, r.ScopeID, r.Key, err)
		}
	}
	for _, r := range rows {
		owned := map[string]string{attrSecValue: r.Value}
		setAttr(owned, attrSecCreatedAt, r.CreatedAt)
		if err := s.putSecret(ctx, SecretScope(r.ScopeKind), r.ScopeID, r.Key, owned); err != nil {
			return 0, fmt.Errorf("secret %s/%s/%s: %w", r.ScopeKind, r.ScopeID, r.Key, err)
		}
	}
	return len(rows), nil
}

// folderAncestors returns folder paths for resolution, deepest first, ending
// with "root".
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

// FolderSecretsResolved walks `folder` up to the "root" catch-all and returns
// key=value, deepest wins. One read per ancestor: a whole-partition read would
// examine every user's credentials to answer about one folder.
func (s *Store) FolderSecretsResolved(folder string) (map[string]string, error) {
	ctx := context.Background()
	paths := folderAncestors(folder)
	out := map[string]string{}
	// paths is deep→shallow, so the FIRST answer for a key is the deepest.
	for _, p := range paths {
		items, err := s.secretsIn(ctx, ScopeFolder, p)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			key, err := secretKeyOf(it)
			if err != nil {
				return nil, err
			}
			if _, seen := out[key]; seen {
				continue
			}
			plain, err := s.open(it.Attrs[attrSecValue])
			if err != nil {
				return nil, err
			}
			out[key] = plain
		}
	}
	return out, nil
}

// FolderSecretsResolvedForUser overlays the caller's user-scoped secrets on
// FolderSecretsResolved: a user's own key WINS (the BYOA override). Empty
// userSub returns the folder set unchanged. The second return names the scope
// each key came from; it is the ONLY point where that provenance exists, so
// secret_use_log takes it from here (spec 5/13 § Audit).
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
	items, err := s.secretsIn(context.Background(), ScopeUser, userSub)
	if err != nil {
		return nil, nil, err
	}
	for _, it := range items {
		key, err := secretKeyOf(it)
		if err != nil {
			return nil, nil, err
		}
		plain, err := s.open(it.Attrs[attrSecValue])
		if err != nil {
			return nil, nil, err
		}
		out[key] = plain
		scopes[key] = ScopeUser
	}
	return out, scopes, nil
}

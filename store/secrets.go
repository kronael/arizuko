package store

// secrets.go holds every read and write of `secrets`: one item per sealed
// value, addressed by (scope_kind, scope_id, key).
//
// THE PARTITION IS OwnerRoutd AND IT IS NOT A CHOICE THIS FILE MAKES. `secrets`
// is a registered resreg resource, and an entity-backed resource's items live at
// its owner subsystem (resreg.SubsystemRoutd, which IS store.OwnerRoutd). A
// second spelling would put this file's items in one partition and the engine's
// read in another, and the failure is silent: a query on a partition nobody
// wrote to is a legal query that selects nothing.
//
// THE SCOPE LEADS THE COORDINATE, which is what makes every resolution a key
// predicate. `scope_kind + KeySep + scope_id + KeySep` is a leading fragment, so
// reading one folder's or one user's secrets is a begins_with that touches
// nothing else — exact, because no value of the three may carry the separator.
// Ordering by that read IS `ORDER BY key`, because key is the last field.
//
// THIS FILE IS THE ONLY WRITER OF THE SEALED HALF. `value` holds the blob,
// `provider`, `refresh_val`, `expires_at` and `scope_list` the surrogate-OAuth
// state, and the resource's RowType deliberately names none of the five
// (routd/resource_columns_test.go rowTypeGaps): a manifest that carried them
// would carry a live credential into the export an operator reads. That is also
// why every upsert here is a MERGE — a Put replaces the item's whole attribute
// set, so one unconditional Put of the metadata would erase the OAuth state
// beside it, and the conformance body AMergeKeepsWhatTheWriterDidNotName is
// what says which credential went.
//
// `created_at` IS core.TS, at 30 bytes. That is the width resreg's own entity
// path stamps a StampedField at (resreg/entity.go itemFor), and the two writers
// of these items have to agree byte for byte — an apply that stamped 30 where
// this file wrote 20 would put two widths in one attribute, and a comparison
// over TEXT answers by byte order (BUGS L97).

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
// among them: they are the key, and storing the coordinate twice is two facts
// that can disagree.
//
// The last four belong to the surrogate-OAuth half (spec 5/15) and only
// secrets_oauth.go writes them; a pasted PAT carries none of them, which is what
// tells the two apart.
const (
	attrSecValue      = "value"
	attrSecCreatedAt  = "created_at"
	attrSecProvider   = "provider"
	attrSecRefreshVal = "refresh_val"
	attrSecExpiresAt  = "expires_at"
	attrSecScopeList  = "scope_list"
)

// secretKey is the coordinate of one secret.
//
// A value carrying the separator is REFUSED rather than joined: two different
// secrets would address one item, the last write would win, and the read that
// splits the key back apart would name a scope neither of them holds. That is
// the same refusal resreg's own entity path makes over the same three fields
// (resreg/entity.go sortKeyFor), because both of them write these items.
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
// The trailing separator is what keeps folder `a` from reaching folder `ab`.
func secretScopePrefix(scope SecretScope, scopeID string) string {
	return string(scope) + KeySep + scopeID + KeySep
}

// secretKeyOf reads the `key` back out of one item's coordinate. A coordinate
// that does not split into three parts is a LOUD fault: half a coordinate names
// a secret nobody wrote, and a resolution reading it would hand a container the
// wrong credential.
func secretKeyOf(it objstore.Item) (string, error) {
	parts := strings.Split(it.Key.Sort, KeySep)
	if len(parts) != 3 {
		return "", fmt.Errorf("secret item %q splits into %d parts and its coordinate "+
			"(scope_kind,scope_id,key) has 3", it.Key.Sort, len(parts))
	}
	return parts[2], nil
}

// secretsIn reads ONE scope's secrets, in key order — which IS the coordinate's
// order, because `key` is its last field.
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

// secretUpsert renders the ONE write a metadata upsert is: merge the fields the
// caller owns into the item, creating it when the secret is new. See the file
// header on why a single Put would be data loss.
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

// secretDelete renders the write that removes one secret. The condition is what
// counts the batch: a delete of a missing item is otherwise idempotent and says
// nothing, and `RowsAffected() == 0` is the ErrSecretNotFound the HTTP layer 404s
// on.
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

// SecretScopeFolder is the `scope_kind` value of a folder-scoped secret,
// spelled as the plain text the coordinate carries. It is declared as a string
// rather than a SecretScope because the readers that need it hold a key
// fragment and not a typed scope: folder_scope.go splits an item's coordinate
// to decide whether a group delete takes the secret with it.
const SecretScopeFolder = "folder"

const (
	ScopeFolder SecretScope = SecretScopeFolder
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

// SetSecret seals one secret and records the act, in ONE batch.
//
// The mutation and its audit row commit together or neither does, which is what
// the transaction here gave: no write without its record, no record without its
// write. The batch gives it without one, because the upsert is a single Merge
// and a batch may name each item once (spec 6/40 §"The audited write commits in
// a GuardedBatch").
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
	// The row names the secret and whether it sealed. It MUST NOT carry the value:
	// an audit trail an operator reads is not a place to put the thing the trail
	// exists to protect.
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
	owned, err := s.sealedAttrs(value)
	if err != nil {
		return err
	}
	return s.putSecret(context.Background(), scope, scopeID, key, owned)
}

// DeleteSecretRow removes one secret WITHOUT emitting an audit_log row — the
// audit-free twin of DeleteSecret used by the FS-mounted daemons that delete
// secrets from routd.db directly (dashd, the CLI). Returns ErrSecretNotFound when
// no row matched so the HTTP layer can 404. routd itself uses DeleteSecret.
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

// SecretKeyRow names one stored secret WITHOUT its value: the key, and the
// instant it landed as its writer stamped it (core.TS).
type SecretKeyRow struct {
	Key       string
	CreatedAt string
}

// SecretKeysIn lists the key names one scope holds, in key order, and the value
// NEVER LEAVES THE ENGINE: `value` is not in the projection, so the sealed blob
// is not read, not decrypted and not put on the wire. That is authority the
// pages asking this question have no use for — /dash/me mirrors GitHub SSH keys,
// names visible and secrets opaque — and projecting after the read would hand it
// to them anyway (objstore Query.Project).
//
// The order is the coordinate's, because `key` is the last of its three fields,
// which is what `ORDER BY key` gave.
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

// DeleteSecret removes one secret and records the act, in ONE batch — the twin
// of SetSecret, and atomic for the same reason.
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

// plaintextSecrets reads every item whose blob is NOT sealed. The prefix test is
// on the attribute's TEXT, so it reads the partition and compares — which is
// what `value NOT LIKE 'v2:%'` was: an unindexed predicate over one table.
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
// seal. It backs `arizuko secret seal-legacy --dry-run`, so the operator sees
// the blast radius BEFORE rewriting the attribute every credential lives in.
func (s *Store) CountPlaintextSecrets(ctx context.Context) (int, error) {
	items, err := s.plaintextSecrets(ctx)
	if err != nil {
		return 0, err
	}
	return len(items), nil
}

// EncryptPlaintextSecrets encrypts every legacy-plaintext row in place under the
// configured key (spec 5/14) and returns how many it sealed. Idempotent. Never
// deletes — its whole job is to seal.
//
// Refuses without a key rather than reporting a silent success. It ran nowhere
// at all until 2026-08-15 (BUGS L16) — a caller that cannot tell "sealed
// nothing, there was nothing to seal" from "sealed nothing, there was no key" is
// how a migration goes unnoticed for months.
//
// The sweep commits a CHUNK at a time rather than all at once, because a batch
// carries at most MaxBatchActions and one instance holds more secrets than that.
// A chunk that lands and a chunk that does not leave work a re-run finishes: a
// sealed value is already prefixed, so the next run does not see it.
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
//
// The partition read comes back in coordinate order, which IS
// `ORDER BY scope_kind, scope_id, key`.
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

// ValidateAndImportSecrets validates EVERY row's blob against the configured
// keyring BEFORE writing any of them, then UPSERTs by (scope_kind, scope_id, key)
// — never DELETE+INSERT, matching secrets' SkipApplyRebuild discipline: one DB can
// hold blobs sealed under different retired keys, so a single probe row proves
// nothing about the rest. Refuses the WHOLE batch on the first failure, naming the
// failing key without echoing the value. The plaintext open() yields is discarded
// immediately.
//
// The write carries the archive's own created_at rather than stamping a new one,
// because the instant the secret was set is part of what the archive restores.
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
//
// One read per ancestor rather than one read of the whole entity: a folder is at
// most a handful deep, each read is an exact begins_with on the coordinate, and
// a whole-partition read would examine every user's credentials to answer about
// one folder. That is the shape ResolveAllowlist already uses.
func (s *Store) FolderSecretsResolved(folder string) (map[string]string, error) {
	ctx := context.Background()
	paths := folderAncestors(folder)
	out := map[string]string{}
	// paths is deep→shallow, so the FIRST answer for a key is the deepest one
	// and every shallower row for it is skipped.
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

package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// openMemSealed opens an in-memory store that carries a keyring, like every
// daemon that writes secrets. Without one each write path refuses (BUGS S2).
// Tests that exercise the refusal itself open a bare store instead.
func openMemSealed(t *testing.T) *Store {
	t.Helper()
	s, err := OpenMem()
	if err != nil {
		t.Fatalf("OpenMem: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	s.SetSecretKeys([]byte("store-test-key"))
	return s
}

// rawSecretValue is what is AT REST: no reader hands the sealed blob back, so
// the encryption assertions read the item at the coordinate secrets.go declares.
func rawSecretValue(t *testing.T, s *Store, scope SecretScope, scopeID, key string) string {
	t.Helper()
	return secretItem(t, s, scope, scopeID, key).Attrs[attrSecValue]
}

func secretItem(t *testing.T, s *Store, scope SecretScope, scopeID, key string) objstore.Item {
	t.Helper()
	k, err := secretKey(scope, scopeID, key)
	if err != nil {
		t.Fatal(err)
	}
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: Secrets, Partition: OwnerRoutd,
		Key: objstore.KeyPredicate{Op: objstore.ExactKey, Value: k.Sort},
	})
	if err != nil {
		t.Fatalf("read secret %s/%s/%s: %v", scope, scopeID, key, err)
	}
	if len(items) != 1 {
		t.Fatalf("secret items at %s/%s/%s = %d, want 1", scope, scopeID, key, len(items))
	}
	return items[0]
}

// TestValidateAndImportSecrets_ValidatesAllBeforeWritingAny is Finding 5
// (spec 5/8 "Secret and token values"): a batch where one row's blob fails
// to decrypt under the configured keyring must write NOTHING — not even the
// rows before it in the batch — and must name the failing row without
// echoing its value.
func TestValidateAndImportSecrets_ValidatesAllBeforeWritingAny(t *testing.T) {
	s := openMemSealed(t)

	good, err := s.seal("plain-value")
	if err != nil {
		t.Fatal(err)
	}
	rows := []ArchiveSecretRow{
		{ScopeKind: "folder", ScopeID: "atlas", Key: "GOOD", Value: good, CreatedAt: "2026-01-01T00:00:00Z"},
		{ScopeKind: "folder", ScopeID: "atlas", Key: "BAD", Value: "v2:not-valid-base64-or-worse", CreatedAt: "2026-01-01T00:00:00Z"},
	}
	_, err = s.ValidateAndImportSecrets(context.Background(), rows)
	if err == nil {
		t.Fatal("expected an error for the malformed row")
	}
	if !strings.Contains(err.Error(), "folder/atlas/BAD") {
		t.Errorf("error must name the failing row, got: %v", err)
	}
	if strings.Contains(err.Error(), "plain-value") {
		t.Errorf("error must never echo a value, got: %v", err)
	}
	// Nothing written — including the row that validated fine.
	if _, err := s.GetSecret(ScopeFolder, "atlas", "GOOD"); !errors.Is(err, ErrSecretNotFound) {
		t.Errorf("GOOD row must not be written when BAD fails validation, got err=%v", err)
	}
}

// TestValidateAndImportSecrets_DifferentRetiredKeys is Finding 5's other
// half: "one DB can hold blobs sealed under different retired keys" — a
// batch where each row is sealed under a DIFFERENT key still validates
// (every key in the keyring is tried per row), and both rows import.
func TestValidateAndImportSecrets_DifferentRetiredKeys(t *testing.T) {
	s := openMemSealed(t)
	s.SetSecretKeys([]byte("key-old"))
	sealedOld, err := s.seal("value-old")
	if err != nil {
		t.Fatal(err)
	}
	s.SetSecretKeys([]byte("key-new"), []byte("key-old")) // rotate: new active, old retained
	sealedNew, err := s.seal("value-new")
	if err != nil {
		t.Fatal(err)
	}

	rows := []ArchiveSecretRow{
		{ScopeKind: "folder", ScopeID: "atlas", Key: "OLD", Value: sealedOld, CreatedAt: "2026-01-01T00:00:00Z"},
		{ScopeKind: "folder", ScopeID: "atlas", Key: "NEW", Value: sealedNew, CreatedAt: "2026-01-01T00:00:00Z"},
	}
	n, err := s.ValidateAndImportSecrets(context.Background(), rows)
	if err != nil {
		t.Fatalf("ValidateAndImportSecrets: %v", err)
	}
	if n != 2 {
		t.Errorf("imported = %d, want 2", n)
	}
	got, err := s.GetSecret(ScopeFolder, "atlas", "OLD")
	if err != nil || got.Value != "value-old" {
		t.Errorf("OLD = %+v, err=%v", got, err)
	}
}

// TestValidateAndImportSecrets_UpsertNotRebuild is the archive UPSERT
// discipline (spec 5/8 "Secret and token values"): importing must update an
// existing row by (scope_kind, scope_id, key), not require the table be
// empty, and must never touch a row the batch doesn't mention.
func TestValidateAndImportSecrets_UpsertNotRebuild(t *testing.T) {
	s := openMemSealed(t)

	if err := s.SetSecret(ScopeFolder, "atlas", "UNTOUCHED", "stays"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSecret(ScopeFolder, "atlas", "OVERWRITE", "old-value"); err != nil {
		t.Fatal(err)
	}
	newVal, err := s.seal("new-value")
	if err != nil {
		t.Fatal(err)
	}
	rows := []ArchiveSecretRow{
		{ScopeKind: "folder", ScopeID: "atlas", Key: "OVERWRITE", Value: newVal, CreatedAt: "2026-02-01T00:00:00Z"},
	}
	if _, err := s.ValidateAndImportSecrets(context.Background(), rows); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSecret(ScopeFolder, "atlas", "OVERWRITE")
	if err != nil || got.Value != "new-value" {
		t.Errorf("OVERWRITE = %+v, err=%v", got, err)
	}
	untouched, err := s.GetSecret(ScopeFolder, "atlas", "UNTOUCHED")
	if err != nil || untouched.Value != "stays" {
		t.Errorf("UNTOUCHED = %+v, err=%v", untouched, err)
	}
}

// TestExportSecretRows_TravelsAsIs confirms ExportSecretRows never decrypts —
// the stored ciphertext form travels verbatim (spec 5/8: "encrypted secret
// blobs travel as-is").
func TestExportSecretRows_TravelsAsIs(t *testing.T) {
	s := openMemSealed(t)
	if err := s.SetSecret(ScopeFolder, "atlas", "TOKEN", "sk-secret-xyz"); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ExportSecretRows(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if !strings.HasPrefix(rows[0].Value, "v2:") {
		t.Errorf("Value = %q, want v2: ciphertext", rows[0].Value)
	}
	if strings.Contains(rows[0].Value, "sk-secret-xyz") {
		t.Error("plaintext leaked into exported row")
	}
}

func TestSecret_EncryptionAtRest_RoundTrip(t *testing.T) {
	s := openMemSealed(t)

	if err := s.SetSecret(ScopeFolder, "atlas", "TOKEN", "sk-secret-xyz"); err != nil {
		t.Fatal(err)
	}
	raw := rawSecretValue(t, s, ScopeFolder, "atlas", "TOKEN")
	if !strings.HasPrefix(raw, "v2:") {
		t.Errorf("stored value not encrypted: %q", raw)
	}
	if strings.Contains(raw, "sk-secret-xyz") {
		t.Error("plaintext leaked into stored value")
	}
	got, err := s.GetSecret(ScopeFolder, "atlas", "TOKEN")
	if err != nil {
		t.Fatal(err)
	}
	if got.Value != "sk-secret-xyz" {
		t.Errorf("decrypted Value = %q, want plaintext", got.Value)
	}
}

// seedLegacyPlaintext writes an unencrypted row the way a build before BUGS S2
// wrote one. No write path can still produce it, so the migration and
// read-through tests must insert it directly.
func seedLegacyPlaintext(t *testing.T, s *Store, scope SecretScope, scopeID, key, value string) {
	t.Helper()
	k, err := secretKey(scope, scopeID, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.WriteEntities(context.Background(), nil, objstore.Write{
		Op: objstore.Put, Entity: Secrets, Key: k,
		Attrs: map[string]string{
			attrSecValue:     value,
			attrSecCreatedAt: core.NowTS(),
		},
	}); err != nil {
		t.Fatalf("seedLegacyPlaintext: %v", err)
	}
}

// BUGS S2: an empty keyring must REFUSE every write. It previously stored the
// plaintext, so an un-keyed daemon persisted each secret readable on disk.
func TestSecret_NoKey_RefusesToWrite(t *testing.T) {
	s := openMem(t)
	defer s.Close()

	writes := map[string]func() error{
		"SetSecret":    func() error { return s.SetSecret(ScopeUser, "u1", "K", "plainval") },
		"PutSecretRow": func() error { return s.PutSecretRow(ScopeUser, "u1", "K", "plainval") },
		"PutOAuthSecret": func() error {
			return s.PutOAuthSecret("u1", "K", "plainval", "github", "refresh-val", time.Time{}, "repo")
		},
	}
	for name, write := range writes {
		if err := write(); !errors.Is(err, ErrNoSecretsKey) {
			t.Errorf("%s with no keyring: err = %v, want ErrNoSecretsKey", name, err)
		}
	}

	// The refusal must also leave NOTHING behind — a partially-written row would
	// be the same disclosure the error exists to prevent.
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: Secrets, Partition: OwnerRoutd,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Errorf("refused writes still left %d secrets row(s)", len(items))
	}
}

// storeValue is the single choke point every write path funnels through, so it
// must never hand back the plaintext it was given.
func TestStoreValue_NeverReturnsPlaintext(t *testing.T) {
	s := openMem(t)
	defer s.Close()

	const plaintext = "sk-live-never-at-rest"
	if got, err := s.storeValue(plaintext); !errors.Is(err, ErrNoSecretsKey) {
		t.Errorf("no keyring: storeValue = %q, %v; want ErrNoSecretsKey", got, err)
	} else if got != "" {
		t.Errorf("no keyring: storeValue returned %q, want empty", got)
	}

	s.SetSecretKeys([]byte("k"))
	got, err := s.storeValue(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, secretV2Prefix) || strings.Contains(got, plaintext) {
		t.Errorf("keyed: storeValue = %q, want a v2: seal that hides the plaintext", got)
	}
}

func TestSecret_LegacyPlaintext_ReadsThrough(t *testing.T) {
	s := openMemSealed(t)
	seedLegacyPlaintext(t, s, ScopeFolder, "atlas", "OLD", "legacy-plain")
	s.SetSecretKeys([]byte("k")) // key configured AFTER the legacy row exists
	got, err := s.GetSecret(ScopeFolder, "atlas", "OLD")
	if err != nil {
		t.Fatal(err)
	}
	if got.Value != "legacy-plain" {
		t.Errorf("legacy read = %q, want legacy-plain", got.Value)
	}
}

func TestSecret_EncryptPlaintextMigrate_Idempotent(t *testing.T) {
	s := openMemSealed(t)
	seedLegacyPlaintext(t, s, ScopeFolder, "atlas", "A", "aaa")
	seedLegacyPlaintext(t, s, ScopeUser, "u1", "B", "bbb")
	s.SetSecretKeys([]byte("mig-key"))
	sealed, err := s.EncryptPlaintextSecrets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sealed != 2 {
		t.Errorf("sealed = %d, want 2", sealed)
	}
	rawB := rawSecretValue(t, s, ScopeUser, "u1", "B")
	if !strings.HasPrefix(rawSecretValue(t, s, ScopeFolder, "atlas", "A"), "v2:") || !strings.HasPrefix(rawB, "v2:") {
		t.Error("rows not migrated to v2:")
	}
	if got, _ := s.GetSecret(ScopeFolder, "atlas", "A"); got.Value != "aaa" {
		t.Errorf("A reads = %q", got.Value)
	}
	again, err := s.EncryptPlaintextSecrets(context.Background()) // idempotent
	if err != nil {
		t.Fatal(err)
	}
	if again != 0 {
		t.Errorf("second run sealed %d rows, want 0", again)
	}
	if rawSecretValue(t, s, ScopeUser, "u1", "B") != rawB {
		t.Error("migrate not idempotent: a v2: row was re-encrypted")
	}
	if got, _ := s.GetSecret(ScopeUser, "u1", "B"); got.Value != "bbb" {
		t.Errorf("B reads = %q after 2nd migrate", got.Value)
	}
}

func TestSecret_WrongKeyFails(t *testing.T) {
	s := openMemSealed(t)
	s.SetSecretKeys([]byte("key-A"))
	if err := s.SetSecret(ScopeFolder, "atlas", "K", "secret"); err != nil {
		t.Fatal(err)
	}
	s.SetSecretKeys([]byte("key-B")) // wrong key, no re-encrypt
	if _, err := s.GetSecret(ScopeFolder, "atlas", "K"); err == nil {
		t.Error("GetSecret under the wrong key should fail to decrypt")
	}
}

// Rotation: a value sealed under the old key still reads once the keyring is
// {new, old}; new writes seal under the active (first) key; dropping the
// retired key makes the old-sealed value unreadable.
func TestSecret_KeyringRotation(t *testing.T) {
	s := openMemSealed(t)
	s.SetSecretKeys([]byte("key-old"))
	if err := s.SetSecret(ScopeFolder, "atlas", "K", "sealed-under-old"); err != nil {
		t.Fatal(err)
	}
	s.SetSecretKeys([]byte("key-new"), []byte("key-old")) // rotate: new active, old retained
	if got, err := s.GetSecret(ScopeFolder, "atlas", "K"); err != nil || got.Value != "sealed-under-old" {
		t.Fatalf("rotated read = %q, %v; want sealed-under-old via retired key", got.Value, err)
	}
	if err := s.SetSecret(ScopeFolder, "atlas", "K2", "sealed-under-new"); err != nil {
		t.Fatal(err)
	}
	s.SetSecretKeys([]byte("key-new")) // drop the retired key
	if got, _ := s.GetSecret(ScopeFolder, "atlas", "K2"); got.Value != "sealed-under-new" {
		t.Errorf("K2 under new-only = %q, want sealed-under-new", got.Value)
	}
	if _, err := s.GetSecret(ScopeFolder, "atlas", "K"); err == nil {
		t.Error("old-sealed value should be unreadable once the retired key is dropped")
	}
}

// FolderSecretsResolved must return DECRYPTED plaintext, not the v2: ciphertext.
func TestFolderSecretsResolved_Decrypts(t *testing.T) {
	s := openMemSealed(t)
	if err := s.SetSecret(ScopeFolder, "atlas/eng", "TOKEN", "sk-plain"); err != nil {
		t.Fatal(err)
	}
	if raw := rawSecretValue(t, s, ScopeFolder, "atlas/eng", "TOKEN"); !strings.HasPrefix(raw, "v2:") {
		t.Fatalf("not encrypted at rest: %q", raw)
	}
	got, err := s.FolderSecretsResolved("atlas/eng")
	if err != nil {
		t.Fatal(err)
	}
	if got["TOKEN"] != "sk-plain" {
		t.Errorf("resolved TOKEN = %q, want decrypted sk-plain (not ciphertext)", got["TOKEN"])
	}
}

func TestSetGetSecret_RoundTrip(t *testing.T) {
	s := openMemSealed(t)

	if err := s.SetSecret(ScopeFolder, "atlas/eng", "API_KEY", "sk-abc-123"); err != nil {
		t.Fatalf("SetSecret: %v", err)
	}
	got, err := s.GetSecret(ScopeFolder, "atlas/eng", "API_KEY")
	if err != nil {
		t.Fatalf("GetSecret: %v", err)
	}
	if got.Value != "sk-abc-123" {
		t.Errorf("Value = %q, want %q", got.Value, "sk-abc-123")
	}
	if got.ScopeKind != ScopeFolder || got.ScopeID != "atlas/eng" || got.Key != "API_KEY" {
		t.Errorf("scope mismatch: %+v", got)
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt should be set")
	}
}

func TestSetSecret_Overwrites(t *testing.T) {
	s := openMemSealed(t)

	if err := s.SetSecret(ScopeFolder, "atlas", "K", "v1"); err != nil {
		t.Fatalf("SetSecret v1: %v", err)
	}
	if err := s.SetSecret(ScopeFolder, "atlas", "K", "v2"); err != nil {
		t.Fatalf("SetSecret v2: %v", err)
	}
	got, err := s.GetSecret(ScopeFolder, "atlas", "K")
	if err != nil {
		t.Fatalf("GetSecret: %v", err)
	}
	if got.Value != "v2" {
		t.Errorf("Value = %q, want v2", got.Value)
	}

	keys, err := s.SecretKeysIn(ScopeFolder, "atlas")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, k := range keys {
		if k.Key == "K" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("rows = %d, want 1 (of %+v)", n, keys)
	}
}

func TestGetSecret_MissingFails(t *testing.T) {
	s := openMemSealed(t)

	_, err := s.GetSecret(ScopeFolder, "atlas", "MISSING")
	if !errors.Is(err, ErrSecretNotFound) {
		t.Errorf("want ErrSecretNotFound, got %v", err)
	}
}

func TestListSecrets_Scope(t *testing.T) {
	s := openMemSealed(t)

	if err := s.SetSecret(ScopeFolder, "atlas", "A", "1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSecret(ScopeFolder, "atlas", "B", "2"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSecret(ScopeFolder, "atlas/eng", "C", "3"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSecret(ScopeUser, "github:alice", "U", "9"); err != nil {
		t.Fatal(err)
	}

	atlas, err := s.ListSecrets(ScopeFolder, "atlas")
	if err != nil {
		t.Fatalf("ListSecrets atlas: %v", err)
	}
	if len(atlas) != 2 {
		t.Errorf("atlas list = %d, want 2", len(atlas))
	}
	for _, sec := range atlas {
		if sec.ScopeKind != ScopeFolder || sec.ScopeID != "atlas" {
			t.Errorf("unexpected row in atlas list: %+v", sec)
		}
	}

	eng, err := s.ListSecrets(ScopeFolder, "atlas/eng")
	if err != nil {
		t.Fatalf("ListSecrets atlas/eng: %v", err)
	}
	if len(eng) != 1 || eng[0].Key != "C" || eng[0].Value != "3" {
		t.Errorf("eng list = %+v, want one C=3", eng)
	}

	users, err := s.ListSecrets(ScopeUser, "github:alice")
	if err != nil {
		t.Fatalf("ListSecrets user: %v", err)
	}
	if len(users) != 1 || users[0].Value != "9" {
		t.Errorf("user list = %+v, want one U=9", users)
	}
}

func TestDeleteSecret(t *testing.T) {
	s := openMemSealed(t)

	if err := s.SetSecret(ScopeFolder, "atlas", "K", "v"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSecret(ScopeFolder, "atlas", "K"); err != nil {
		t.Fatalf("DeleteSecret: %v", err)
	}
	if _, err := s.GetSecret(ScopeFolder, "atlas", "K"); !errors.Is(err, ErrSecretNotFound) {
		t.Errorf("after delete: want ErrSecretNotFound, got %v", err)
	}
	// Missing key → ErrSecretNotFound (parity with DeleteSecretRow, so the HTTP
	// layer 404s instead of reporting a phantom success + audit row).
	if err := s.DeleteSecret(ScopeFolder, "atlas", "K"); !errors.Is(err, ErrSecretNotFound) {
		t.Errorf("DeleteSecret on missing: want ErrSecretNotFound, got %v", err)
	}
}

func TestFolderSecretsResolved_DeepestWins(t *testing.T) {
	s := openMemSealed(t)

	if err := s.SetSecret(ScopeFolder, "atlas", "KEY", "v1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSecret(ScopeFolder, "atlas/eng", "KEY", "v2"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSecret(ScopeFolder, "atlas", "ONLY_SHALLOW", "shallow"); err != nil {
		t.Fatal(err)
	}

	got, err := s.FolderSecretsResolved("atlas/eng")
	if err != nil {
		t.Fatalf("FolderSecretsResolved: %v", err)
	}
	if got["KEY"] != "v2" {
		t.Errorf("KEY = %q, want v2 (deepest wins)", got["KEY"])
	}
	if got["ONLY_SHALLOW"] != "shallow" {
		t.Errorf("ONLY_SHALLOW = %q, want shallow", got["ONLY_SHALLOW"])
	}
}

// FolderSecretsResolvedForUser overlays the caller's user-scoped secrets on the
// folder set: a user's own key WINS over the folder default; a folder-only key
// still resolves; an empty userSub returns the folder set unchanged.
func TestFolderSecretsResolvedForUser_UserOverridesFolder(t *testing.T) {
	s := openMemSealed(t)

	// GITHUB_TOKEN: capability credential — allowed at folder scope (shared team key).
	if err := s.SetSecret(ScopeFolder, "atlas", "GITHUB_TOKEN", "folder-gh"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSecret(ScopeFolder, "atlas", "FOLDER_ONLY", "shared"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSecret(ScopeUser, "github:alice", "GITHUB_TOKEN", "alice-gh"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSecret(ScopeUser, "github:alice", "USER_ONLY", "alice-extra"); err != nil {
		t.Fatal(err)
	}

	got, _, err := s.FolderSecretsResolvedForUser("atlas", "github:alice")
	if err != nil {
		t.Fatalf("FolderSecretsResolvedForUser: %v", err)
	}
	if got["GITHUB_TOKEN"] != "alice-gh" {
		t.Errorf("GITHUB_TOKEN = %q, want alice-gh (user wins)", got["GITHUB_TOKEN"])
	}
	if got["FOLDER_ONLY"] != "shared" {
		t.Errorf("FOLDER_ONLY = %q, want shared (folder key survives)", got["FOLDER_ONLY"])
	}
	if got["USER_ONLY"] != "alice-extra" {
		t.Errorf("USER_ONLY = %q, want alice-extra (user-only key present)", got["USER_ONLY"])
	}

	// A different user does NOT see alice's override → folder default stands.
	bob, _, err := s.FolderSecretsResolvedForUser("atlas", "github:bob")
	if err != nil {
		t.Fatal(err)
	}
	if bob["GITHUB_TOKEN"] != "folder-gh" {
		t.Errorf("bob GITHUB_TOKEN = %q, want folder-gh", bob["GITHUB_TOKEN"])
	}
	if _, ok := bob["USER_ONLY"]; ok {
		t.Error("bob must not see alice's USER_ONLY key")
	}

	// Empty userSub → the folder set unchanged.
	none, _, err := s.FolderSecretsResolvedForUser("atlas", "")
	if err != nil {
		t.Fatal(err)
	}
	if none["GITHUB_TOKEN"] != "folder-gh" || len(none) != 2 {
		t.Errorf("empty-user resolution = %v, want plain folder set", none)
	}
}

// TestEnvProfileKeyFolderScopeRejected verifies the store-layer enforcement:
// env-profile keys (ANTHROPIC_API_KEY etc) must not be written at folder scope.
func TestEnvProfileKeyFolderScopeRejected(t *testing.T) {
	s := openMemSealed(t)

	for _, k := range []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "OPENAI_API_KEY", "CODEX_API_KEY"} {
		if err := s.SetSecret(ScopeFolder, "atlas", k, "val"); err == nil {
			t.Errorf("SetSecret(ScopeFolder, %s): expected error, got nil", k)
		}
		if err := s.PutSecretRow(ScopeFolder, "atlas", k, "val"); err == nil {
			t.Errorf("PutSecretRow(ScopeFolder, %s): expected error, got nil", k)
		}
	}
	// Capability keys at folder scope are fine.
	if err := s.SetSecret(ScopeFolder, "atlas", "GITHUB_TOKEN", "ghp_val"); err != nil {
		t.Errorf("SetSecret(ScopeFolder, GITHUB_TOKEN): unexpected error: %v", err)
	}
	// Env-profile keys at user scope are fine.
	if err := s.SetSecret(ScopeUser, "github:alice", "ANTHROPIC_API_KEY", "sk-key"); err != nil {
		t.Errorf("SetSecret(ScopeUser, ANTHROPIC_API_KEY): unexpected error: %v", err)
	}
}

// User overrides must DECRYPT — a sealed user value reads back as plaintext,
// never the v2: ciphertext.
func TestFolderSecretsResolvedForUser_Decrypts(t *testing.T) {
	s := openMemSealed(t)
	if err := s.SetSecret(ScopeUser, "github:alice", "ANTHROPIC_API_KEY", "sk-alice"); err != nil {
		t.Fatal(err)
	}
	if raw := rawSecretValue(t, s, ScopeUser, "github:alice", "ANTHROPIC_API_KEY"); !strings.HasPrefix(raw, "v2:") {
		t.Fatalf("user secret not sealed: %q", raw)
	}
	got, _, err := s.FolderSecretsResolvedForUser("atlas", "github:alice")
	if err != nil {
		t.Fatal(err)
	}
	if got["ANTHROPIC_API_KEY"] != "sk-alice" {
		t.Errorf("resolved user key = %q, want decrypted sk-alice", got["ANTHROPIC_API_KEY"])
	}
}

func TestFolderSecretsResolved_RootFallback(t *testing.T) {
	s := openMemSealed(t)

	if err := s.SetSecret(ScopeFolder, "root", "KEY", "base"); err != nil {
		t.Fatal(err)
	}

	got, err := s.FolderSecretsResolved("atlas/eng")
	if err != nil {
		t.Fatalf("FolderSecretsResolved: %v", err)
	}
	if got["KEY"] != "base" {
		t.Errorf("KEY = %q, want base (root fallback)", got["KEY"])
	}

	// Also: root resolves to root.
	got, err = s.FolderSecretsResolved("root")
	if err != nil {
		t.Fatalf("FolderSecretsResolved(root): %v", err)
	}
	if got["KEY"] != "base" {
		t.Errorf("root resolution KEY = %q, want base", got["KEY"])
	}
}

func TestValidSecretKey(t *testing.T) {
	cases := map[string]bool{
		"GITHUB_TOKEN": true,
		"K":            true,
		"A1_B2":        true,
		"":             false,
		"lower":        false,
		"1LEAD":        false,
		"WITH-DASH":    false,
		"WITH SPACE":   false,
		"a_lower":      false,
	}
	for key, want := range cases {
		if got := ValidSecretKey(key); got != want {
			t.Errorf("ValidSecretKey(%q) = %v, want %v", key, got, want)
		}
	}
}

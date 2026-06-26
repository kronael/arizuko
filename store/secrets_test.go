package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func rawSecretValue(t *testing.T, s *Store, scope SecretScope, scopeID, key string) string {
	t.Helper()
	var v string
	if err := s.db.QueryRow(
		`SELECT value FROM secrets WHERE scope_kind=? AND scope_id=? AND key=?`,
		string(scope), scopeID, key,
	).Scan(&v); err != nil {
		t.Fatalf("rawSecretValue: %v", err)
	}
	return v
}

func TestSecret_EncryptionAtRest_RoundTrip(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()
	s.SetSecretKeys([]byte("test-secrets-key"))

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

func TestSecret_NoKey_PlaintextAtRest(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()
	if err := s.SetSecret(ScopeUser, "u1", "K", "plainval"); err != nil {
		t.Fatal(err)
	}
	if raw := rawSecretValue(t, s, ScopeUser, "u1", "K"); raw != "plainval" {
		t.Errorf("no-key stored = %q, want plaintext", raw)
	}
	if got, _ := s.GetSecret(ScopeUser, "u1", "K"); got.Value != "plainval" {
		t.Errorf("Value = %q", got.Value)
	}
}

func TestSecret_LegacyPlaintext_ReadsThrough(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()
	if err := s.SetSecret(ScopeFolder, "atlas", "OLD", "legacy-plain"); err != nil {
		t.Fatal(err)
	}
	s.SetSecretKeys([]byte("k")) // key configured AFTER the plaintext write
	got, err := s.GetSecret(ScopeFolder, "atlas", "OLD")
	if err != nil {
		t.Fatal(err)
	}
	if got.Value != "legacy-plain" {
		t.Errorf("legacy read = %q, want legacy-plain", got.Value)
	}
}

func TestSecret_EncryptPlaintextMigrate_Idempotent(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()
	if err := s.SetSecret(ScopeFolder, "atlas", "A", "aaa"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSecret(ScopeUser, "u1", "B", "bbb"); err != nil {
		t.Fatal(err)
	}
	s.SetSecretKeys([]byte("mig-key"))
	if err := s.EncryptPlaintextSecrets(context.Background()); err != nil {
		t.Fatal(err)
	}
	rawB := rawSecretValue(t, s, ScopeUser, "u1", "B")
	if !strings.HasPrefix(rawSecretValue(t, s, ScopeFolder, "atlas", "A"), "v2:") || !strings.HasPrefix(rawB, "v2:") {
		t.Error("rows not migrated to v2:")
	}
	if got, _ := s.GetSecret(ScopeFolder, "atlas", "A"); got.Value != "aaa" {
		t.Errorf("A reads = %q", got.Value)
	}
	if err := s.EncryptPlaintextSecrets(context.Background()); err != nil { // idempotent
		t.Fatal(err)
	}
	if rawSecretValue(t, s, ScopeUser, "u1", "B") != rawB {
		t.Error("migrate not idempotent: a v2: row was re-encrypted")
	}
	if got, _ := s.GetSecret(ScopeUser, "u1", "B"); got.Value != "bbb" {
		t.Errorf("B reads = %q after 2nd migrate", got.Value)
	}
}

func TestSecret_WrongKeyFails(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()
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
	s, _ := OpenMem()
	defer s.Close()
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
	s, _ := OpenMem()
	defer s.Close()
	s.SetSecretKeys([]byte("resolve-key"))
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
	s, _ := OpenMem()
	defer s.Close()

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
	s, _ := OpenMem()
	defer s.Close()

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

	// Exactly one row.
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM secrets WHERE scope_kind='folder' AND scope_id='atlas' AND key='K'`).Scan(&n)
	if n != 1 {
		t.Errorf("rows = %d, want 1", n)
	}
}

func TestGetSecret_MissingFails(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	_, err := s.GetSecret(ScopeFolder, "atlas", "MISSING")
	if !errors.Is(err, ErrSecretNotFound) {
		t.Errorf("want ErrSecretNotFound, got %v", err)
	}
}

func TestListSecrets_Scope(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

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
	s, _ := OpenMem()
	defer s.Close()

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
	s, _ := OpenMem()
	defer s.Close()

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
	s, _ := OpenMem()
	defer s.Close()

	if err := s.SetSecret(ScopeFolder, "atlas", "ANTHROPIC_API_KEY", "folder-key"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSecret(ScopeFolder, "atlas", "FOLDER_ONLY", "shared"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSecret(ScopeUser, "github:alice", "ANTHROPIC_API_KEY", "alice-key"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSecret(ScopeUser, "github:alice", "USER_ONLY", "alice-extra"); err != nil {
		t.Fatal(err)
	}

	got, err := s.FolderSecretsResolvedForUser("atlas", "github:alice")
	if err != nil {
		t.Fatalf("FolderSecretsResolvedForUser: %v", err)
	}
	if got["ANTHROPIC_API_KEY"] != "alice-key" {
		t.Errorf("ANTHROPIC_API_KEY = %q, want alice-key (user wins)", got["ANTHROPIC_API_KEY"])
	}
	if got["FOLDER_ONLY"] != "shared" {
		t.Errorf("FOLDER_ONLY = %q, want shared (folder key survives)", got["FOLDER_ONLY"])
	}
	if got["USER_ONLY"] != "alice-extra" {
		t.Errorf("USER_ONLY = %q, want alice-extra (user-only key present)", got["USER_ONLY"])
	}

	// A different user does NOT see alice's override → folder default stands.
	bob, err := s.FolderSecretsResolvedForUser("atlas", "github:bob")
	if err != nil {
		t.Fatal(err)
	}
	if bob["ANTHROPIC_API_KEY"] != "folder-key" {
		t.Errorf("bob ANTHROPIC_API_KEY = %q, want folder-key", bob["ANTHROPIC_API_KEY"])
	}
	if _, ok := bob["USER_ONLY"]; ok {
		t.Error("bob must not see alice's USER_ONLY key")
	}

	// Empty userSub → the folder set unchanged.
	none, err := s.FolderSecretsResolvedForUser("atlas", "")
	if err != nil {
		t.Fatal(err)
	}
	if none["ANTHROPIC_API_KEY"] != "folder-key" || len(none) != 2 {
		t.Errorf("empty-user resolution = %v, want plain folder set", none)
	}
}

// User overrides must DECRYPT — a sealed user value reads back as plaintext,
// never the v2: ciphertext.
func TestFolderSecretsResolvedForUser_Decrypts(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()
	s.SetSecretKeys([]byte("byoa-key"))
	if err := s.SetSecret(ScopeUser, "github:alice", "ANTHROPIC_API_KEY", "sk-alice"); err != nil {
		t.Fatal(err)
	}
	if raw := rawSecretValue(t, s, ScopeUser, "github:alice", "ANTHROPIC_API_KEY"); !strings.HasPrefix(raw, "v2:") {
		t.Fatalf("user secret not sealed: %q", raw)
	}
	got, err := s.FolderSecretsResolvedForUser("atlas", "github:alice")
	if err != nil {
		t.Fatal(err)
	}
	if got["ANTHROPIC_API_KEY"] != "sk-alice" {
		t.Errorf("resolved user key = %q, want decrypted sk-alice", got["ANTHROPIC_API_KEY"])
	}
}

func TestFolderSecretsResolved_RootFallback(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

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

// v1 stores plaintext per spec 7/Y. Verify the `value` column holds the
// raw string written, not a ciphertext.
func TestSecretPlaintextAtRest(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	const plaintext = "ghp_topsecret_token"
	if err := s.SetSecret(ScopeFolder, "atlas", "GITHUB_TOKEN", plaintext); err != nil {
		t.Fatalf("SetSecret: %v", err)
	}
	var got string
	if err := s.db.QueryRow(
		`SELECT value FROM secrets WHERE scope_kind='folder' AND scope_id='atlas' AND key='GITHUB_TOKEN'`,
	).Scan(&got); err != nil {
		t.Fatalf("scan value: %v", err)
	}
	if got != plaintext {
		t.Errorf("value at rest = %q, want %q (plaintext)", got, plaintext)
	}
}

package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

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
	// Idempotent.
	if err := s.DeleteSecret(ScopeFolder, "atlas", "K"); err != nil {
		t.Errorf("DeleteSecret on missing: %v", err)
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

func TestSetGetSecret_Encrypted(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()
	s.SetSecretKey([]byte("test-key"))

	const plaintext = "sk-abc-123"
	if err := s.SetSecret(ScopeFolder, "atlas", "API_KEY", plaintext); err != nil {
		t.Fatalf("SetSecret: %v", err)
	}
	// On-disk value must be ciphertext, not plaintext.
	var raw string
	s.db.QueryRow(
		`SELECT value FROM secrets WHERE scope_kind='folder' AND scope_id='atlas' AND key='API_KEY'`,
	).Scan(&raw)
	if !strings.HasPrefix(raw, "v1:") {
		t.Errorf("raw value = %q, want v1: prefix", raw)
	}
	if raw == plaintext {
		t.Error("raw value must not equal plaintext")
	}
	// GetSecret must decrypt transparently.
	got, err := s.GetSecret(ScopeFolder, "atlas", "API_KEY")
	if err != nil {
		t.Fatalf("GetSecret: %v", err)
	}
	if got.Value != plaintext {
		t.Errorf("Value = %q, want %q", got.Value, plaintext)
	}
}

func TestGetSecret_PlaintextMigrationCompat(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	// Insert a plaintext row directly (simulates pre-encryption row).
	const plaintext = "old-plain-value"
	s.db.Exec(
		`INSERT INTO secrets (scope_kind, scope_id, key, value, created_at) VALUES ('folder','atlas','OLD_KEY',?,?)`,
		plaintext, "2024-01-01T00:00:00Z",
	)

	// With key set, plaintext rows are returned as-is (migration compat).
	s.SetSecretKey([]byte("test-key"))
	got, err := s.GetSecret(ScopeFolder, "atlas", "OLD_KEY")
	if err != nil {
		t.Fatalf("GetSecret: %v", err)
	}
	if got.Value != plaintext {
		t.Errorf("Value = %q, want %q", got.Value, plaintext)
	}
}

func TestEncryptAllSecrets(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	// Seed two plaintext rows.
	s.db.Exec(
		`INSERT INTO secrets (scope_kind, scope_id, key, value, created_at) VALUES ('folder','atlas','K1','v1','2024-01-01T00:00:00Z')`)
	s.db.Exec(
		`INSERT INTO secrets (scope_kind, scope_id, key, value, created_at) VALUES ('folder','atlas','K2','v2','2024-01-01T00:00:00Z')`)

	s.SetSecretKey([]byte("test-key"))
	if err := s.EncryptAllSecrets(context.Background()); err != nil {
		t.Fatalf("EncryptAllSecrets: %v", err)
	}

	// Verify on-disk values are now ciphertext.
	rows, _ := s.db.Query(`SELECT key, value FROM secrets WHERE scope_kind='folder' AND scope_id='atlas'`)
	defer rows.Close()
	for rows.Next() {
		var k, v string
		rows.Scan(&k, &v)
		if !strings.HasPrefix(v, "v1:") {
			t.Errorf("key %s: raw value %q, want v1: prefix", k, v)
		}
	}

	// GetSecret still decrypts correctly.
	got, err := s.GetSecret(ScopeFolder, "atlas", "K1")
	if err != nil {
		t.Fatalf("GetSecret K1: %v", err)
	}
	if got.Value != "v1" {
		t.Errorf("K1 = %q, want v1", got.Value)
	}

	// Idempotent: calling again must not error.
	if err := s.EncryptAllSecrets(context.Background()); err != nil {
		t.Fatalf("EncryptAllSecrets idempotent: %v", err)
	}
}

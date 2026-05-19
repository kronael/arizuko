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

func TestGetSecret_PlaintextInvalidatedWithKey(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	// Insert a plaintext row directly (simulates pre-key row).
	s.db.Exec(
		`INSERT INTO secrets (scope_kind, scope_id, key, value, created_at) VALUES ('folder','atlas','OLD_KEY','old-plain','2024-01-01T00:00:00Z')`,
	)

	// With key set, plaintext row is not readable (returns ErrSecretNotFound).
	s.SetSecretKey([]byte("test-key"))
	_, err := s.GetSecret(ScopeFolder, "atlas", "OLD_KEY")
	if !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("expected ErrSecretNotFound for plaintext row, got %v", err)
	}
}

func TestPurgeUnencryptedSecrets(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	s.db.Exec(`INSERT INTO secrets (scope_kind, scope_id, key, value, created_at) VALUES ('folder','atlas','K1','plaintext','2024-01-01T00:00:00Z')`)
	s.db.Exec(`INSERT INTO secrets (scope_kind, scope_id, key, value, created_at) VALUES ('folder','atlas','K2','plaintext','2024-01-01T00:00:00Z')`)

	s.SetSecretKey([]byte("test-key"))
	// Add one encrypted row so we can verify it survives.
	if err := s.SetSecret(ScopeFolder, "atlas", "K3", "real-value"); err != nil {
		t.Fatalf("SetSecret: %v", err)
	}

	if err := s.PurgeUnencryptedSecrets(context.Background()); err != nil {
		t.Fatalf("PurgeUnencryptedSecrets: %v", err)
	}

	// K1 and K2 are gone.
	if _, err := s.GetSecret(ScopeFolder, "atlas", "K1"); !errors.Is(err, ErrSecretNotFound) {
		t.Error("K1 should be purged")
	}
	// K3 survives.
	got, err := s.GetSecret(ScopeFolder, "atlas", "K3")
	if err != nil {
		t.Fatalf("K3: %v", err)
	}
	if got.Value != "real-value" {
		t.Errorf("K3 = %q, want real-value", got.Value)
	}

	// Idempotent.
	if err := s.PurgeUnencryptedSecrets(context.Background()); err != nil {
		t.Fatalf("PurgeUnencryptedSecrets idempotent: %v", err)
	}
}

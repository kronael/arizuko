package store

import (
	"bytes"
	"errors"
	"testing"
)

const testAuthSecret = "test-auth-secret-do-not-use-in-prod"

func TestSetGetSecret_RoundTrip(t *testing.T) {
	s, _ := OpenMemWithSecret(testAuthSecret)
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
	s, _ := OpenMemWithSecret(testAuthSecret)
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
	s, _ := OpenMemWithSecret(testAuthSecret)
	defer s.Close()

	_, err := s.GetSecret(ScopeFolder, "atlas", "MISSING")
	if !errors.Is(err, ErrSecretNotFound) {
		t.Errorf("want ErrSecretNotFound, got %v", err)
	}
}

func TestListSecrets_Scope(t *testing.T) {
	s, _ := OpenMemWithSecret(testAuthSecret)
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
	s, _ := OpenMemWithSecret(testAuthSecret)
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
	s, _ := OpenMemWithSecret(testAuthSecret)
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
	s, _ := OpenMemWithSecret(testAuthSecret)
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

func TestUserSecrets_Scoped(t *testing.T) {
	s, _ := OpenMemWithSecret(testAuthSecret)
	defer s.Close()

	if err := s.SetSecret(ScopeUser, "github:A", "K", "A"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSecret(ScopeUser, "github:B", "K", "B"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSecret(ScopeFolder, "atlas", "K", "folder"); err != nil {
		t.Fatal(err)
	}

	a, err := s.UserSecrets("github:A")
	if err != nil {
		t.Fatalf("UserSecrets A: %v", err)
	}
	if a["K"] != "A" || len(a) != 1 {
		t.Errorf("UserSecrets(A) = %v, want {K:A}", a)
	}
	b, err := s.UserSecrets("github:B")
	if err != nil {
		t.Fatalf("UserSecrets B: %v", err)
	}
	if b["K"] != "B" || len(b) != 1 {
		t.Errorf("UserSecrets(B) = %v, want {K:B}", b)
	}
}

func TestSecretEncryption_BlobIsCiphertext(t *testing.T) {
	s, _ := OpenMemWithSecret(testAuthSecret)
	defer s.Close()

	plaintext := "plain"
	if err := s.SetSecret(ScopeFolder, "atlas", "K", plaintext); err != nil {
		t.Fatal(err)
	}
	var blob []byte
	if err := s.db.QueryRow(
		`SELECT enc_value FROM secrets WHERE scope_kind='folder' AND scope_id='atlas' AND key='K'`,
	).Scan(&blob); err != nil {
		t.Fatalf("scan blob: %v", err)
	}
	if bytes.Contains(blob, []byte(plaintext)) {
		t.Errorf("blob contains plaintext: %x", blob)
	}
	// First 12 bytes are the AES-GCM nonce; the cipher then yields
	// plaintext-len + tag-len (16 B) bytes. So total ≥ 12 + len(pt) + 16.
	const nonceSize = 12
	const tagSize = 16
	want := nonceSize + len(plaintext) + tagSize
	if len(blob) != want {
		t.Errorf("blob len = %d, want %d (nonce + ct + tag)", len(blob), want)
	}
}

func TestEncryption_DifferentNoncesPerSet(t *testing.T) {
	s, _ := OpenMemWithSecret(testAuthSecret)
	defer s.Close()

	// Two different keys, identical plaintext, must produce different ciphertexts.
	if err := s.SetSecret(ScopeFolder, "atlas", "K1", "same-plain"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSecret(ScopeFolder, "atlas", "K2", "same-plain"); err != nil {
		t.Fatal(err)
	}
	var b1, b2 []byte
	s.db.QueryRow(`SELECT enc_value FROM secrets WHERE key='K1'`).Scan(&b1)
	s.db.QueryRow(`SELECT enc_value FROM secrets WHERE key='K2'`).Scan(&b2)
	if bytes.Equal(b1, b2) {
		t.Error("identical plaintext produced identical ciphertext (nonce reused)")
	}
	// Nonces (first 12 bytes) must differ.
	if bytes.Equal(b1[:12], b2[:12]) {
		t.Error("nonces are identical across two SetSecret calls")
	}

	// And re-setting the same key must also rotate the nonce.
	if err := s.SetSecret(ScopeFolder, "atlas", "K1", "same-plain"); err != nil {
		t.Fatal(err)
	}
	var b1b []byte
	s.db.QueryRow(`SELECT enc_value FROM secrets WHERE key='K1'`).Scan(&b1b)
	if bytes.Equal(b1[:12], b1b[:12]) {
		t.Error("re-set with same plaintext reused nonce")
	}
}

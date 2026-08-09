package store

import (
	"database/sql"
	"strings"
	"testing"
	"time"
)

// oauthStore builds a Store on a fresh in-memory secrets table that carries the
// surrogate-OAuth columns routd migration 0017 adds (store's own migrations
// predate them; routd owns the live table).
func oauthStore(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", "file:oauth_"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE secrets (
		scope_kind TEXT NOT NULL, scope_id TEXT NOT NULL, key TEXT NOT NULL,
		value BLOB NOT NULL, created_at TEXT NOT NULL,
		provider TEXT, refresh_val BLOB, expires_at DATETIME, scope_list TEXT,
		PRIMARY KEY (scope_kind, scope_id, key));`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := New(db)
	s.SetSecretKeys([]byte("oauth-test-key"))
	return s
}

func TestPutOAuthSecret_RoundTripSealed(t *testing.T) {
	s := oauthStore(t)
	exp := time.Now().Add(time.Hour).UTC()
	if err := s.PutOAuthSecret("github:alice", "GITHUB_TOKEN", "acc-tok", "github", "ref-tok", exp, "repo,read:user"); err != nil {
		t.Fatal(err)
	}

	// value + refresh_val sealed at rest (v2:), never plaintext.
	var rawVal, rawRef string
	if err := s.db.QueryRow(
		`SELECT value, refresh_val FROM secrets WHERE scope_id='github:alice' AND key='GITHUB_TOKEN'`,
	).Scan(&rawVal, &rawRef); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(rawVal, "v2:") || strings.Contains(rawVal, "acc-tok") {
		t.Errorf("value not sealed: %q", rawVal)
	}
	if !strings.HasPrefix(rawRef, "v2:") || strings.Contains(rawRef, "ref-tok") {
		t.Errorf("refresh_val not sealed: %q", rawRef)
	}

	rows, err := s.UserOAuthSecrets("github:alice", []string{"GITHUB_TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	oa := rows[0]
	if oa.Value != "acc-tok" || oa.Refresh != "ref-tok" || oa.Provider != "github" || oa.Scope != "repo,read:user" {
		t.Errorf("decrypted row = %+v", oa)
	}
	if oa.ExpiresAt.IsZero() || oa.ExpiresAt.Sub(exp).Abs() > time.Second {
		t.Errorf("expires_at = %v, want ~%v", oa.ExpiresAt, exp)
	}

	// The BYOA read path (FolderSecretsResolvedForUser) sees the fresh value too.
	all, _, err := s.FolderSecretsResolvedForUser("main", "github:alice")
	if err != nil {
		t.Fatal(err)
	}
	if all["GITHUB_TOKEN"] != "acc-tok" {
		t.Errorf("FolderSecretsResolvedForUser[GITHUB_TOKEN] = %q, want acc-tok", all["GITHUB_TOKEN"])
	}
}

func TestUserOAuthSecrets_SkipsPAT(t *testing.T) {
	s := oauthStore(t)
	// A pasted PAT (no provider) at the same key kind must not surface as OAuth.
	if err := s.PutSecretRow(ScopeUser, "github:bob", "OTHER_TOKEN", "pat-value"); err != nil {
		t.Fatal(err)
	}
	rows, err := s.UserOAuthSecrets("github:bob", []string{"OTHER_TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("PAT row surfaced as OAuth: %+v", rows)
	}
}

func TestUpdateOAuthSecret_Reseals(t *testing.T) {
	s := oauthStore(t)
	if err := s.PutOAuthSecret("github:alice", "GITHUB_TOKEN", "v1", "github", "r1", time.Now().Add(30*time.Second), "repo"); err != nil {
		t.Fatal(err)
	}
	newExp := time.Now().Add(2 * time.Hour).UTC()
	if err := s.UpdateOAuthSecret("github:alice", "GITHUB_TOKEN", "v2", "r2", newExp, "repo"); err != nil {
		t.Fatal(err)
	}
	rows, _ := s.UserOAuthSecrets("github:alice", []string{"GITHUB_TOKEN"})
	if len(rows) != 1 || rows[0].Value != "v2" || rows[0].Refresh != "r2" {
		t.Fatalf("after update = %+v", rows)
	}
	if rows[0].ExpiresAt.Sub(newExp).Abs() > time.Second {
		t.Errorf("expires_at not updated: %v", rows[0].ExpiresAt)
	}

	// Updating an absent row is a not-found, not a silent insert.
	if err := s.UpdateOAuthSecret("github:alice", "MISSING", "x", "y", newExp, ""); err != ErrSecretNotFound {
		t.Errorf("update missing row = %v, want ErrSecretNotFound", err)
	}
}

func TestClearOAuthSecret_NullsExpiryAndRefresh(t *testing.T) {
	s := oauthStore(t)
	if err := s.PutOAuthSecret("github:alice", "GITHUB_TOKEN", "acc", "github", "ref", time.Now().Add(30*time.Second), "repo"); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearOAuthSecret("github:alice", "GITHUB_TOKEN"); err != nil {
		t.Fatal(err)
	}
	var exp, ref sql.NullString
	if err := s.db.QueryRow(
		`SELECT expires_at, refresh_val FROM secrets WHERE scope_id='github:alice' AND key='GITHUB_TOKEN'`,
	).Scan(&exp, &ref); err != nil {
		t.Fatal(err)
	}
	if exp.Valid || ref.Valid {
		t.Errorf("expires_at/refresh_val not nulled: exp=%v ref=%v", exp, ref)
	}
	// The row (value+provider) survives so the UI can show a reconnect prompt.
	conns, _ := s.ListUserConnections("github:alice")
	if len(conns) != 1 || conns[0].Provider != "github" {
		t.Errorf("connection row should survive clear: %+v", conns)
	}
}

package store

import (
	"strings"
	"testing"
	"time"
)

// oauthStore builds a keyed Store over the secrets entity. The surrogate-OAuth
// half is four attributes secrets_oauth.go writes, so nothing has to be added
// beside the entity every opener already creates.
func oauthStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenMem()
	if err != nil {
		t.Fatalf("OpenMem: %v", err)
	}
	t.Cleanup(func() { s.Close() })
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
	at := secretItem(t, s, ScopeUser, "github:alice", "GITHUB_TOKEN")
	rawVal, rawRef := at.Attrs[attrSecValue], at.Attrs[attrSecRefreshVal]
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
	// Absent is the only null this model has: the clear TAKES both attributes
	// away rather than writing an empty one a presence test would still find.
	at := secretItem(t, s, ScopeUser, "github:alice", "GITHUB_TOKEN")
	exp, expSet := at.Attrs[attrSecExpiresAt]
	ref, refSet := at.Attrs[attrSecRefreshVal]
	if expSet || refSet {
		t.Errorf("expires_at/refresh_val not cleared: exp=%q ref=%q", exp, ref)
	}
	// The row (value+provider) survives so the UI can show a reconnect prompt.
	conns, _ := s.ListUserConnections("github:alice")
	if len(conns) != 1 || conns[0].Provider != "github" {
		t.Errorf("connection row should survive clear: %+v", conns)
	}
}

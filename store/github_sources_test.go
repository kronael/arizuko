package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/kronael/arizuko/objstore"
)

func TestGitHubSourceKey(t *testing.T) {
	good := map[[2]string]string{
		{"repo", "kronael/arizuko"}: "repo/kronael/arizuko",
		{"org", "kronael"}:          "org/kronael",
		{"app", "arizuko-krons"}:    "app/arizuko-krons",
	}
	for in, want := range good {
		got, err := GitHubSourceKey(in[0], in[1])
		if err != nil || got != want {
			t.Errorf("GitHubSourceKey(%q,%q) = %q, %v; want %q", in[0], in[1], got, err, want)
		}
		kind, name, err := ParseGitHubSourceKey(got)
		if err != nil || kind != in[0] || name != in[1] {
			t.Errorf("ParseGitHubSourceKey(%q) = %q, %q, %v", got, kind, name, err)
		}
	}
	bad := [][2]string{
		{"repo", "kronael"}, {"repo", "kronael/arizuko/pr"}, {"org", "kronael/arizuko"},
		{"app", ""}, {"user", "kronael"}, {"repo", "-kronael/arizuko"},
		{"org", ".."}, {"repo", "kronael/a b"}, {"org", "kron ael"},
	}
	for _, in := range bad {
		if key, err := GitHubSourceKey(in[0], in[1]); err == nil {
			t.Errorf("GitHubSourceKey(%q,%q) = %q, want error", in[0], in[1], key)
		}
	}
	for _, key := range []string{"kronael", "user/kronael", "repo/kronael", ""} {
		if _, _, err := ParseGitHubSourceKey(key); err == nil {
			t.Errorf("ParseGitHubSourceKey(%q) accepted", key)
		}
	}
}

func TestGitHubSourceRoutes(t *testing.T) {
	rows, err := GitHubSourceRoutes("repo/kronael/arizuko", "eng")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 7 {
		t.Fatalf("repo rows = %d, want 7", len(rows))
	}
	if rows[0].Match != "chat_jid=github:kronael/arizuko/pr/* verb=message" || rows[0].Target != "eng" || rows[0].Seq != 10 {
		t.Errorf("repo trigger row = %+v", rows[0])
	}
	if rows[6].Match != "chat_jid=github:kronael/arizuko" || rows[6].Target != "eng#observe" || rows[6].Seq != 100 {
		t.Errorf("repo observe row = %+v", rows[6])
	}
	rows, err = GitHubSourceRoutes("org/kronael", "eng")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 8 || rows[7].Match != "chat_jid=github:kronael" || rows[0].Seq != 20 {
		t.Errorf("org rows = %+v", rows)
	}
	rows, err = GitHubSourceRoutes("app/arizuko-krons", "eng")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 8 || rows[0].Match != "chat_jid=github:*/*/pr/* verb=message" || rows[7].Match != "chat_jid=github:*" || rows[0].Seq != 30 {
		t.Errorf("app rows = %+v", rows)
	}
}

func keyedStore(t *testing.T) *Store {
	t.Helper()
	s := openMem(t)
	t.Cleanup(func() { s.Close() })
	s.SetSecretKeys([]byte("k1"))
	seedFolder(t, s, "eng")
	return s
}

func TestGitHubSource_AddListSecretRemove(t *testing.T) {
	s := keyedStore(t)
	secret, err := s.AddGitHubSource(GitHubSource{Source: "org/kronael", OwnerFolder: "eng"}, "eng")
	if err != nil {
		t.Fatal(err)
	}
	if len(secret) < 40 {
		t.Fatalf("secret too short: %q", secret)
	}
	stored := storedGitHubSecret(t, s, "org/kronael")
	if !strings.HasPrefix(stored, secretV2Prefix) || strings.Contains(stored, secret) {
		t.Fatalf("secret stored in plaintext: %q", stored)
	}
	got, ok, err := s.GitHubSourceSecret("org/kronael")
	if err != nil || !ok || got != secret {
		t.Fatalf("GitHubSourceSecret = %q, %v, %v", got, ok, err)
	}
	if _, ok, err := s.GitHubSourceSecret("org/nobody"); ok || err != nil {
		t.Fatalf("unknown source = %v, %v", ok, err)
	}
	list, err := s.ListGitHubSources("eng")
	if err != nil || len(list) != 1 || list[0].Source != "org/kronael" || list[0].OwnerFolder != "eng" {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if all, _ := s.ListGitHubSources(""); len(all) != 1 {
		t.Fatalf("list all = %+v", all)
	}
	if other, _ := s.ListGitHubSources("other"); len(other) != 0 {
		t.Fatalf("list other = %+v", other)
	}
	routes := s.AllRoutes()
	if len(routes) != 8 {
		t.Fatalf("seeded routes = %d, want 8: %+v", len(routes), routes)
	}
	if _, err := s.AddGitHubSource(GitHubSource{Source: "org/kronael", OwnerFolder: "eng"}, "eng"); !errors.Is(err, ErrGitHubSourceExists) {
		t.Fatalf("duplicate register err = %v", err)
	}
	if hit, err := s.RemoveGitHubSource("org/kronael", "other"); hit || err != nil {
		t.Fatalf("cross-folder revoke = %v, %v", hit, err)
	}
	hit, err := s.RemoveGitHubSource("org/kronael", "eng")
	if err != nil || !hit {
		t.Fatalf("revoke = %v, %v", hit, err)
	}
	if left := s.AllRoutes(); len(left) != 0 {
		t.Fatalf("routes left after revoke: %+v", left)
	}
	if _, ok, _ := s.GitHubSourceSecret("org/kronael"); ok {
		t.Fatal("source still resolves after revoke")
	}
}

func TestGitHubSource_NoKeyRefuses(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	seedFolder(t, s, "eng")
	ws := &WriteSet{}
	if _, err := s.AddGitHubSourceInto(context.Background(), ws,
		GitHubSource{Source: "app/x", OwnerFolder: "eng"}, "eng",
		ErrGitHubSourceExists); !errors.Is(err, ErrNoSecretsKey) {
		t.Fatalf("err = %v, want ErrNoSecretsKey", err)
	}
}

// storedGitHubSecret reads the sealed value off the item. No reader hands back
// the value at rest — GitHubSourceSecret opens it — so the at-rest assertion
// addresses the coordinate and the attribute name github_sources.go declares.
func storedGitHubSecret(t *testing.T, s *Store, source string) string {
	t.Helper()
	key, err := githubSourceKey(source)
	if err != nil {
		t.Fatal(err)
	}
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: GitHubSources, Partition: OwnerRoutd,
		Key: objstore.KeyPredicate{Op: objstore.ExactKey, Value: key.Sort},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("github_sources items for %q = %d, want 1", source, len(items))
	}
	return items[0].Attrs[attrGHSecret]
}

package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/kronael/arizuko/core"
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

// The whole seeded set, in order: a wildcard or a verb that drifts in any one
// row changes which folder a repository's traffic reaches (BUGS L417).
func TestGitHubSourceRoutes(t *testing.T) {
	cases := []struct {
		key  string
		want []core.Route
	}{
		{"repo/kronael/arizuko", []core.Route{
			{Seq: 10, Match: "chat_jid=github:kronael/arizuko/pr/* verb=mention", Target: "eng#{jid}"},
			{Seq: 10, Match: "chat_jid=github:kronael/arizuko/issue/* verb=mention", Target: "eng#{jid}"},
			{Seq: 10, Match: "chat_jid=github:kronael/arizuko verb=mention", Target: "eng#{jid}"},
			{Seq: 100, Match: "chat_jid=github:kronael/arizuko/pr/*", Target: "eng?observe#{jid}"},
			{Seq: 100, Match: "chat_jid=github:kronael/arizuko/issue/*", Target: "eng?observe#{jid}"},
			{Seq: 100, Match: "chat_jid=github:kronael/arizuko", Target: "eng?observe#{jid}"},
		}},
		{"org/kronael", []core.Route{
			{Seq: 20, Match: "chat_jid=github:kronael/*/pr/* verb=mention", Target: "eng#{jid}"},
			{Seq: 20, Match: "chat_jid=github:kronael/*/issue/* verb=mention", Target: "eng#{jid}"},
			{Seq: 20, Match: "chat_jid=github:kronael/* verb=mention", Target: "eng#{jid}"},
			{Seq: 20, Match: "chat_jid=github:kronael verb=mention", Target: "eng#{jid}"},
			{Seq: 200, Match: "chat_jid=github:kronael/*/pr/*", Target: "eng?observe#{jid}"},
			{Seq: 200, Match: "chat_jid=github:kronael/*/issue/*", Target: "eng?observe#{jid}"},
			{Seq: 200, Match: "chat_jid=github:kronael/*", Target: "eng?observe#{jid}"},
			{Seq: 200, Match: "chat_jid=github:kronael", Target: "eng?observe#{jid}"},
		}},
		{"app/arizuko-krons", []core.Route{
			{Seq: 30, Match: "chat_jid=github:*/*/pr/* verb=mention", Target: "eng#{jid}"},
			{Seq: 30, Match: "chat_jid=github:*/*/issue/* verb=mention", Target: "eng#{jid}"},
			{Seq: 30, Match: "chat_jid=github:*/* verb=mention", Target: "eng#{jid}"},
			{Seq: 30, Match: "chat_jid=github:* verb=mention", Target: "eng#{jid}"},
			{Seq: 300, Match: "chat_jid=github:*/*/pr/*", Target: "eng?observe#{jid}"},
			{Seq: 300, Match: "chat_jid=github:*/*/issue/*", Target: "eng?observe#{jid}"},
			{Seq: 300, Match: "chat_jid=github:*/*", Target: "eng?observe#{jid}"},
			{Seq: 300, Match: "chat_jid=github:*", Target: "eng?observe#{jid}"},
		}},
	}
	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			got, err := GitHubSourceRoutes(c.key, "eng")
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("rows = %d, want %d: %+v", len(got), len(c.want), got)
			}
			for i, w := range c.want {
				if got[i].Seq != w.Seq || got[i].Match != w.Match || got[i].Target != w.Target {
					t.Errorf("row %d = {%d %q %q}, want {%d %q %q}", i,
						got[i].Seq, got[i].Match, got[i].Target, w.Seq, w.Match, w.Target)
				}
			}
		})
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

func TestGitHubSourceRemoveKeepsOtherTargets(t *testing.T) {
	s := keyedStore(t)
	for _, tc := range []struct{ source, owner, target string }{
		{"app/one", "eng", "eng/sub"},
		{"app/two", "other", "other/sub"},
	} {
		if _, err := s.AddGitHubSource(GitHubSource{Source: tc.source, OwnerFolder: tc.owner}, tc.target); err != nil {
			t.Fatal(err)
		}
	}
	if hit, err := s.RemoveGitHubSource("app/one", "eng"); err != nil || !hit {
		t.Fatalf("remove = %v, %v", hit, err)
	}
	if _, ok, err := s.GitHubSourceSecret("app/one"); err != nil || ok {
		t.Fatalf("removed source remains: ok=%v err=%v", ok, err)
	}
	if _, ok, err := s.GitHubSourceSecret("app/two"); err != nil || !ok {
		t.Fatalf("other source missing: ok=%v err=%v", ok, err)
	}
	routes := s.AllRoutes()
	want, err := GitHubSourceRoutes("app/two", "other/sub")
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != len(want) {
		t.Fatalf("remaining routes = %+v, want %+v", routes, want)
	}
	for _, route := range routes {
		if !strings.HasPrefix(route.Target, "other/sub") {
			t.Errorf("unexpected route: %+v", route)
		}
	}
}

func TestGitHubSourceRemoveLegacyKeepsSourceAndRoutes(t *testing.T) {
	s := keyedStore(t)
	for _, tc := range []struct{ source, owner string }{{"app/one", "eng"}, {"app/two", "other"}} {
		if _, err := s.AddGitHubSource(GitHubSource{Source: tc.source, OwnerFolder: tc.owner}, tc.owner); err != nil {
			t.Fatal(err)
		}
	}
	key, err := githubSourceKey("app/one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Obj().GuardedBatch(context.Background(), nil, []objstore.Write{{
		Op: objstore.Update, Entity: GitHubSources, Key: key, Remove: []string{attrGHTargetFolder, attrGHRouteIDs},
	}}); err != nil {
		t.Fatal(err)
	}
	if hit, err := s.RemoveGitHubSource("app/one", "eng"); err == nil || hit {
		t.Fatalf("legacy removal = %v, %v; want error", hit, err)
	}
	if _, ok, err := s.GitHubSourceSecret("app/one"); err != nil || !ok {
		t.Fatalf("legacy source missing: %v, %v", ok, err)
	}
	if routes := s.AllRoutes(); len(routes) != 16 {
		t.Fatalf("routes after refused removal = %+v", routes)
	}
}

func TestGitHubSourceRemoveKeepsExistingAndReplacedRoutes(t *testing.T) {
	s := keyedStore(t)
	seeded, err := GitHubSourceRoutes("app/one", "eng")
	if err != nil {
		t.Fatal(err)
	}
	existingID, err := s.AddRoute(seeded[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddGitHubSource(GitHubSource{Source: "app/one", OwnerFolder: "eng"}, "eng"); err != nil {
		t.Fatal(err)
	}
	var replaced core.Route
	for _, route := range s.AllRoutes() {
		if route.Match == seeded[1].Match {
			replaced = route
		}
	}
	if replaced.ID == 0 {
		t.Fatal("seeded route missing")
	}
	if _, err := s.DeleteRoute(replaced.ID); err != nil {
		t.Fatal(err)
	}
	replacementID, err := s.AddRoute(seeded[1])
	if err != nil {
		t.Fatal(err)
	}
	var edited core.Route
	for _, route := range s.AllRoutes() {
		if route.Match == seeded[2].Match {
			edited = route
		}
	}
	if edited.ID == 0 {
		t.Fatal("route to edit missing")
	}
	edited.Target = "other#{jid}"
	if _, err := s.UpdateRoute(edited.ID, edited); err != nil {
		t.Fatal(err)
	}
	if hit, err := s.RemoveGitHubSource("app/one", "eng"); err != nil || !hit {
		t.Fatalf("remove = %v, %v", hit, err)
	}
	routes := s.AllRoutes()
	if len(routes) != 3 {
		t.Fatalf("remaining routes = %+v", routes)
	}
	ids := map[int64]bool{routes[0].ID: true, routes[1].ID: true, routes[2].ID: true}
	if !ids[existingID] || !ids[replacementID] || !ids[edited.ID] {
		t.Fatalf("remaining route IDs = %v, want %d, %d and %d", ids, existingID, replacementID, edited.ID)
	}
}

func TestGitHubSourceRemoveRejectsTargetOutsideOwner(t *testing.T) {
	s := keyedStore(t)
	if _, err := s.AddGitHubSource(GitHubSource{Source: "app/one", OwnerFolder: "eng"}, "other"); err != nil {
		t.Fatal(err)
	}
	if hit, err := s.RemoveGitHubSource("app/one", "eng"); err == nil || hit {
		t.Fatalf("remove outside target = %v, %v; want error", hit, err)
	}
	if routes := s.AllRoutes(); len(routes) != 8 {
		t.Fatalf("routes after refused removal = %+v", routes)
	}
}

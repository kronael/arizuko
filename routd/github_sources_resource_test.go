package routd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/ipc"
)

func TestGitHubSourcesAppAuthorizationREST(t *testing.T) {
	for _, tc := range []struct {
		kind, folder, scope string
		want                int
	}{
		{"app", "eng", "routes:write:own_group", 403},
		{"app", "eng", "routes:write", 403},
		{"app", "", "routes:write:own_group", 403},
		{"repo", "eng", "routes:write:own_group", 201},
		{"org", "eng", "routes:write:own_group", 201},
		{"app", "", "routes:write", 201},
	} {
		t.Run(tc.kind+tc.scope, func(t *testing.T) {
			db, h := authSrv(t, fakeVerifier{sub: "user:u", scope: []string{tc.scope}, folder: tc.folder})
			db.SetSecretKeys([]byte("k1"))
			_ = db.PutGroup(core.Group{Folder: "eng"})
			name := "bot"
			if tc.kind == "repo" {
				name = "owner/repo"
			}
			rec := doJSON(t, h, "POST", "/v1/github_sources", "", map[string]string{"owner_folder": "eng", "kind": tc.kind, "name": name})
			if rec.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tc.want, rec.Body.String())
			}
			if tc.want == 403 {
				rows, err := db.secretStore().ListGitHubSources("")
				if err != nil || len(rows) != 0 || routeCount(t, db) != 0 {
					t.Fatalf("denied registration left rows=%v err=%v", rows, err)
				}
				if _, ok, err := db.secretStore().GitHubSourceSecret("app/bot"); err != nil || ok {
					t.Fatalf("denied registration left secret: ok=%v err=%v", ok, err)
				}
			}
		})
	}
}

func TestGitHubSourcesAppAuthorizationMCP(t *testing.T) {
	for _, elevated := range []bool{false, true} {
		t.Run(map[bool]string{false: "scoped", true: "operator"}[elevated], func(t *testing.T) {
			db, err := OpenMem()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			db.SetSecretKeys([]byte("k1"))
			_ = db.PutGroup(core.Group{Folder: "eng"})
			grantMCPTools(t, db, "eng", "register_github_source")
			srv := NewServer(db, nil, nil, nil, 0, "")
			turn := turnMCP{folder: "eng", elevated: elevated}
			sock := groupfolder.IpcSocket(t.TempDir())
			pb := srv.githubSourcesPostBuild("eng", "folder:eng", srv.turnAuthorize(turn), srv.turnVisible(turn))
			stop, err := ipc.ServeMCP(sock, srv.buildRoutdFns(turn), srv.buildStoreFns(turn), "eng", elevated, 0, "folder:eng", pb)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(stop)
			text, failure := callToolText(t, sock, "register_github_source", map[string]any{"kind": "app", "name": "bot"})
			if (failure == "") != elevated {
				t.Fatalf("elevated=%v text=%s error=%s", elevated, text, failure)
			}
			if !elevated {
				secrets, err := srv.derivedConnectorSecrets(turnMCP{folder: "eng", chatJID: "github:victim/private/pr/1"}, []string{ipc.GitHubInstallationToken})
				if err == nil || !strings.Contains(err.Error(), "no github source") || len(secrets) != 0 {
					t.Fatalf("foreign repository credential: secrets=%v err=%v", secrets, err)
				}
				rows, err := db.secretStore().ListGitHubSources("")
				if err != nil || len(rows) != 0 || routeCount(t, db) != 0 {
					t.Fatalf("denied registration left rows=%v err=%v", rows, err)
				}
				if _, ok, err := db.secretStore().GitHubSourceSecret("app/bot"); err != nil || ok {
					t.Fatalf("denied registration left secret: ok=%v err=%v", ok, err)
				}
				for _, source := range []struct{ kind, name string }{{"repo", "owner/repo"}, {"org", "owner"}} {
					_, failure := callToolText(t, sock, "register_github_source", map[string]any{"kind": source.kind, "name": source.name})
					if failure != "" {
						t.Fatalf("%s registration denied: %s", source.kind, failure)
					}
				}
			}
		})
	}
}

func TestGitHubSources_RESTRoundTrip(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetSecretKeys([]byte("k1"))
	_ = db.PutGroup(core.Group{Folder: "eng"})
	h := NewServer(db, nil, nil, nil, 0, "krons.test").Handler()

	rec := doJSON(t, h, "POST", "/v1/github_sources", "",
		map[string]string{"owner_folder": "eng", "kind": "app", "name": "arizuko-krons"})
	if rec.Code != 201 {
		t.Fatalf("register status=%d body=%s", rec.Code, rec.Body.String())
	}
	var issued struct {
		Source, URL, Secret string
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &issued)
	if issued.Source != "app/arizuko-krons" || issued.URL != "https://krons.test/github/app/arizuko-krons" || len(issued.Secret) < 40 {
		t.Fatalf("issued=%+v", issued)
	}
	if got, ok, _ := db.secretStore().GitHubSourceSecret("app/arizuko-krons"); !ok || got != issued.Secret {
		t.Fatalf("stored secret %q ok=%v != issued %q", got, ok, issued.Secret)
	}
	if n := routeCount(t, db); n != 8 {
		t.Fatalf("seeded routes = %d, want 8", n)
	}

	rec = doJSON(t, h, "GET", "/v1/github_sources?owner_folder=eng", "", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"source":"app/arizuko-krons"`) {
		t.Fatalf("list status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), issued.Secret) || strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("list leaks the secret: %s", rec.Body.String())
	}

	rec = doJSON(t, h, "POST", "/v1/github_sources", "",
		map[string]string{"owner_folder": "eng", "kind": "app", "name": "arizuko-krons"})
	if rec.Code != 409 {
		t.Fatalf("duplicate status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, h, "POST", "/v1/github_sources", "",
		map[string]string{"owner_folder": "eng", "kind": "user", "name": "x"})
	if rec.Code != 400 {
		t.Fatalf("bad kind status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, h, "POST", "/v1/github_sources", "",
		map[string]string{"owner_folder": "eng", "kind": "repo", "name": "kronael/arizuko", "target_folder": "ops"})
	if rec.Code != 403 {
		t.Fatalf("outside-subtree target status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, h, "DELETE", "/v1/github_sources/app/arizuko-krons?owner_folder=other", "", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"deleted":false`) {
		t.Fatalf("cross-folder revoke status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, h, "DELETE", "/v1/github_sources/app/arizuko-krons?owner_folder=eng", "", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"deleted":true`) {
		t.Fatalf("revoke status=%d body=%s", rec.Code, rec.Body.String())
	}
	if n := routeCount(t, db); n != 0 {
		t.Fatalf("routes left after revoke = %d", n)
	}
}

func TestGitHubSources_NoSecretsKeyRefuses(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_ = db.PutGroup(core.Group{Folder: "eng"})
	h := NewServer(db, nil, nil, nil, 0, "").Handler()
	rec := doJSON(t, h, "POST", "/v1/github_sources", "",
		map[string]string{"owner_folder": "eng", "kind": "org", "name": "kronael"})
	if rec.Code != 500 || !strings.Contains(rec.Body.String(), "SECRETS_KEY") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if n := routeCount(t, db); n != 0 {
		t.Fatalf("routes seeded despite refused register = %d", n)
	}
}

func routeCount(t *testing.T, db *DB) int {
	t.Helper()
	rows, err := db.Routes()
	if err != nil {
		t.Fatal(err)
	}
	return len(rows)
}

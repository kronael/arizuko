package routd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kronael/arizuko/core"
)

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
	if n := routeCount(t, db); n != 5 {
		t.Fatalf("seeded routes = %d, want 5", n)
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

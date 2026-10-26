package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/store"
)

// generateInstance builds a bare instance dir and prepares routd's own tables,
// which is where proxyd_routes lives. `generate` needs .env + services/.
func generateInstance(t *testing.T, env string) string {
	t.Helper()
	t.Cleanup(func() { dropInstance(t) })
	base := t.TempDir()
	t.Setenv("ARIZUKO_DATA_DIR", base)
	dataDir := filepath.Join(base, "arizuko_g")
	seedCoreSource(t, filepath.Join(base, "release"))
	unsealPackageFixtures(t, dataDir)
	if err := os.MkdirAll(filepath.Join(dataDir, "store"), 0o755); err != nil {
		t.Fatal(err)
	}
	// core.LoadConfigFrom pins DATA_DIR process-wide on its FIRST call and
	// godotenv-Loads the instance .env into the process env, where compose's
	// envOr reads it as a fallback. One CLI command is one process, so this
	// only bites a test binary running many instances — pin it per test.
	t.Setenv("DATA_DIR", dataDir)
	if err := os.MkdirAll(filepath.Join(dataDir, "services"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, ".env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	rdb, err := routd.Open()
	if err != nil {
		t.Fatal(err)
	}
	rdb.Close()
	return dataDir
}

func instanceRoutes(t *testing.T, dataDir string) map[string]store.ProxydRoute {
	t.Helper()
	rdb, err := routd.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer rdb.Close()
	all, err := store.NewOver(rdb.Obj()).AllProxydRoutes()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]store.ProxydRoute{}
	for _, r := range all {
		out[r.Path] = r
	}
	return out
}

// The whole L45 fix end to end: `arizuko generate` writes the release's route
// table into proxyd_routes. Asserted on the DB, not on the compose file — the
// compose file is where these routes used to live (PROXYD_ROUTES_JSON) and
// where a route that reads correctly there still never reached the instance.
func TestGenerateWritesProxydRoutes(t *testing.T) {
	dataDir := generateInstance(t, "WEB_PORT=8095\nONBOARDING_ENABLED=true\n")
	generateCompose(dataDir)

	rows := instanceRoutes(t, dataDir)
	// The seven krons never received, plus the /dash/ catch-all beside them.
	for _, path := range []string{
		"/dash/onbod/", "/dash/timed/", "/invite/", "/me", "/me/", "/pair/", "/panel/", "/dash/",
	} {
		r, ok := rows[path]
		if !ok {
			t.Errorf("route %q missing from proxyd_routes", path)
			continue
		}
		if r.Owner != store.RouteOwnerArizuko {
			t.Errorf("route %q owner = %q, want %q", path, r.Owner, store.RouteOwnerArizuko)
		}
	}
}

// A route whose gated_by env is off is never written. The row must not exist at
// all: a path routed at a daemon this instance does not run answers 502 instead
// of falling through to the static site.
func TestGenerateSkipsGatedOffRoute(t *testing.T) {
	// "unset" has to mean unset in the PROCESS too: compose's envOr falls back
	// to os.Getenv, and an earlier test's .env reaches it through godotenv.Load.
	t.Setenv("ONBOARDING_ENABLED", "")
	dataDir := generateInstance(t, "WEB_PORT=8095\nWEBDAV_ENABLED=false\n")
	generateCompose(dataDir)

	rows := instanceRoutes(t, dataDir)
	if r, ok := rows["/dav/"]; ok {
		t.Errorf("/dav/ written with WEBDAV_ENABLED=false: %+v", r)
	}
	if _, ok := rows["/invite/"]; ok {
		t.Error("/invite/ written with ONBOARDING_ENABLED unset")
	}
	if _, ok := rows["/chat/"]; !ok {
		t.Error("/chat/ missing — ungated core routes must still be written")
	}
}

// Regenerating must not touch a route the operator added at a path arizuko does
// not ship (krons hand-added /arizuko), and must respect an operator-owned row
// at a path it does ship. The second is the override: owning the row is how an
// operator wins, and generate hands the path off for good.
func TestGenerateKeepsOperatorRoutes(t *testing.T) {
	t.Cleanup(func() { dropInstance(t) })
	dataDir := generateInstance(t, "WEB_PORT=8095\n")

	rdb, err := routd.Open()
	if err != nil {
		t.Fatal(err)
	}
	st := store.NewOver(rdb.Obj())
	if _, _, err := st.SyncProxydRoutes([]store.ProxydRoute{
		{Path: "/arizuko", RedirectTo: "/pub/arizuko/", Auth: "public", Owner: store.RouteOwnerOperator},
		{Path: "/panel/", Backend: "http://my-panel:8080", Auth: "user", Owner: store.RouteOwnerOperator},
	}); err != nil {
		t.Fatal(err)
	}
	rdb.Close()

	generateCompose(dataDir)

	rows := instanceRoutes(t, dataDir)
	own, ok := rows["/arizuko"]
	if !ok {
		t.Fatal("regenerate deleted the operator's own route")
	}
	if own.RedirectTo != "/pub/arizuko/" || own.Owner != store.RouteOwnerOperator {
		t.Errorf("/arizuko = %+v, want the operator's row untouched", own)
	}
	panel := rows["/panel/"]
	if panel.Backend != "http://my-panel:8080" || panel.Owner != store.RouteOwnerOperator {
		t.Errorf("/panel/ = %+v, want the operator's override kept", panel)
	}
	if rows["/chat/"].Owner != store.RouteOwnerArizuko {
		t.Errorf("/chat/ = %+v, want an arizuko-owned row beside the overrides", rows["/chat/"])
	}
}

// Generate is the release's writer: a route whose definition changed between
// releases must be rewritten in place, which is exactly what the seed-once path
// never did.
func TestGenerateRewritesChangedArizukoRoute(t *testing.T) {
	t.Cleanup(func() { dropInstance(t) })
	dataDir := generateInstance(t, "WEB_PORT=8095\n")

	rdb, err := routd.Open()
	if err != nil {
		t.Fatal(err)
	}
	// The row an older release left behind: same path, stale backend and auth.
	if _, _, err := store.NewOver(rdb.Obj()).SyncProxydRoutes([]store.ProxydRoute{{
		Path: "/chat/", Backend: "http://old-webd:8080", Auth: "user",
		Owner: store.RouteOwnerArizuko,
	}}); err != nil {
		t.Fatal(err)
	}
	rdb.Close()

	generateCompose(dataDir)

	got := instanceRoutes(t, dataDir)["/chat/"]
	if got.Backend != "http://webd:8080" || got.Auth != "optional" {
		t.Errorf("/chat/ = %+v, want the release's definition", got)
	}
}

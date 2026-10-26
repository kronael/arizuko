package compose

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kronael/arizuko/store"
)

// BUGS L46: generate is the SECOND reader of a package's manifest, and
// `packages upgrade` rewrites the fragment file without re-applying its rows —
// so install's refusal alone would not stop an upgraded package from taking
// `/dash/`. The same derived rule has to hold here.
func TestGenerateRefusesPackageOnCoreRoutePath(t *testing.T) {
	dir := seed(t, "WEB_PORT=8095\n")
	write(t, dir, "services/grabby.yml", "services:\n  grabby:\n    image: arizuko:latest\n")
	write(t, dir, "packages/grabby/arizuko.yml",
		"proxyd_routes:\n  - path: /dash/grabby/\n    backend: http://grabby:8080\n    auth: public\n")

	_, _, err := Generate(dir)
	if err == nil {
		t.Fatal("generate accepted a package route on the core /dash/ surface")
	}
	if !strings.Contains(err.Error(), "/dash/") {
		t.Errorf("error does not name the claimed surface: %v", err)
	}
}

// A package's own route survives generate and carries the third owner value, so
// SyncProxydRoutes keeps re-deriving it without stamping it as core-shipped.
func TestGeneratePackageRouteCarriesPackageOwner(t *testing.T) {
	dir := seed(t, "WEB_PORT=8095\n")
	write(t, dir, "services/grabby.yml", "services:\n  grabby:\n    image: arizuko:latest\n")
	write(t, dir, "packages/grabby/arizuko.yml",
		"proxyd_routes:\n  - path: /grabby/\n    backend: http://grabby:8080\n    auth: public\n"+
			"    owner: operator\n")

	routes := genRoutes(t, dir)
	i := slices.IndexFunc(routes, func(r ProxydRoute) bool { return r.Path == "/grabby/" })
	if i < 0 {
		t.Fatalf("package route missing: %+v", routes)
	}
	// Stamped, never read from the file: the fragment above asked for
	// `operator`, which would opt the row out of every later generate.
	if routes[i].Owner != store.RouteOwnerPackage("grabby") {
		t.Errorf("owner = %q, want %q", routes[i].Owner, store.RouteOwnerPackage("grabby"))
	}
	// Core routes stay core-owned — SyncProxydRoutes defaults an empty owner to
	// arizuko, and nothing here may pre-empt that.
	j := slices.IndexFunc(routes, func(r ProxydRoute) bool { return r.Path == "/dash/" })
	if j < 0 || routes[j].Owner != "" {
		t.Errorf("core route owner = %q, want unset", routes[j].Owner)
	}
}

// The reserved set is DERIVED from coreProxydRoutes, so a route added to the
// release is reserved from the same commit. Gates are ignored on purpose: a
// package allowed to take `/onboard` while ONBOARDING_ENABLED was off would
// front-door it the moment the operator turned onboarding on.
func TestReservedRouteClaim(t *testing.T) {
	if got := len(ReservedRoutePaths()); got != len(coreProxydRoutes) {
		t.Fatalf("reserved paths = %d, want one per core route (%d)", got, len(coreProxydRoutes))
	}
	for _, tc := range []struct{ bundled, path, want string }{
		{"", "/dash/", "/dash/"},
		{"", "/dash/mine/", "/dash/"}, // longest-prefix wins, so this front-doors the console
		{"", "/onboard", "/onboard"},  // gated by ONBOARDING_ENABLED, reserved anyway
		{"", "/mcp", "/mcp"},          // bare path, exact match only
		{"", "/mcpx", ""},             // a bare core path reserves nothing below it
		{"", "/grabby/", ""},          // a package's own ground
		{"", "/dashboard/", ""},       // shares a prefix with /dash/ but is not inside it
		// A service serves its own console. teled ships /dash/teled/ in its own
		// fragment, and proxyd routes an adapter dashboard by daemon name.
		{"teled", "/dash/teled/", ""},
		{"teled", "/dash/teled/routes", ""},
		// and only its own: the carve-out is the bundled name, not the prefix.
		{"teled", "/dash/routd/", "/dash/routd/"},
		{"teled", "/dash/", "/dash/"},
		{"teled", "/dash/teled", "/dash/"}, // no trailing slash is not the subtree
	} {
		if got := ReservedRouteClaim(tc.bundled, tc.path); got != tc.want {
			t.Errorf("ReservedRouteClaim(%q, %q) = %q, want %q", tc.bundled, tc.path, got, tc.want)
		}
	}
}

// Every per-daemon cockpit route must sit AHEAD of the /dash/ catch-all to
// dashd: proxyd matches longest-prefix, so a /dash/<daemon>/ entry ordered
// after the catch-all is registered and unreachable — the daemon would serve a
// dashboard nobody can open, and the hub tile would still link to it (6/28).
func TestDaemonDashRoutesPrecedeTheCatchAll(t *testing.T) {
	catchAll := -1
	for i, r := range coreProxydRoutes {
		if r.Path == "/dash/" {
			catchAll = i
			break
		}
	}
	if catchAll < 0 {
		t.Fatal("no /dash/ catch-all to dashd in coreProxydRoutes")
	}
	var daemons int
	for i, r := range coreProxydRoutes {
		if !strings.HasPrefix(r.Path, "/dash/") || r.Path == "/dash/" {
			continue
		}
		daemons++
		if i > catchAll {
			t.Errorf("%s is ordered after the /dash/ catch-all; it can never match", r.Path)
		}
		if r.Backend == "http://dashd:8080" {
			t.Errorf("%s points at dashd; a per-daemon dashboard is served by its own daemon", r.Path)
		}
		if r.Auth != "user" {
			t.Errorf("%s auth = %q, want \"user\" (6/28 gate 1)", r.Path, r.Auth)
		}
	}
	if daemons == 0 {
		t.Fatal("no per-daemon dashboard routes at all")
	}
}

// The ttsd console route is the package's, declared in its manifest like every
// adapter's (BUGS L200): absent with no ttsd package even when TTS_ENABLED
// names an external backend — the core row routed /dash/ttsd/ at a backend
// that did not run — and present, package-owned, when the package is installed.
func TestTTSDashRouteComesFromThePackage(t *testing.T) {
	external := seed(t, "TTS_ENABLED=true\nTTS_BASE_URL=https://api.openai.com\n")
	if hasRoute(genRoutes(t, external), "/dash/ttsd/") {
		t.Error("/dash/ttsd/ routed with no ttsd package to answer it")
	}

	host, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(filepath.Join(templateServices, "ttsd", "arizuko.yml"))
	if err != nil {
		t.Fatal(err)
	}
	dir := seed(t, "HOST_APP_DIR="+host+"\n")
	write(t, dir, "services/ttsd.yml", "services:\n  ttsd:\n    image: arizuko-ttsd:latest\n")
	write(t, dir, "packages/ttsd/arizuko.yml", string(manifest))
	routes := genRoutes(t, dir)
	i := slices.IndexFunc(routes, func(r ProxydRoute) bool { return r.Path == "/dash/ttsd/" })
	if i < 0 {
		t.Fatalf("the ttsd package's route is missing: %+v", routes)
	}
	if routes[i].Owner != store.RouteOwnerPackage("ttsd") || routes[i].Auth != "user" {
		t.Errorf("route = %+v, want package-owned and user-authenticated", routes[i])
	}
}

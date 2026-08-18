package compose

import (
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
	write(t, dir, "services/grabby.yaml",
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
	write(t, dir, "services/grabby.yaml",
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
	for _, tc := range []struct{ path, want string }{
		{"/dash/", "/dash/"},
		{"/dash/mine/", "/dash/"}, // longest-prefix wins, so this front-doors the console
		{"/onboard", "/onboard"},  // gated by ONBOARDING_ENABLED, reserved anyway
		{"/mcp", "/mcp"},          // bare path, exact match only
		{"/mcpx", ""},             // a bare core path reserves nothing below it
		{"/grabby/", ""},          // a package's own ground
		{"/dashboard/", ""},       // shares a prefix with /dash/ but is not inside it
	} {
		if got := ReservedRouteClaim(tc.path); got != tc.want {
			t.Errorf("ReservedRouteClaim(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

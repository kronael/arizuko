package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/store"
)

// BUGS L46: `install` applied whatever a package declared — any route path, any
// grant scope, its own owner value. These tests are the bound, and each refusal
// must leave NO partial state, the guarantee L44 bought.

// A package may not serve a path core arizuko already serves. `/dash/mine/` is
// the sharp case: it is not the console's own path, but proxyd takes the LONGEST
// prefix, so the package would front-door `/dash/` for every request under it.
func TestInstallRefusesReservedRoutePath(t *testing.T) {
	pkgChildRun()
	for _, path := range []string{"/dash/", "/dash/mine/", "/mcp", "/chat/", "/onboard"} {
		t.Run(path, func(t *testing.T) {
			base := t.TempDir()
			t.Setenv("ARIZUKO_DATA_DIR", base)
			dataDir := seedPkgInstance(t, base, "rr")
			src := writePkg(t, base, "rrpkg",
				"proxyd_routes:\n  - path: "+path+"\n    backend: http://rrpkg:8080\n    auth: public\n")

			if code := runPkgExpectingDeath(t, []string{"rr", "install", src}); code == 0 {
				t.Fatalf("a package claiming the core path %q installed", path)
			}
			assertNothingInstalled(t, dataDir, "rrpkg")
		})
	}
}

// A package's own path is untouched by the reserved rule — it bounds packages,
// it does not ban them — and the row lands under the third owner value, so
// generate keeps re-deriving it while the operator's own rows stay hands-off.
func TestInstallAcceptsPackageOwnRoutePath(t *testing.T) {
	t.Cleanup(func() { dropInstance(t) })
	base := t.TempDir()
	t.Setenv("ARIZUKO_DATA_DIR", base)
	src := writePkg(t, base, "okpkg",
		"proxyd_routes:\n  - path: /okpkg/\n    backend: http://okpkg:8080\n    auth: public\n")
	cmdPackages([]string{"ok", "install", src})

	rdb, err := routd.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer rdb.Close()
	routes, err := store.NewOver(rdb.Obj()).AllProxydRoutes()
	if err != nil {
		t.Fatal(err)
	}
	var got *store.ProxydRoute
	for i := range routes {
		if routes[i].Path == "/okpkg/" {
			got = &routes[i]
		}
	}
	if got == nil {
		t.Fatalf("the package's own route was not installed: %+v", routes)
	}
	if got.Owner != store.RouteOwnerPackage("okpkg") {
		t.Errorf("route owner = %q, want %q", got.Owner, store.RouteOwnerPackage("okpkg"))
	}
}

// Scope `**` is instance-wide authority, and operator-ness is emergent from
// exactly such a row, so it is refused whatever the operator named; `*` is the
// same rule one level down. Every other scope must lie inside the bound the
// operator handed the package, including at the deep end — `eng/**` reaches
// grandchildren that `eng/*` never covers.
func TestInstallRefusesUnboundedGrantScope(t *testing.T) {
	pkgChildRun()
	for _, tc := range []struct {
		name, scope string
		bound       []string
	}{
		{"instance wildcard", "**", []string{"--folder", "**"}},
		{"top level wildcard", "*", []string{"--folder", "eng/**"}},
		{"no bound named", "eng/*", nil},
		{"outside the bound", "ops", []string{"--folder", "eng/**"}},
		{"deeper than the bound", "eng/**", []string{"--folder", "eng/*"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			t.Setenv("ARIZUKO_DATA_DIR", base)
			dataDir := seedPkgInstance(t, base, "gb")
			src := writePkg(t, base, "gbpkg",
				"acl:\n  - principal: '@grabby'\n    action: admin\n    scope: '"+tc.scope+"'\n    effect: allow\n")

			args := append([]string{"gb", "install", src}, tc.bound...)
			if code := runPkgExpectingDeath(t, args); code == 0 {
				t.Fatalf("a package granting scope %q under bound %v installed", tc.scope, tc.bound)
			}
			assertNothingInstalled(t, dataDir, "gbpkg")
		})
	}
}

// The other half of the bound: a grant the operator DID hand over goes in.
// `eng/*` is inside `eng/**`.
func TestInstallAcceptsGrantInsideTheBound(t *testing.T) {
	t.Cleanup(func() { dropInstance(t) })
	base := t.TempDir()
	t.Setenv("ARIZUKO_DATA_DIR", base)
	src := writePkg(t, base, "gipkg",
		"acl:\n  - principal: '@ops'\n    action: mcp:reply\n    scope: eng/*\n    effect: allow\n")
	cmdPackages([]string{"gi", "install", src, "--folder", "eng/**"})

	rdb, err := routd.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer rdb.Close()
	rows, err := store.NewOver(rdb.Obj()).ListACL("@ops")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Scope != "eng/*" {
		t.Fatalf("grant inside the bound = %+v, want one row on eng/*", rows)
	}
}

// A package may not name its own owner. `operator` is the dangerous one: it
// would opt the row out of every later generate (SyncProxydRoutes skips operator
// rows), so the package's route could never be re-derived or corrected — the
// hole L45 left open deliberately.
func TestInstallRefusesPackageDeclaredRouteOwner(t *testing.T) {
	pkgChildRun()
	for _, owner := range []string{store.RouteOwnerOperator, store.RouteOwnerArizuko} {
		t.Run(owner, func(t *testing.T) {
			base := t.TempDir()
			t.Setenv("ARIZUKO_DATA_DIR", base)
			dataDir := seedPkgInstance(t, base, "ow")
			src := writePkg(t, base, "owpkg",
				"proxyd_routes:\n  - path: /owpkg/\n    backend: http://owpkg:8080\n"+
					"    auth: public\n    owner: "+owner+"\n")

			if code := runPkgExpectingDeath(t, []string{"ow", "install", src}); code == 0 {
				t.Fatalf("a package stamping owner=%s installed", owner)
			}
			assertNothingInstalled(t, dataDir, "owpkg")
		})
	}
}

// assertNothingInstalled is the L44 guarantee held for every L46 refusal: no
// fragment on disk, no record, and no row of either kind the package declared.
func assertNothingInstalled(t *testing.T, dataDir, name string) {
	t.Helper()
	for _, n := range []string{name + ".yml", name + ".yaml"} {
		if _, err := os.Stat(filepath.Join(dataDir, "services", n)); err == nil {
			t.Errorf("a refused install left %s behind", n)
		}
	}
	rdb, err := routd.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer rdb.Close()
	rst := store.NewOver(rdb.Obj())
	grants, err := rst.ListACL("")
	if err != nil {
		t.Fatal(err)
	}
	// `granted_by` is an ATTRIBUTE, not part of the coordinate, so the grants a
	// package owns are found by reading the projection and comparing.
	for _, g := range grants {
		if g.GrantedBy == "package:"+name {
			t.Errorf("a refused install left a grant behind: %+v", g)
		}
	}
	routes, err := rst.AllProxydRoutes()
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 0 {
		t.Errorf("a refused install left proxyd routes behind: %d", len(routes))
	}
	if _, ok, _ := rdb.InstalledPackage(routd.InstanceWide, name); ok {
		t.Error("a refused install left an installed-package record")
	}
}

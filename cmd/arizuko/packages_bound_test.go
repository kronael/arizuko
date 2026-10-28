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

// A package's own path is untouched by the reserved rule — it bounds packages,
// it does not ban them — and the row lands under the third owner value, so
// generate keeps re-deriving it while the operator's own rows stay hands-off.

// Scope `**` is instance-wide authority, and operator-ness is emergent from
// exactly such a row, so it is refused whatever the operator named; `*` is the
// same rule one level down. Every other scope must lie inside the bound the
// operator handed the package, including at the deep end — `eng/**` reaches
// grandchildren that `eng/*` never covers.

// The other half of the bound: a grant the operator DID hand over goes in.
// `eng/*` is inside `eng/**`.

// A package may not name its own owner. `operator` is the dangerous one: it
// would opt the row out of every later generate (SyncProxydRoutes skips operator
// rows), so the package's route could never be re-derived or corrected — the
// hole L45 left open deliberately.

// assertNothingInstalled is the L44 guarantee held for every L46 refusal: no
// fragment on disk, no record, and no row of either kind the package declared.
func assertNothingInstalled(t *testing.T, dataDir, name string) {
	t.Helper()
	for _, n := range []string{name + ".yml"} {
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
	for _, route := range routes {
		if route.Owner == store.RouteOwnerPackage(name) {
			t.Errorf("a refused install left a proxyd route behind: %+v", route)
		}
	}
	if _, ok, _ := rdb.InstalledPackage(routd.InstanceWide, name); ok {
		t.Error("a refused install left an installed-package record")
	}
}

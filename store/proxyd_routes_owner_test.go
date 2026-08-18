package store

import "testing"

// BUGS L46: a package's route needed a THIRD owner value. `arizuko` would let it
// pass as core-shipped; `operator` would opt it out of every later generate, so
// an upgraded package could never correct its own backend. `package:<name>`
// falls on the arizuko side of the sync split — rewritten every run — because
// generate re-derives it from the package's own manifest fragment, exactly as it
// re-derives a core route from the release table.
func TestSyncProxydRoutesRewritesPackageRoutesAndKeepsTheOwner(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	pkg := RouteOwnerPackage("helpbot")
	if _, _, err := s.SyncProxydRoutes([]ProxydRoute{
		{Path: "/helpbot/", Backend: "http://helpbot:8080", Auth: "public", Owner: pkg},
	}); err != nil {
		t.Fatal(err)
	}

	// The package is upgraded and its fragment now points somewhere else.
	written, kept, err := s.SyncProxydRoutes([]ProxydRoute{
		{Path: "/helpbot/", Backend: "http://helpbot:9090", Auth: "user", Owner: pkg},
	})
	if err != nil {
		t.Fatal(err)
	}
	if written != 1 || kept != 0 {
		t.Fatalf("written=%d kept=%d, want 1/0 — a package route is generate-managed", written, kept)
	}
	r, ok := routeAt(t, s, "/helpbot/")
	if !ok {
		t.Fatal("/helpbot/ missing after sync")
	}
	if r.Backend != "http://helpbot:9090" {
		t.Errorf("upgraded backend did not reach proxyd_routes: %+v", r)
	}
	if r.Owner != pkg {
		t.Errorf("owner = %q, want %q — the rewrite must not relabel the row as core-shipped", r.Owner, pkg)
	}
}

// The operator side of the split is unchanged: taking a package's path over
// still hands it off for good.
func TestSyncProxydRoutesStillSkipsOperatorOwnedPackagePath(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.PutProxydRoute(ProxydRoute{
		Path: "/helpbot/", Backend: "http://mine:8080", Auth: "user", Owner: RouteOwnerOperator}); err != nil {
		t.Fatal(err)
	}
	written, kept, err := s.SyncProxydRoutes([]ProxydRoute{
		{Path: "/helpbot/", Backend: "http://helpbot:8080", Auth: "public", Owner: RouteOwnerPackage("helpbot")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if written != 0 || kept != 1 {
		t.Fatalf("written=%d kept=%d, want 0/1", written, kept)
	}
	r, _ := routeAt(t, s, "/helpbot/")
	if r.Backend != "http://mine:8080" || r.Owner != RouteOwnerOperator {
		t.Errorf("generate overwrote the operator's takeover: %+v", r)
	}
}

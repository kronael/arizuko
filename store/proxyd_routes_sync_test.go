package store

import "testing"

// SyncProxydRoutes is what `arizuko generate` calls to carry a release's proxyd
// routes to an existing instance. Before it, the routes travelled as
// PROXYD_ROUTES_JSON and proxyd read that env var only while proxyd_routes was
// empty — so on krons, which had 12 rows from its first boot, the 7 routes
// added to later releases (/invite/, /pair/, /me, /me/, /panel/, /dash/onbod/,
// /dash/timed/) never arrived, and every health check stayed green (BUGS L45).

func routeAt(t *testing.T, s *Store, path string) (ProxydRoute, bool) {
	t.Helper()
	all, err := s.AllProxydRoutes()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range all {
		if r.Path == path {
			return r, true
		}
	}
	return ProxydRoute{}, false
}

// A route arizuko added since this instance first booted must land, and one
// whose definition changed in the release must be rewritten. This is the L45
// fault itself: the seed-once path left both stale forever.
func TestSyncProxydRoutesWritesAndUpdatesArizukoRoutes(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// The instance as it stands: one route, pointing at the old backend.
	if _, _, err := s.SyncProxydRoutes([]ProxydRoute{
		{Path: "/dash/", Backend: "http://dashd:8080", Auth: "user"},
	}); err != nil {
		t.Fatal(err)
	}

	// The next release moves /dash/ and adds /invite/.
	written, kept, err := s.SyncProxydRoutes([]ProxydRoute{
		{Path: "/dash/", Backend: "http://dashd:9090", Auth: "operator"},
		{Path: "/invite/", Backend: "http://onbod:8080", Auth: "public", GatedBy: "ONBOARDING_ENABLED"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if written != 2 || kept != 0 {
		t.Fatalf("written=%d kept=%d, want 2/0", written, kept)
	}

	dash, ok := routeAt(t, s, "/dash/")
	if !ok {
		t.Fatal("/dash/ missing")
	}
	if dash.Backend != "http://dashd:9090" || dash.Auth != "operator" {
		t.Errorf("/dash/ not rewritten from the release: %+v", dash)
	}
	if dash.Owner != RouteOwnerArizuko {
		t.Errorf("/dash/ owner = %q, want %q", dash.Owner, RouteOwnerArizuko)
	}
	inv, ok := routeAt(t, s, "/invite/")
	if !ok {
		t.Fatal("/invite/ never arrived — the L45 fault")
	}
	if inv.Backend != "http://onbod:8080" || inv.GatedBy != "ONBOARDING_ENABLED" {
		t.Errorf("/invite/ = %+v", inv)
	}
}

// The two rows generate must not touch: an operator's route at a path arizuko
// does not ship (krons hand-added /arizuko), and an operator's deliberate
// override at a path arizuko DOES ship. Owning the row is the override; editing
// arizuko's row is not (proxyd's ActionUpdate refuses that).
func TestSyncProxydRoutesLeavesOperatorRoutesAlone(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	own := ProxydRoute{Path: "/arizuko", RedirectTo: "/pub/arizuko/", Auth: "public",
		Owner: RouteOwnerOperator}
	override := ProxydRoute{Path: "/dash/", Backend: "http://my-dash:8080", Auth: "user",
		Owner: RouteOwnerOperator}
	if _, _, err := s.SyncProxydRoutes([]ProxydRoute{own, override}); err != nil {
		t.Fatal(err)
	}

	written, kept, err := s.SyncProxydRoutes([]ProxydRoute{
		{Path: "/dash/", Backend: "http://dashd:8080", Auth: "user"},
		{Path: "/me", Backend: "http://webd:8080", Auth: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if written != 1 || kept != 1 {
		t.Fatalf("written=%d kept=%d, want 1/1", written, kept)
	}

	// Not shipped, so never read and never written: it survives byte for byte.
	got, ok := routeAt(t, s, "/arizuko")
	if !ok {
		t.Fatal("operator route at a path arizuko does not ship was deleted")
	}
	if got.RedirectTo != "/pub/arizuko/" || got.Owner != RouteOwnerOperator {
		t.Errorf("/arizuko = %+v, want the operator's row untouched", got)
	}
	// Shipped, but the operator owns it: arizuko hands the path off.
	dash, _ := routeAt(t, s, "/dash/")
	if dash.Backend != "http://my-dash:8080" || dash.Owner != RouteOwnerOperator {
		t.Errorf("/dash/ = %+v, want the operator's override kept", dash)
	}
	if _, ok := routeAt(t, s, "/me"); !ok {
		t.Error("/me missing — an operator-owned path must not stop the rest of the sync")
	}
}

// A package's hot-applied route is generate-managed, not an operator override:
// it is re-derived from the package's `*-routes.json` on every generate, so an
// unset Owner must mean arizuko. Stamping it `operator` would freeze a
// package's route at whatever the install-time release shipped.
func TestSyncProxydRoutesDefaultsToArizukoOwner(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, _, err := s.SyncProxydRoutes([]ProxydRoute{
		{Path: "/rp/", Backend: "http://routepkg:8080", Auth: "public"}}); err != nil {
		t.Fatal(err)
	}
	r, ok := routeAt(t, s, "/rp/")
	if !ok || r.Owner != RouteOwnerArizuko {
		t.Fatalf("route = %+v ok=%v, want owner %q", r, ok, RouteOwnerArizuko)
	}
}

// TestRouteSurvivesGenerate: exactly one owner is left alone by
// SyncProxydRoutes. The PATCH guard used to denylist `arizuko` while this loop
// allowlisted `operator`, so a `package:<name>` row passed the guard with 200
// and was silently overwritten at the next deploy (BUGS L81).
func TestRouteSurvivesGenerate(t *testing.T) {
	for _, c := range []struct {
		owner string
		want  bool
	}{
		{RouteOwnerOperator, true},
		{RouteOwnerArizuko, false},
		{RouteOwnerPackage("slakd"), false},
		{RouteOwnerPackage("teled"), false},
		{"", false},
	} {
		if got := RouteSurvivesGenerate(c.owner); got != c.want {
			t.Errorf("RouteSurvivesGenerate(%q) = %v, want %v", c.owner, got, c.want)
		}
	}
}

// A release ships the same path on every generate, so the second write of a
// path must rewrite the row rather than fail on the primary key. A plain
// INSERT errors here, and SyncProxydRoutes reports that error for the whole
// set — one unchanged route would stop the rest of the sync.
func TestSyncProxydRoutesRewritesAPathInPlace(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	r := ProxydRoute{Path: "/slack/", Backend: "http://slakd:8080", Auth: "public",
		GatedBy: "SLACK_BOT_TOKEN", PreserveHeaders: []string{"X-Slack-Signature"}}
	if _, _, err := s.SyncProxydRoutes([]ProxydRoute{r}); err != nil {
		t.Fatal(err)
	}
	r.Backend = "http://slakd2:8080"
	if _, _, err := s.SyncProxydRoutes([]ProxydRoute{r}); err != nil {
		t.Fatal(err)
	}

	got, ok := routeAt(t, s, "/slack/")
	if !ok {
		t.Fatal("/slack/ missing after the rewrite")
	}
	if got.Backend != "http://slakd2:8080" || len(got.PreserveHeaders) != 1 {
		t.Errorf("row not rewritten: %+v", got)
	}
}

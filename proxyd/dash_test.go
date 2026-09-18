package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/store"
	"github.com/kronael/arizuko/tests/testutils"
)

// proxyd is the front door: a path on its own mux never reaches s.route, where
// a routed request is authenticated. Every dashboard path must therefore be
// mounted behind requireAuth, or the route table and its write forms answer an
// unauthenticated caller.
func TestDashboardPathsAreBehindRequireAuth(t *testing.T) {
	s := &server{cfg: config{authSecret: "x"}, rr: newRoutesResource(nil, nil)}
	mux := s.mux()
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/dash/proxyd/"},
		{http.MethodPost, "/dash/proxyd/"},
		{http.MethodPost, "/dash/proxyd/delete"},
	} {
		r := httptest.NewRequest(tc.method, "http://k.example"+tc.path, strings.NewReader(""))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		// requireAuth answers an unauthenticated caller with a redirect to the
		// login flow (or 404 when AUTH_SECRET is unset) — never with the page.
		if w.Code == http.StatusOK {
			t.Errorf("%s %s served 200 to an unauthenticated caller", tc.method, tc.path)
		}
		if strings.Contains(w.Body.String(), "Longest prefix wins") {
			t.Errorf("%s %s leaked the route table unauthenticated", tc.method, tc.path)
		}
	}
}

// A core route must not be deletable from the dashboard: compose rewrites it on
// the next generate, so the button would appear to work and then undo itself.
func TestCoreRoutesAreNotDeletable(t *testing.T) {
	got := proxydRouteTable([]Route{
		{Path: "/dash/", Backend: "http://dashd:8080", Auth: "user"},
		{Path: "/mine/", Backend: "http://mine:8080", Auth: "user"},
	})
	if strings.Count(got, "/dash/proxyd/delete") != 1 {
		t.Error("expected exactly one delete form — the non-core route's")
	}
	if !strings.Contains(got, `<span class="dim">core</span>`) {
		t.Error("the core route is not marked as such")
	}
}

// The table is ordered by match precedence, not alphabetically: that ordering
// is the only question it answers, and alphabetical hides which prefix wins.
func TestRouteTableIsInMatchOrder(t *testing.T) {
	got := proxydRouteTable([]Route{
		{Path: "/a/", Backend: "b"},
		{Path: "/a/longer/", Backend: "b"},
	})
	if strings.Index(got, "/a/longer/") > strings.Index(got, `<code>/a/</code>`) {
		t.Error("the shorter prefix is listed first; longest prefix wins at match time")
	}
}

// An empty route table is an outage, not a blank page.
func TestNoRoutesIsAnError(t *testing.T) {
	if !strings.Contains(proxydRouteTable(nil), "banner-err") {
		t.Error("zero routes rendered as a normal empty table")
	}
}

// The create form defaults to the CLOSED gate: a route added without thinking
// about auth must not be public.
func TestCreateFormDefaultsToUserAuth(t *testing.T) {
	got := routeCreateForm()
	if !strings.Contains(got, `<option value="user" selected>`) {
		t.Error("the auth default is not `user`")
	}
}

// The operator gate still applies after requireAuth: authentication is not
// authorization.
func TestDashRefusesAFolderScopedCaller(t *testing.T) {
	s := &server{ks: &auth.KeySet{}}
	r := httptest.NewRequest(http.MethodGet, "http://k.example/dash/proxyd/", nil)
	r.Header.Set("X-User-Groups", `["acme"]`)
	w := httptest.NewRecorder()
	s.handleDash(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("folder-scoped caller = %d, want 403", w.Code)
	}
}

// An operator's own route must survive the next deploy. The store defaults an
// empty owner to RouteOwnerArizuko — "the release derived this" — and
// `arizuko generate` deletes every arizuko-owned row it did not derive, so a
// route written from the dashboard without an owner vanished on the next
// generate (BUGS L205).
func TestDashRouteIsStampedOperator(t *testing.T) {
	st := testutils.NewRoutdStore(t)
	s := &server{stRoutd: st}
	if err := s.writeRoute(context.Background(), store.ProxydRoute{
		Path: "/ops/", Backend: "http://ops:8080", Auth: "user",
		Owner: store.RouteOwnerOperator,
	}); err != nil {
		t.Fatalf("writeRoute: %v", err)
	}
	routes, err := st.AllProxydRoutes()
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	var found bool
	for _, rt := range routes {
		if rt.Path != "/ops/" {
			continue
		}
		found = true
		if rt.Owner != store.RouteOwnerOperator {
			t.Errorf("owner = %q, want %q — generate deletes an arizuko-owned row it did not derive",
				rt.Owner, store.RouteOwnerOperator)
		}
	}
	if !found {
		t.Fatalf("the route did not land; got %+v", routes)
	}
}

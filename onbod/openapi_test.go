package main

import (
	"net/http/httptest"
	"testing"

	_ "github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/resreg/resregtest"
)

// TestOnbodOpenAPI_AdvertisesOnlyWhatItMounts is the F21/F27/F32 class guard
// applied to onbod. Both arguments are the PRODUCTION values —
// onbodOpenAPIResources is the list the daemon hands OpenAPIHandler and
// newOnbodMux is the routing table it serves — so the test cannot pass by
// agreeing with a copy of itself (BUGS F40).
//
// nil ks is the local-dev branch of stripUnsignedGuard, which changes no
// route; the probe never invokes a handler, it reads the pattern mux.Handler
// resolves to.
func TestOnbodOpenAPI_AdvertisesOnlyWhatItMounts(t *testing.T) {
	db := testDB(t)
	mux := newOnbodMux(db, db, config{}, nil)

	// Half 1 — the class guard. n == 0 would mean the emitter produced nothing
	// and every assertion below was skipped; onbod advertises three resources.
	if n := resregtest.AssertServesWhatItAdvertises(t, "onbod", onbodOpenAPIResources, mux); n == 0 {
		t.Fatal("onbod advertises no operation — the guard checked nothing")
	}

	// Half 2 — the ownership anchor. proxyd owns proxyd_routes and routd owns
	// scheduled_tasks; onbod must mount neither.
	resregtest.AssertServesNoneOf(t, "onbod", []string{"proxyd_routes", "scheduled_tasks"}, mux)
}

// TestOnbodMux_ServesItsPublicRoutes pins what newOnbodMux DOES serve, so the
// guard above cannot be satisfied by a mux that serves nothing at all.
func TestOnbodMux_ServesItsPublicRoutes(t *testing.T) {
	db := testDB(t)
	mux := newOnbodMux(db, db, config{}, nil)
	for _, want := range [][2]string{
		{"GET", "/health"},
		{"GET", "/openapi.json"},
		{"GET", "/onboard"},
		{"GET", "/invite/abc"},
		{"GET", "/dash/onbod/"},
	} {
		if _, pattern := mux.Handler(httptest.NewRequest(want[0], want[1], nil)); pattern == "" {
			t.Errorf("onbod's mux does not serve %s %s", want[0], want[1])
		}
	}
}

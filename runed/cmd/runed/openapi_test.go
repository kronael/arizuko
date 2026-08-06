package main

import (
	"net/http/httptest"
	"testing"

	_ "github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/resreg/resregtest"
	"github.com/kronael/arizuko/runed"
)

// testServer is the minimum runed.Server the mux needs: auditResource reads
// db.SQL() at REGISTRATION time, so a nil DB panics before any route is
// mounted. Manager and Verifier stay nil — no handler is ever invoked here, the
// probe only reads the pattern mux.Handler resolves to.
func testServer(t *testing.T) *runed.Server {
	t.Helper()
	db, err := runed.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return runed.NewServer(nil, db, nil)
}

// TestRunedOpenAPI_AdvertisesOnlyWhatItMounts is the F21/F27/F32 class guard
// applied to runed. Both arguments are the PRODUCTION values —
// runedOpenAPIResources is the list the running daemon hands OpenAPIHandler and
// newRunedMux is the routing table it serves — so the test cannot pass by
// agreeing with a copy of itself (BUGS F40).
//
func TestRunedOpenAPI_AdvertisesOnlyWhatItMounts(t *testing.T) {
	mux := newRunedMux(testServer(t))

	// Half 1 — the class guard. n == 0 would mean the emitter produced nothing
	// and every assertion below was skipped; runed advertises `audit`, so a zero
	// here is the vacuity the guard exists to prevent.
	if n := resregtest.AssertServesWhatItAdvertises(t, "runed", runedOpenAPIResources, mux); n == 0 {
		t.Fatal("runed advertises no operation — the guard checked nothing")
	}

	// Half 2 — the ownership anchor. proxyd owns proxyd_routes and onbod owns
	// onboarding; runed must mount neither.
	resregtest.AssertServesNoneOf(t, "runed", []string{"proxyd_routes", "onboarding"}, mux)
}

// TestRunedMux_ServesItsPublicRoutes pins what newRunedMux DOES serve, so the
// guard above cannot be satisfied by a mux that serves nothing at all.
func TestRunedMux_ServesItsPublicRoutes(t *testing.T) {
	mux := newRunedMux(testServer(t))
	for _, want := range [][2]string{
		{"GET", "/health"},
		{"GET", "/openapi.json"},
		{"GET", "/v1/audit"},
		{"POST", "/v1/runs"},
	} {
		if _, pattern := mux.Handler(httptest.NewRequest(want[0], want[1], nil)); pattern == "" {
			t.Errorf("runed's mux does not serve %s %s", want[0], want[1])
		}
	}
}

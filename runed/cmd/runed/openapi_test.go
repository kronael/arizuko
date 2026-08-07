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

// TestRunedOpenAPI_AdvertisesOnlyWhatItMounts pins runed's whole documented
// surface. newRunedMux is the PRODUCTION routing table, and since BUGS F33 it
// is the only input to the document, so what this freezes is what runed serves.
func TestRunedOpenAPI_AdvertisesOnlyWhatItMounts(t *testing.T) {
	mux := newRunedMux(testServer(t))

	// runed hand-rolls GET /v1/sessions and GET /v1/sessions/recent over
	// session_log. Neither may appear here: authd's `sessions` resource is a
	// different table behind the same path, and only a RegisterREST mount of
	// that resource documents it (BUGS F33/F46).
	resregtest.AssertAdvertises(t, "runed", mux, []string{
		"GET /v1/audit",
	})

	// The ownership anchor. proxyd owns proxyd_routes and onbod owns
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

package main

import (
	"net/http/httptest"
	"testing"

	_ "github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/resreg/resregtest"
)

// TestOnbodOpenAPI_AdvertisesOnlyWhatItMounts pins onbod's whole documented
// surface. Since BUGS F33 the document is DERIVED from newOnbodMux, so this is
// the truth about what onbod serves, frozen.
//
// nil ks is the local-dev branch of stripUnsignedGuard, which changes no
// route; the probe never invokes a handler, it reads the pattern mux.Handler
// resolves to.
func TestOnbodOpenAPI_AdvertisesOnlyWhatItMounts(t *testing.T) {
	db := testDB(t)
	mux := newOnbodMux(db, db, config{}, nil)

	resregtest.AssertAdvertises(t, "onbod", mux, []string{
		"DELETE /v1/gates/{gate}",
		"DELETE /v1/invites/{ref}",
		"DELETE /v1/onboarding/{jid}",
		"GET /v1/audit",
		"GET /v1/gates",
		"GET /v1/invites",
		"GET /v1/onboarding",
		"POST /v1/invites",
		"POST /v1/onboarding",
		"POST /v1/onboarding/{jid}/approve",
		"POST /v1/onboarding/{jid}/reprompt",
		"PUT /v1/gates/{gate}",
	})

	// The ownership anchor. proxyd owns proxyd_routes and routd owns
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

package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kronael/arizuko/core"
	_ "github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/resreg/resregtest"
)

// testMux builds authd's real routing table. A real Authd is required, not a
// zero server: mountAudit reads a.db at REGISTRATION time, so a nil one panics
// before any route is mounted. No handler is ever invoked here — the probe only
// reads the pattern mux.Handler resolves to.
func testMux(t *testing.T, cfg *core.Config) *http.ServeMux {
	t.Helper()
	_, a := auditTestAuthd(t)
	return (&server{a: a}).mux(cfg)
}

// TestAuthdOpenAPI_AdvertisesOnlyWhatItMounts is the F21/F27/F32 class guard
// applied to authd. Both arguments are the PRODUCTION values —
// authdOpenAPIResources is the list the daemon hands OpenAPIHandler and
// server.mux is the routing table it serves — so the test cannot pass by
// agreeing with a copy of itself (BUGS F40).
func TestAuthdOpenAPI_AdvertisesOnlyWhatItMounts(t *testing.T) {
	mux := testMux(t, nil)

	// Half 1 — the class guard. n == 0 would mean the emitter produced nothing
	// and every assertion below was skipped; authd advertises three resources.
	if n := resregtest.AssertServesWhatItAdvertises(t, "authd", authdOpenAPIResources, mux); n == 0 {
		t.Fatal("authd advertises no operation — the guard checked nothing")
	}

	// Half 2 — the ownership anchor. proxyd owns proxyd_routes and onbod owns
	// onboarding; authd must mount neither.
	resregtest.AssertServesNoneOf(t, "authd", []string{"proxyd_routes", "onboarding"}, mux)
}

// TestAuthdMux_CoversTheWholeServedSurface pins that mux() is the complete
// surface, not a prefix of it. /openapi.json used to be registered in main()
// after mux() returned, which is exactly the blind spot that let the doc and
// the routing table drift unobserved; a guard probing a mux missing the mount
// under test guards nothing. /auth/* moved in with it, so this is the whole
// table modulo the two env/config-gated mounts asserted below.
func TestAuthdMux_CoversTheWholeServedSurface(t *testing.T) {
	mux := testMux(t, nil)
	for _, want := range [][2]string{
		{"GET", "/health"},
		{"GET", "/openapi.json"},
		{"GET", "/v1/keys"},
		{"POST", "/v1/tokens"},
		{"GET", "/v1/audit"},
		{"GET", "/v1/signing_keys"},
		{"GET", "/v1/sessions"},
	} {
		if _, pattern := mux.Handler(httptest.NewRequest(want[0], want[1], nil)); pattern == "" {
			t.Errorf("authd's mux does not serve %s %s", want[0], want[1])
		}
	}
}

// TestAuthdMux_MountsOAuthWhenConfigured is the other half of "complete
// surface": /auth/* is config-gated, so a nil cfg leaving it unmounted must not
// be mistaken for mux() having dropped it. Both branches are pinned.
func TestAuthdMux_MountsOAuthWhenConfigured(t *testing.T) {
	if _, pattern := testMux(t, nil).Handler(
		httptest.NewRequest("GET", "/auth/login", nil)); pattern != "" {
		t.Errorf("nil cfg mounted /auth/login as %q, want unmounted", pattern)
	}
	mux := testMux(t, &core.Config{AuthBaseURL: "https://example.test"})
	for _, path := range []string{"/auth/login", "/auth/me"} {
		if _, pattern := mux.Handler(httptest.NewRequest("GET", path, nil)); pattern == "" {
			t.Errorf("configured mux does not serve GET %s", path)
		}
	}
}

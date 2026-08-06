package main

import (
	"net/http/httptest"
	"testing"

	_ "github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/resreg/resregtest"
)

// TestProxydOpenAPI_AdvertisesOnlyWhatItMounts is the F21/F27/F32 class guard
// applied to proxyd. Both arguments are the PRODUCTION values —
// proxydOpenAPIResources is the list the daemon hands OpenAPIHandler and
// server.mux is the routing table it serves — so the test cannot pass by
// agreeing with a copy of itself (BUGS F40).
func TestProxydOpenAPI_AdvertisesOnlyWhatItMounts(t *testing.T) {
	s, up := testRouteServer(t, nil, "testsecret")
	defer up.Close()
	mux := s.mux()

	// Half 1 — the class guard. n == 0 would mean the emitter produced nothing
	// and every assertion below was skipped; proxyd advertises proxyd_routes.
	if n := resregtest.AssertServesWhatItAdvertises(t, "proxyd", proxydOpenAPIResources, mux); n == 0 {
		t.Fatal("proxyd advertises no operation — the guard checked nothing")
	}

	// Half 2 — the ownership anchor. routd owns `routes`, the wire identity
	// proxyd's live resource once drifted onto (root CLAUDE.md, 2026-07-01);
	// onbod owns onboarding. proxyd must mount neither.
	resregtest.AssertServesNoneOf(t, "proxyd", []string{"routes", "onboarding"}, mux)
}

// TestProxydCatchAllDoesNotSatisfyTheGuard is the reason the shared assertion
// compares mux PATTERNS byte-for-byte instead of asking whether some handler
// answered. proxyd mounts `/`, so every probe in this package resolves to a
// handler and a presence test would report a clean surface for a daemon that
// mounts nothing at all. This pins that the catch-all is visible AS the
// catch-all: a foreign path lands on pattern "/", which is not the pattern the
// document promises, so neither half of the guard can be fooled by it.
func TestProxydCatchAllDoesNotSatisfyTheGuard(t *testing.T) {
	s, up := testRouteServer(t, nil, "testsecret")
	defer up.Close()
	mux := s.mux()

	// A path no mount claims still gets a handler — that is the catch-all.
	h, pattern := mux.Handler(httptest.NewRequest("GET", "/v1/onboarding", nil))
	if h == nil {
		t.Fatal("catch-all absent: proxyd must serve every path")
	}
	if pattern != "/" {
		t.Fatalf("foreign path resolved to pattern %q, want the catch-all %q — "+
			"the guard distinguishes mounts from the fallback by this string", pattern, "/")
	}

	// And the owned mount is NOT the catch-all: it resolves to its own pattern,
	// which is what makes half 1 meaningful on a daemon that answers everything.
	if _, pattern := mux.Handler(httptest.NewRequest("GET", "/v1/proxyd_routes", nil)); pattern != "GET /v1/proxyd_routes" {
		t.Errorf("owned mount resolved to %q, want %q", pattern, "GET /v1/proxyd_routes")
	}
}

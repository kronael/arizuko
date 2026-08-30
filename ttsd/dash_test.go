package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The page must name the backend URL: ttsd is a thin proxy, "the voice is
// broken" is almost always the wrong backend configured, and TTS_BACKEND_URL is
// visible nowhere else an operator can reach.
func TestPanelNamesTheBackendAndItsState(t *testing.T) {
	up := backendPanel("http://kokoro:8880", true, 12*time.Millisecond)
	if !strings.Contains(up, "kokoro:8880") {
		t.Error("the panel does not name the backend")
	}
	if !strings.Contains(up, "reachable") || strings.Contains(up, "unreachable") {
		t.Error("a reachable backend did not render as reachable")
	}
	down := backendPanel("http://kokoro:8880", false, time.Second)
	if !strings.Contains(down, "unreachable") {
		t.Error("an unreachable backend did not say so")
	}
	if !strings.Contains(down, `tile err`) {
		t.Error("a down backend is not marked with the error state")
	}
}

// ttsd holds no keyset, so it cannot use the nil-KeySet gate the other daemons
// use — that form falls open, and proxyd's `user` route authenticates a person,
// not an operator. Deployed, the page is operator-only (6/28).
func TestDashboardIsOperatorOnlyWhenEnforcing(t *testing.T) {
	h := dashHandler("http://backend.invalid", true)

	r := httptest.NewRequest(http.MethodGet, "http://k.example/dash/ttsd/", nil)
	r.Header.Set("X-User-Groups", `["acme"]`)
	w := httptest.NewRecorder()
	h(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("a logged-in non-operator got %d, want 403", w.Code)
	}

	// No header at all is not an operator either.
	bare := httptest.NewRecorder()
	h(bare, httptest.NewRequest(http.MethodGet, "http://k.example/dash/ttsd/", nil))
	if bare.Code != http.StatusForbidden {
		t.Errorf("an unstamped request got %d, want 403", bare.Code)
	}
}

// With AUTHD_URL unset there is no proxyd stamping headers, so enforcing would
// lock the local-dev operator out of their own daemon.
func TestLocalDevFallsOpen(t *testing.T) {
	w := httptest.NewRecorder()
	dashHandler("http://backend.invalid", false)(w,
		httptest.NewRequest(http.MethodGet, "http://k.example/dash/ttsd/", nil))
	if w.Code != http.StatusOK {
		t.Errorf("local dev got %d, want 200", w.Code)
	}
}

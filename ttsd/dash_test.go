package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth/authtest"
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

// Deployed, the page is operator-only (6/28) — and the operator scope counts
// only behind proxyd's transit bearer. The third case is the one this gate used
// to fail: the scope alone, which anyone who could reach the port could send.
func TestDashboardIsOperatorOnlyWhenEnforcing(t *testing.T) {
	ks, bearer := authtest.Operator(t)
	h := dashHandler("http://backend.invalid", ks)

	get := func(groups, auth string) int {
		r := httptest.NewRequest(http.MethodGet, "http://k.example/dash/ttsd/", nil)
		if groups != "" {
			r.Header.Set("X-User-Groups", groups)
		}
		if auth != "" {
			r.Header.Set("Authorization", "Bearer "+auth)
		}
		w := httptest.NewRecorder()
		h(w, r)
		return w.Code
	}

	if got := get(`["acme"]`, bearer); got != http.StatusForbidden {
		t.Errorf("a logged-in non-operator got %d, want 403", got)
	}
	if got := get("", ""); got != http.StatusForbidden {
		t.Errorf("an unstamped request got %d, want 403", got)
	}
	// BUGS L270: the operator scope with no proof beside it is a forgery.
	if got := get(`["**"]`, ""); got != http.StatusForbidden {
		t.Errorf("an unproven `**` got %d, want 403", got)
	}
	if got := get(`["**"]`, bearer); got != http.StatusOK {
		t.Errorf("a proven operator got %d, want 200", got)
	}
}

// With AUTHD_URL unset there is no proxyd stamping headers, so enforcing would
// lock the local-dev operator out of their own daemon.
func TestLocalDevFallsOpen(t *testing.T) {
	w := httptest.NewRecorder()
	dashHandler("http://backend.invalid", nil)(w,
		httptest.NewRequest(http.MethodGet, "http://k.example/dash/ttsd/", nil))
	if w.Code != http.StatusOK {
		t.Errorf("local dev got %d, want 200", w.Code)
	}
}

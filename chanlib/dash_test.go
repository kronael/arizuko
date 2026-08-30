package chanlib

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// dashReq drives the shared adapter dashboard.
func dashReq(h http.HandlerFunc, groups string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "http://k.example/dash/slakd/", nil)
	if groups != "" {
		r.Header.Set("X-User-Groups", groups)
	}
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

// Every Go adapter gets its dashboard from the mux it already builds — one
// renderer, no per-adapter code (6/31). Mounting it in NewAdapterMux is what
// makes that true, so this asserts the route is there.
func TestEveryAdapterMuxServesItsDashboard(t *testing.T) {
	mux := NewAdapterMux("slakd", []string{"slack:"}, &fakeBot{},
		func() bool { return true }, func() int64 { return time.Now().Unix() }, nil)

	r := httptest.NewRequest(http.MethodGet, "http://k.example/dash/slakd/", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /dash/slakd/ = %d, want 200", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"slakd", "status", "last inbound", "/dash/services/"} {
		if !strings.Contains(body, want) {
			t.Errorf("the adapter page does not render %q", want)
		}
	}
}

// Status has THREE values. `stale` is a live process whose platform link went
// quiet — the "healthchecks green but the agent doesn't reply" case — and
// collapsing it into ok or disconnected loses what separates them.
func TestStatusDistinguishesStaleFromDownAndOk(t *testing.T) {
	fresh := adapterOverview("slakd", []string{"slack:"}, true, time.Now().Unix())
	if !strings.Contains(fresh, ">ok<") {
		t.Error("a connected adapter with fresh inbound is not ok")
	}
	old := time.Now().Add(-24 * time.Hour).Unix()
	stale := adapterOverview("slakd", []string{"slack:"}, true, old)
	if !strings.Contains(stale, ">stale<") {
		t.Error("a connected adapter with old inbound is not stale")
	}
	// Stale WARNS rather than errs: the process is up, so the next step is the
	// platform link, not a restart.
	if !strings.Contains(stale, "tile warn") {
		t.Error("stale did not render as a warning")
	}
	down := adapterOverview("slakd", []string{"slack:"}, false, old)
	if !strings.Contains(down, ">disconnected<") || !strings.Contains(down, "tile err") {
		t.Error("a disconnected adapter did not render as an error")
	}
}

// The page and /health must not disagree about when an adapter went quiet, so
// the threshold comes from the same table.
func TestStaleThresholdMatchesTheHealthEndpoint(t *testing.T) {
	for name, want := range staleThresholds {
		if got := staleThresholdFor(name); got != want {
			t.Errorf("%s: dashboard threshold %v, /health uses %v", name, got, want)
		}
	}
	if got := staleThresholdFor("no-such-adapter"); got != defaultStaleThreshold {
		t.Errorf("unknown adapter threshold = %v, want the default %v", got, defaultStaleThreshold)
	}
}

// An adapter verifies no tokens and holds no keyset, so the nil-KeySet gate the
// core daemons use would fall open here — and proxyd's `user` route
// authenticates a person, not an operator.
func TestDashboardEnforcesOperatorWhenDeployed(t *testing.T) {
	t.Setenv("AUTHD_URL", "http://authd:8080")
	h := handleAdapterDash("slakd", nil, &fakeBot{}, func() bool { return true }, func() int64 { return 0 })

	if w := dashReq(h, `["acme"]`); w.Code != http.StatusForbidden {
		t.Errorf("a logged-in non-operator got %d, want 403", w.Code)
	}
	if w := dashReq(h, ""); w.Code != http.StatusForbidden {
		t.Errorf("an unstamped request got %d, want 403", w.Code)
	}
	if w := dashReq(h, `["**"]`); w.Code != http.StatusOK {
		t.Errorf("an operator got %d, want 200", w.Code)
	}
}

// With AUTHD_URL unset nothing stamps headers, so enforcing would lock the
// local-dev operator out of their own adapter.
func TestLocalDevFallsOpen(t *testing.T) {
	t.Setenv("AUTHD_URL", "")
	h := handleAdapterDash("slakd", nil, &fakeBot{}, func() bool { return true }, func() int64 { return 0 })
	if w := dashReq(h, ""); w.Code != http.StatusOK {
		t.Errorf("local dev got %d, want 200", w.Code)
	}
}

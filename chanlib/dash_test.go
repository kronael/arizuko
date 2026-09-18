package chanlib

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth/authtest"
)

// dashReq drives the shared adapter dashboard. bearer is proxyd's transit
// proof; without it a stamped scope is a forgery and the gate must refuse it.
func dashReq(h http.HandlerFunc, groups string, bearer ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "http://k.example/dash/slakd/", nil)
	if groups != "" {
		r.Header.Set("X-User-Groups", groups)
	}
	if len(bearer) > 0 && bearer[0] != "" {
		r.Header.Set("Authorization", "Bearer "+bearer[0])
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

// Deployed, the adapter page is operator-only (6/28), and the operator scope
// counts only behind proxyd's transit bearer. Eight adapters inherit this one
// handler, so the third case below is eight surfaces at once (BUGS L270).
func TestDashboardEnforcesOperatorWhenDeployed(t *testing.T) {
	ks, bearer := authtest.Operator(t)
	h := handleAdapterDash("slakd", nil, &fakeBot{}, func() bool { return true }, func() int64 { return 0 }, ks)

	if w := dashReq(h, `["acme"]`, bearer); w.Code != http.StatusForbidden {
		t.Errorf("a logged-in non-operator got %d, want 403", w.Code)
	}
	if w := dashReq(h, ""); w.Code != http.StatusForbidden {
		t.Errorf("an unstamped request got %d, want 403", w.Code)
	}
	if w := dashReq(h, `["**"]`); w.Code != http.StatusForbidden {
		t.Errorf("an unproven `**` got %d, want 403 — the header alone is a forgery", w.Code)
	}
	if w := dashReq(h, `["**"]`, bearer); w.Code != http.StatusOK {
		t.Errorf("a proven operator got %d, want 200", w.Code)
	}
}

// With AUTHD_URL unset nothing stamps headers, so enforcing would lock the
// local-dev operator out of their own adapter.
func TestLocalDevFallsOpen(t *testing.T) {
	h := handleAdapterDash("slakd", nil, &fakeBot{}, func() bool { return true }, func() int64 { return 0 }, nil)
	if w := dashReq(h, ""); w.Code != http.StatusOK {
		t.Errorf("local dev got %d, want 200", w.Code)
	}
}

// sectionBot is a bot that contributes a delta section.
type sectionBot struct {
	fakeBot
	secs []DashSection
}

func (s *sectionBot) DashSections() []DashSection { return s.secs }

// An adapter with state the shared page cannot show contributes a SECTION, not
// a second page (6/32). The interface is optional, so an adapter that has no
// delta needs no code — that is the common case.
func TestDeltaSectionsAreOptionalAndRendered(t *testing.T) {
	plain := handleAdapterDash("slakd", nil, &fakeBot{},
		func() bool { return true }, func() int64 { return 0 }, nil)
	if body := dashReq(plain, "").Body.String(); strings.Contains(body, "<h2>Poll sources") {
		t.Error("an adapter with no DashSections rendered one")
	}

	with := handleAdapterDash("reditd", nil, &sectionBot{
		secs: []DashSection{{Title: "Poll sources and cursors", Rows: [][2]string{
			{"inbox", "t4_abc"}, {"sr:golang", "(never read)"},
		}}},
	}, func() bool { return true }, func() int64 { return 0 }, nil)
	body := dashReq(with, "").Body.String()
	for _, want := range []string{"Poll sources and cursors", "sr:golang", "(never read)", "t4_abc"} {
		if !strings.Contains(body, want) {
			t.Errorf("the delta section omits %q", want)
		}
	}
}

// An empty section still renders its title: "watching nothing" is an answer,
// and omitting the panel looks identical to the adapter not having the feature.
func TestEmptySectionStillSaysSo(t *testing.T) {
	got := renderSection(DashSection{Title: "Poll sources"})
	if !strings.Contains(got, "Poll sources") || !strings.Contains(got, "Nothing to show") {
		t.Errorf("empty section = %q, want the title and a clean-state line", got)
	}
}

// Section values are adapter state — a subreddit name is operator-supplied.
func TestSectionValuesAreEscaped(t *testing.T) {
	got := renderSection(DashSection{Title: "x", Rows: [][2]string{{"<b>k</b>", "<script>v</script>"}}})
	if strings.Contains(got, "<script>") || strings.Contains(got, "<b>k</b>") {
		t.Error("a section row rendered as live markup")
	}
}

// TestAdapterDashMountsAtTheDaemonName pins the routed path. teled runs with
// Name "telegram" and AUTHD_SERVICE_NAME "teled", and its manifest publishes
// /dash/teled/ — mounting at the platform name made the page 404 on every
// live instance while its own test passed by supplying a daemon name no
// production adapter ever sets.
func TestAdapterDashMountsAtTheDaemonName(t *testing.T) {
	t.Setenv("AUTHD_SERVICE_NAME", "teled")
	mux := NewAdapterMux("telegram", []string{"telegram:"}, &fakeBot{},
		func() bool { return true }, func() int64 { return 0 }, nil)
	for path, want := range map[string]bool{"/dash/teled/": true, "/dash/telegram/": false} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		got := w.Code != http.StatusNotFound
		if got != want {
			t.Errorf("%s: found=%v want=%v (code %d)", path, got, want, w.Code)
		}
	}
}

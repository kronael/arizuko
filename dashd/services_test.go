package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kronael/arizuko/compose"
)

// TestServicesOperator: an operator GET /dash/services/ renders one tile per
// known daemon. In-test, daemon hostnames fail DNS resolution → unknown status
// for every tile. A routed tile still links to its control plane despite the
// unknown status — an unreachable built daemon is exactly the one an operator
// wants to click through to diagnose. Unbuilt tiles always render the name as
// plain text, regardless of status.
func TestServicesOperator(t *testing.T) {
	db := routdDB(t)
	d := &dash{routd: db, routdURL: stubRoutd(t, db)}
	mux := http.NewServeMux()
	d.registerRoutes(mux)

	req := asOperator(httptest.NewRequest("GET", "/dash/services/", nil))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `class="services-grid"`) {
		t.Errorf("missing services-grid")
	}
	for _, s := range services {
		if !strings.Contains(body, s.Name) {
			t.Errorf("missing tile for %s", s.Name)
		}
		// A routed dashboard links even when the probe says unknown: an
		// unreachable daemon is exactly the one an operator wants to click
		// through to diagnose. The test server has no proxydURL, so the routed
		// set is the fallback — the core routes — which is that fallback's
		// contract, asserted here rather than assumed.
		link := fmt.Sprintf(`<a href="%s">%s</a>`, s.Dash, s.Name)
		if routed := shouldLink(compose.ReservedRoutePaths(), s); routed && !strings.Contains(body, link) {
			t.Errorf("routed daemon %s: expected link %q despite unknown status", s.Name, link)
		} else if !routed && strings.Contains(body, link) {
			t.Errorf("unrouted daemon %s: rendered link %q at a path proxyd does not carry", s.Name, link)
		}
	}
	// Unreachable via DNS → unknown (not err; err = deployed but down).
	if !strings.Contains(body, `data-status="unknown"`) {
		t.Errorf("expected unknown status for unresolvable hosts")
	}
}

// The hub's link source must cover BOTH registration paths 6/28 names, or it
// calls a whole class of dashboard a 404.
//
// This replaced TestServicesBuiltFlagMatchesMountedRoutes, which existed to
// catch drift between a hand-kept `Built` bool and the tree — it had drifted in
// five of eight rows, in both directions. The bool is gone: shouldLink reads
// the route table, so the property that test defended now holds by
// construction. What still needs asserting is that the SOURCE is complete.
func TestLinkSourceCoversBothRegistrationPaths(t *testing.T) {
	core := compose.ReservedRoutePaths()
	manifests := manifestRoutePaths(t)

	// Core daemons register in coreProxydRoutes.
	for _, want := range []string{"/dash/routd/", "/dash/runed/", "/dash/authd/", "/dash/proxyd/"} {
		if !slices.Contains(core, want) {
			t.Errorf("%s is not a core route; its tile would never link", want)
		}
	}
	// Packaged daemons register in template/services/<name>.yaml. The live
	// table proxyd serves carries both, which is why routedDashPaths asks
	// proxyd rather than reading either list alone.
	for _, want := range []string{"/dash/slakd/", "/dash/teled/"} {
		if slices.Contains(core, want) {
			t.Errorf("%s is a core route; adapters register by manifest", want)
		}
		if !slices.Contains(manifests, want) {
			t.Errorf("%s is in no service manifest; its tile would never link", want)
		}
	}
}

// The fallback is what an operator sees while proxyd is the broken thing, so it
// must never link at a path that might not exist.
func TestFallbackLinksOnlyCoreRoutes(t *testing.T) {
	d := &dash{} // no proxydURL — the fallback path
	got := d.routedDashPaths(context.Background())
	if !slices.Equal(got, compose.ReservedRoutePaths()) {
		t.Errorf("fallback returned %v, want exactly the core routes", got)
	}
	for _, s := range services {
		if shouldLink(got, s) && !slices.Contains(compose.ReservedRoutePaths(), s.Dash) {
			t.Errorf("fallback linked %s at a path it cannot vouch for", s.Name)
		}
	}
}

// TestShouldLink: the render decision is gated on Built alone — probe status
// never suppresses the link (D6: an unreachable built daemon is exactly the
// one an operator wants to click through to).
func TestShouldLink(t *testing.T) {
	cases := []struct {
		name string
		s    service
		want bool
	}{
		{"a routed dashboard", service{Name: "routd", Dash: "/dash/routd/"}, true},
		{"a path nobody registered", service{Name: "x", Dash: "/dash/nope/"}, false},
	}
	routed := compose.ReservedRoutePaths()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shouldLink(routed, c.s); got != c.want {
				t.Errorf("shouldLink(%+v) = %v, want %v", c.s, got, c.want)
			}
		})
	}
}

// TestServicesNonOperatorForbidden: the hub is operator-only.
func TestServicesNonOperatorForbidden(t *testing.T) {
	db := routdDB(t)
	d := &dash{routd: db, routdURL: stubRoutd(t, db)}
	mux := http.NewServeMux()
	d.registerRoutes(mux)

	req := httptest.NewRequest("GET", "/dash/services/", nil)
	req.Header.Set("X-User-Sub", "github:regular") // no ** → not an operator
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

func TestProbeHealthUnreachable(t *testing.T) {
	if got := probeHealth("no-such-daemon-host", ""); got != statusUnknown {
		t.Errorf("probeHealth(unresolvable) = %q, want %q", got, statusUnknown)
	}
}

func TestProbeHealthOKAndErr(t *testing.T) {
	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer okSrv.Close()
	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer errSrv.Close()

	// probeHealth pins :8080 (unreachable in-test), so exercise its real
	// classifier against arbitrary httptest URLs.
	if got := classifyHealth(http.Get(okSrv.URL)); got != statusOK {
		t.Errorf("2xx → %q, want ok", got)
	}
	if got := classifyHealth(http.Get(errSrv.URL)); got != statusErr {
		t.Errorf("503 → %q, want err", got)
	}
}

// manifestRoutePaths reads the routes packaged daemons ship in
// template/services/<name>.yaml. 6/28 names TWO registration paths — core
// daemons in coreProxydRoutes, packaged ones in a manifest — and a check that
// knows only the first calls every adapter dashboard a 404.
func manifestRoutePaths(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("../template/services/*/arizuko.yml")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, f := range files {
		routes, err := compose.ManifestRoutes(f)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for _, r := range routes {
			out = append(out, r.Path)
		}
	}
	return out
}

package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestServicesOperator: an operator GET /dash/services/ renders one tile per
// known daemon. In-test, daemon hostnames fail DNS resolution → unknown status
// for every tile. Built tiles still link to their control plane despite the
// unknown status — an unreachable built daemon is exactly the one an operator
// wants to click through to diagnose. Unbuilt tiles always render the name as
// plain text, regardless of status.
func TestServicesOperator(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	d := &dash{dbRoutd: db}
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
		link := fmt.Sprintf(`<a href="%s">%s</a>`, s.Dash, s.Name)
		if s.Built && !strings.Contains(body, link) {
			t.Errorf("built daemon %s: expected link %q despite unknown status", s.Name, link)
		}
		if !s.Built && strings.Contains(body, link) {
			t.Errorf("unbuilt daemon %s: unexpected link %q", s.Name, link)
		}
	}
	// Unreachable via DNS → unknown (not err; err = deployed but down).
	if !strings.Contains(body, `data-status="unknown"`) {
		t.Errorf("expected unknown status for unresolvable hosts")
	}
}

// Built and the mounted route must agree in BOTH directions. The flag is hand
// maintained and the route is registered somewhere else entirely, so nothing
// but this test connects them:
//
//   - Built without a route is a tile whose link 404s.
//   - A route without Built is a shipped page no operator can reach. That is
//     the direction with no other guard at all — /dash/authd/ shipped with its
//     tile still reading Built:false, and flipping it back was caught by
//     nothing until this test existed. The hub is the ONLY nav path to a
//     per-daemon cockpit (they are deliberately not in navLinks), so an
//     unflipped tile hides the page completely.
//
// Pattern matching is by prefix because dashd registers `GET /dash/` as the
// portal: an unmounted /dash/<daemon>/ still resolves, to that catch-all, so
// "did it resolve" is not the question — "did it resolve to ITS OWN route" is.
func TestServicesBuiltFlagMatchesMountedRoutes(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	mux := newMux(&dash{dbRoutd: db})

	for _, s := range services {
		_, pattern := mux.Handler(httptest.NewRequest("GET", s.Dash, nil))
		mounted := strings.Contains(pattern, s.Dash)
		if s.Built && !mounted {
			t.Errorf("%s is Built but %s resolves to %q — the hub links to a 404",
				s.Name, s.Dash, pattern)
		}
		if !s.Built && mounted {
			t.Errorf("%s serves %s but its tile is Built:false — the page ships and "+
				"the services hub, its only nav path, renders the name as plain text",
				s.Name, s.Dash)
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
		{"built", service{Name: "routd", Built: true}, true},
		{"unbuilt", service{Name: "onbod", Built: false}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shouldLink(c.s); got != c.want {
				t.Errorf("shouldLink(%+v) = %v, want %v", c.s, got, c.want)
			}
		})
	}
}

// TestServicesNonOperatorForbidden: the hub is operator-only.
func TestServicesNonOperatorForbidden(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	d := &dash{dbRoutd: db}
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

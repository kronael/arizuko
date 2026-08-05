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

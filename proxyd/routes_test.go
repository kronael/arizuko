package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMatchRoute_LongestPrefix_Wins(t *testing.T) {
	rs := []Route{
		{Path: "/api/", Backend: "http://api"},
		{Path: "/api/special/", Backend: "http://api-special"},
	}
	got := MatchRoute(rs, "/api/special/foo")
	if got == nil || got.Backend != "http://api-special" {
		t.Errorf("got = %+v, want longest-prefix /api/special/", got)
	}
	got = MatchRoute(rs, "/api/other")
	if got == nil || got.Backend != "http://api" {
		t.Errorf("got = %+v, want /api/", got)
	}
}

func TestMatchRoute_NoMatch_Returns_Nil(t *testing.T) {
	rs := []Route{{Path: "/api/", Backend: "http://api"}}
	if got := MatchRoute(rs, "/other"); got != nil {
		t.Errorf("got = %+v, want nil", got)
	}
}

func TestMatchRoute_ExactPath(t *testing.T) {
	rs := []Route{{Path: "/onboard", Backend: "http://onbod"}}
	if got := MatchRoute(rs, "/onboard"); got == nil {
		t.Error("exact-match /onboard should hit")
	}
	if got := MatchRoute(rs, "/onboard/x"); got != nil {
		t.Errorf("exact-only /onboard should not match /onboard/x, got %+v", got)
	}
}

// buildRouteProxy is exercised by the TOML-route dispatch tests in
// main_test.go (TestProxyd_TOMLRoute_*). Preserve-headers and strip-prefix
// behaviour are covered there end-to-end via server.route.

func TestBuildRouteProxy_BadURL_ReturnsNil(t *testing.T) {
	if rp := buildRouteProxy(Route{Path: "/x/", Backend: ":://bad"}); rp != nil {
		t.Errorf("expected nil for invalid backend, got %v", rp)
	}
}

// Sanity: a route's ReverseProxy reaches its declared backend.
func TestBuildRouteProxy_Reaches_Backend(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(204)
	}))
	defer up.Close()
	rp := buildRouteProxy(Route{Path: "/x/", Backend: up.URL})
	req := httptest.NewRequest("GET", "/x/hi", nil)
	w := httptest.NewRecorder()
	rp.ServeHTTP(w, req)
	if w.Code != 204 {
		t.Errorf("status = %d, want 204", w.Code)
	}
}

// TestBuildRouteProxy_ForwardsHostPathAndHeaders pins what one route's proxy puts on
// the wire. It exists because `ReverseProxy.Director` is deprecated and `Rewrite`
// does NOT carry the same defaults: Rewrite adds no `X-Forwarded-For` unless the hook
// asks, and takes the Host from the rewritten URL rather than the inbound request. On
// the one daemon that stamps identity for every backend, that difference has to be a
// measured fact rather than a reading of the documentation.
func TestBuildRouteProxy_ForwardsHostPathAndHeaders(t *testing.T) {
	type seen struct {
		host, path, xff, keep, drop string
	}
	var got seen
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, rq *http.Request) {
		got = seen{
			host: rq.Host,
			path: rq.URL.Path,
			xff:  rq.Header.Get("X-Forwarded-For"),
			keep: rq.Header.Get("X-User-Groups"),
			drop: rq.Header.Get("Connection"),
		}
		w.WriteHeader(204)
	}))
	defer up.Close()
	backend := strings.TrimPrefix(up.URL, "http://")

	rp := buildRouteProxy(Route{
		Path: "/dav/", Backend: up.URL, StripPrefix: true,
		PreserveHeaders: []string{"X-User-Groups"},
	})
	req := httptest.NewRequest("GET", "/dav/sub/page", nil)
	req.Host = "krons.example.com"
	req.RemoteAddr = "203.0.113.9:4444"
	req.Header.Set("X-User-Groups", `["solo/inbox"]`)
	// A hop-by-hop header must not reach the backend whichever hook runs.
	req.Header.Set("Connection", "keep-alive")
	rp.ServeHTTP(httptest.NewRecorder(), req)

	// The Host is the BACKEND's, never the inbound one: proxyd is not transparent,
	// and a backend that sees krons.example.com answers for the wrong site.
	if got.host != backend {
		t.Errorf("Host = %q, want the backend %q", got.host, backend)
	}
	if got.path != "/sub/page" {
		t.Errorf("path = %q, want the prefix stripped", got.path)
	}
	// X-Forwarded-For is what lets a backend see the real client at all, and
	// TRUSTED_PROXIES is written against it (BUGS `L40`).
	if !strings.Contains(got.xff, "203.0.113.9") {
		t.Errorf("X-Forwarded-For = %q, want the client address", got.xff)
	}
	if got.keep != `["solo/inbox"]` {
		t.Errorf("preserved header = %q, want it intact", got.keep)
	}
	if got.drop != "" {
		t.Errorf("Connection reached the backend as %q; hop-by-hop headers must not", got.drop)
	}
}

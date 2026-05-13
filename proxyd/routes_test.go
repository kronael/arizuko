package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLoadRoutes_EmptyEnv_ReturnsEmpty(t *testing.T) {
	rs, err := LoadRoutes("")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(rs) != 0 {
		t.Errorf("routes = %v, want empty", rs)
	}
}

func TestLoadRoutes_ValidJSON_ParsesAll(t *testing.T) {
	in := `[
	  {"path":"/slack/","backend":"http://slakd:8080","auth":"public",
	   "preserve_headers":["X-Slack-Signature"]},
	  {"path":"/dash/","backend":"http://dashd:8080","auth":"user"},
	  {"path":"/dav/","backend":"http://davd:8080","auth":"user",
	   "strip_prefix":true,"gated_by":"WEBDAV_ENABLED"}
	]`
	rs, err := LoadRoutes(in)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(rs) != 3 {
		t.Fatalf("len = %d, want 3", len(rs))
	}
	if rs[0].Path != "/slack/" || rs[0].Backend != "http://slakd:8080" ||
		rs[0].Auth != "public" || len(rs[0].PreserveHeaders) != 1 {
		t.Errorf("route[0] = %+v", rs[0])
	}
	if !rs[2].StripPrefix || rs[2].GatedBy != "WEBDAV_ENABLED" {
		t.Errorf("route[2] = %+v", rs[2])
	}
}

func TestLoadRoutes_BadJSON_Errors(t *testing.T) {
	if _, err := LoadRoutes("{not json"); err == nil {
		t.Error("expected error on malformed JSON")
	}
}

func TestLoadRoutes_Validation(t *testing.T) {
	cases := []struct {
		name, json string
	}{
		{"no_leading_slash", `[{"path":"slack/","backend":"http://x","auth":"public"}]`},
		{"empty_backend", `[{"path":"/x/","backend":"","auth":"public"}]`},
		{"unknown_auth", `[{"path":"/x/","backend":"http://x","auth":"admin"}]`},
		{"missing_auth", `[{"path":"/x/","backend":"http://x"}]`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := LoadRoutes(c.json); err == nil {
				t.Errorf("expected validation error for %s", c.name)
			}
		})
	}
}

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

package api

// Per-daemon integration check: /openapi.json on gated parses as JSON
// and lists every owned resource path. Drift between catalog + emit is
// caught here (vs. resreg/openapi_test which exercises the engine in
// isolation against a synthetic struct).

import (
	"encoding/json"
	"testing"
)

func TestOpenAPI_Gated(t *testing.T) {
	srv, _, _ := setup(t)
	w := getJSON(srv.Handler(), "/openapi.json", "")
	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("content-type = %q, want application/json", got)
	}
	var doc map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, w.Body.String())
	}
	if doc["openapi"] != "3.1.0" {
		t.Errorf("openapi = %v, want 3.1.0", doc["openapi"])
	}
	paths := doc["paths"].(map[string]any)
	for _, want := range []string{
		"/v1/groups", "/v1/acl", "/v1/acl_membership", "/v1/routes",
		"/v1/web_routes", "/v1/scheduled_tasks", "/v1/secrets",
		"/v1/network_rules",
	} {
		if _, ok := paths[want]; !ok {
			t.Errorf("paths missing %s", want)
		}
	}
	// Public — no auth header used; if it were gated, this test would 401.
}

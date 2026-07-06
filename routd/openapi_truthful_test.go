package routd

// Spec 5/44 #32: the engine-generated OpenAPI mirrors each resource's REAL
// mounted REST endpoints (single-sourced from resreg/resources), not the PK-CRUD
// convention — so the published doc carries no phantom paths (a PATCH /{pk} or a
// resource-name path no handler serves).

import (
	"encoding/json"
	"testing"

	"github.com/kronael/arizuko/resreg"
	_ "github.com/kronael/arizuko/resreg/resources" // registers the resources
)

func TestOpenAPITruthful(t *testing.T) {
	// routes: real face is POST add / PUT set / DELETE by {id} / GET list — the
	// PK convention would instead advertise PATCH+DELETE /v1/routes/{seq}.
	rp := openAPIPaths(t, "routd", "routes")
	col := mustPath(t, rp, "/v1/routes")
	for _, m := range []string{"get", "post", "put"} {
		if _, ok := col[m]; !ok {
			t.Errorf("/v1/routes missing real %s op", m)
		}
	}
	if _, bad := col["patch"]; bad {
		t.Error("phantom PATCH /v1/routes leaked (no such handler)")
	}
	if _, ok := mustPath(t, rp, "/v1/routes/{id}")["delete"]; !ok {
		t.Error("/v1/routes/{id} missing real delete")
	}
	if _, bad := rp["/v1/routes/{seq}"]; bad {
		t.Error("phantom PK-convention path /v1/routes/{seq} leaked into the doc")
	}

	// onboarding_gates is served at /v1/gates (onbod), NOT the resource-name path.
	gp := openAPIPaths(t, "onbod", "onboarding_gates")
	if _, ok := gp["/v1/gates"]; !ok {
		t.Errorf("onboarding_gates doc missing its real /v1/gates: %v", gp)
	}
	if _, bad := gp["/v1/onboarding_gates"]; bad {
		t.Error("phantom /v1/onboarding_gates (resource-name convention) leaked")
	}
}

func openAPIPaths(t *testing.T, daemon, resource string) map[string]any {
	t.Helper()
	out, err := resreg.OpenAPI(daemon, "/", []string{resource})
	if err != nil {
		t.Fatalf("OpenAPI(%s): %v", resource, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("parse OpenAPI(%s): %v", resource, err)
	}
	return doc["paths"].(map[string]any)
}

func mustPath(t *testing.T, paths map[string]any, key string) map[string]any {
	t.Helper()
	p, ok := paths[key].(map[string]any)
	if !ok {
		t.Fatalf("missing path %s: %v", key, paths)
	}
	return p
}

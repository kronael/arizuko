package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore/dynstore/dyntest"
	"github.com/kronael/arizuko/tests/testutils"
)

func TestMain(m *testing.M) { dyntest.Main(m) }

// authd mints the JWT sub prefixed ("user:alice"), and proxyd stamps that claim
// into X-User-Sub verbatim. acl.principal and acl_membership.child store BARE
// subs, so carrying the prefix into the Caller matched no row and no wildcard
// row: every proxyd_routes action was denied for every operator (BUGS L100).
//
// The existing resource tests seed Principal "**", which matchPrincipal admits
// for anything, so they could not see this. This one seeds a NAMED principal.
func TestCallerSubIsBareSoANamedACLRowMatches(t *testing.T) {
	st := testutils.NewRoutdStore(t)
	if err := st.AddACLRow(core.ACLRow{
		Principal: "alice", Action: "*", Scope: "**", Effect: "allow",
	}); err != nil {
		t.Fatal(err)
	}
	ks, _, tok := webdBearerKS(t)
	build := callerFromHTTP(ks)

	req := httptest.NewRequest(http.MethodGet, "/v1/proxyd_routes", nil)
	req.Header.Set("X-User-Sub", "user:alice")
	req.Header.Set("Authorization", "Bearer "+tok)
	c, err := build(req)
	if err != nil {
		t.Fatalf("callerFromHTTP: %v", err)
	}
	if c.Sub != "alice" {
		t.Fatalf("Caller.Sub = %q, want the bare sub the acl rows store", c.Sub)
	}
	if !auth.Authorize(st, auth.Caller{Principal: c.Sub}, "proxyd_routes:list", "**", nil) {
		t.Error("a named acl row did not admit the caller; the principal did not match")
	}
	// A service principal is stripped the same way.
	req2 := httptest.NewRequest(http.MethodGet, "/v1/proxyd_routes", nil)
	req2.Header.Set("X-User-Sub", "service:webd")
	req2.Header.Set("Authorization", "Bearer "+tok)
	c2, err := build(req2)
	if err != nil {
		t.Fatal(err)
	}
	if c2.Sub != "webd" {
		t.Errorf("Caller.Sub = %q, want \"webd\"", c2.Sub)
	}
}

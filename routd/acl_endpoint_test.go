package routd

// REST-face tests for the spec 5/16 acl fold: POST /v1/acl (add) and body-DELETE
// /v1/acl (remove) now ride the SAME shared aclHandler the agent's
// add_acl/remove_acl MCP tools use, via resreg.RegisterREST + the injected
// aclRESTCaller/aclRESTGate. The *Endpoint tests below are the operator/root
// parity cases (empty JWT folder = tier-0, unrestricted); the *Containment tests
// are the security tightening — a folder-scoped caller is now bound to its own
// authority, closing the pre-fold hole where the REST face gated on the acl:write
// bearer scope ALONE and never bound the body scope. list_acl has no REST twin.

import (
	"testing"

	"github.com/kronael/arizuko/core"
)

// aclReq is the /v1/acl POST(add)/DELETE(remove) body after the fold: the shared
// handler reads the flat MCP arg names (principal/scope[/action/effect]). Replaces
// the retired aclWriteBody — granted_by is no longer a client arg (the handler
// stamps provenance server-side from the caller's folder).
type aclReq struct {
	Principal string `json:"principal"`
	Scope     string `json:"scope"`
	Action    string `json:"action,omitempty"`
	Effect    string `json:"effect,omitempty"`
}

// TestACLAddEndpoint: POST /v1/acl (acl:write, root) writes a row into routd's OWN
// routd.db; ListACL/UserScopes read it back — proving the write lands where the
// scope snapshot looks.
func TestACLAddEndpoint(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "service:dashd", scope: []string{"acl:write"}})

	rec := doJSON(t, h, "POST", "/v1/acl", "", aclReq{
		Principal: "github:7", Scope: "main/eng"})
	if rec.Code != 200 {
		t.Fatalf("POST /v1/acl = %d want 200 body=%s", rec.Code, rec.Body.String())
	}

	rows := db.ListACL("github:7")
	if len(rows) != 1 || rows[0].Scope != "main/eng" || rows[0].Action != "admin" || rows[0].Effect != "allow" {
		t.Fatalf("ListACL after POST = %+v", rows)
	}
	if got := db.UserScopes("github:7"); len(got) != 1 || got[0] != "main/eng" {
		t.Errorf("UserScopes = %v, want [main/eng]", got)
	}
}

// TestACLAddOperatorEndpoint: scope "**" maps to role:operator membership (the
// same semantic the CLI uses), reachable via the closure walk.
func TestACLAddOperatorEndpoint(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "service:dashd", scope: []string{"acl:write"}})

	if rec := doJSON(t, h, "POST", "/v1/acl", "", aclReq{
		Principal: "github:9", Scope: "**"}); rec.Code != 200 {
		t.Fatalf("operator POST = %d body=%s", rec.Code, rec.Body.String())
	}
	st := db.aclEval()
	found := false
	for _, a := range st.Ancestors("github:9") {
		if a == "role:operator" {
			found = true
		}
	}
	if !found {
		t.Fatalf("github:9 not a member of role:operator after POST **")
	}
}

// TestACLRemoveEndpoint: DELETE /v1/acl (root) removes a previously-written row.
func TestACLRemoveEndpoint(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "service:dashd", scope: []string{"acl:write"}})

	if err := db.AddACLRow(core.ACLRow{
		Principal: "u1", Action: "admin", Scope: "main", Effect: "allow", GrantedBy: "seed"}); err != nil {
		t.Fatal(err)
	}
	if err := db.AddMembership("u1", "role:operator", "seed"); err != nil {
		t.Fatal(err)
	}

	if rec := doJSON(t, h, "DELETE", "/v1/acl", "", aclReq{
		Principal: "u1", Scope: "main"}); rec.Code != 200 {
		t.Fatalf("DELETE row = %d body=%s", rec.Code, rec.Body.String())
	}
	if rows := db.ListACL("u1"); len(rows) != 0 {
		t.Fatalf("acl rows after DELETE = %d, want 0", len(rows))
	}

	if rec := doJSON(t, h, "DELETE", "/v1/acl", "", aclReq{
		Principal: "u1", Scope: "**"}); rec.Code != 200 {
		t.Fatalf("DELETE operator = %d body=%s", rec.Code, rec.Body.String())
	}
	for _, a := range db.aclEval().Ancestors("u1") {
		if a == "role:operator" {
			t.Fatal("u1 still member of role:operator after DELETE **")
		}
	}
}

// TestACLEndpointRequiresScope: a token without acl:write is denied (403).
func TestACLEndpointRequiresScope(t *testing.T) {
	_, h := authSrv(t, fakeVerifier{sub: "user:nobody", scope: []string{"chats:read"}})
	if rec := doJSON(t, h, "POST", "/v1/acl", "", aclReq{
		Principal: "x", Scope: "y"}); rec.Code != 403 {
		t.Fatalf("POST /v1/acl without scope = %d want 403", rec.Code)
	}
}

// TestACLEndpointMissingFields: principal+scope are required (400).
func TestACLEndpointMissingFields(t *testing.T) {
	_, h := authSrv(t, fakeVerifier{sub: "service:dashd", scope: []string{"acl:write"}})
	if rec := doJSON(t, h, "POST", "/v1/acl", "", aclReq{Principal: "x"}); rec.Code != 400 {
		t.Fatalf("POST without scope = %d want 400", rec.Code)
	}
}

// TestACLRESTContainmentDenied is the security fix (spec 5/16). The folded REST
// Gate re-runs the MCP scope-containment, so a folder-scoped caller may grant only
// WITHIN its own authority. A tier-1 caller ("world/a") grants its own world
// (allowed, row written) but is DENIED a cross-world scope AND "**" — closing the
// pre-fold hole where the REST face gated on acl:write ALONE. FAILS (the cross-
// world POST returns 200 + writes a row) if aclRESTGate's AuthorizeStructural is
// dropped — the REST twin of TestACLMCP_ContainmentDenied.
func TestACLRESTContainmentDenied(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "user:wa", scope: []string{"acl:write"}, folder: "world/a"})

	// Own world: allowed, row written.
	if rec := doJSON(t, h, "POST", "/v1/acl", "", aclReq{
		Principal: "google:x", Scope: "world/b", Action: "read"}); rec.Code != 200 {
		t.Fatalf("own-world POST = %d want 200 body=%s", rec.Code, rec.Body.String())
	}
	if aclRowCount(t, db, "world/b") != 1 {
		t.Fatalf("own-world POST did not write a row (world/b rows=%d)", aclRowCount(t, db, "world/b"))
	}
	// Cross-world: DENIED (403), nothing written — proves the hole is closed.
	if rec := doJSON(t, h, "POST", "/v1/acl", "", aclReq{
		Principal: "google:x", Scope: "other/x", Action: "read"}); rec.Code != 403 {
		t.Fatalf("cross-world POST = %d want 403 body=%s", rec.Code, rec.Body.String())
	}
	if aclRowCount(t, db, "other/x") != 0 {
		t.Fatal("denied cross-world POST still wrote a row")
	}
	// "**" operator role: DENIED (a tier-1 folder does not own **).
	if rec := doJSON(t, h, "POST", "/v1/acl", "", aclReq{
		Principal: "google:x", Scope: "**"}); rec.Code != 403 {
		t.Fatalf("** POST by non-root = %d want 403 body=%s", rec.Code, rec.Body.String())
	}
	if operatorMembershipExists(t, db, "google:x") {
		t.Fatal("denied ** POST still wrote an operator membership edge")
	}
}

// TestACLRESTOperatorRootOnly: scope "**" is granted by root (tier-0, empty JWT
// folder) — a role:operator membership edge, not an acl row — and revoked the same
// way. Mirrors the MCP "**" overload over REST; the non-root denial is covered by
// TestACLRESTContainmentDenied.
func TestACLRESTOperatorRootOnly(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "service:dashd", scope: []string{"acl:write"}, folder: ""})

	if rec := doJSON(t, h, "POST", "/v1/acl", "", aclReq{
		Principal: "google:op", Scope: "**"}); rec.Code != 200 {
		t.Fatalf("root ** POST = %d want 200 body=%s", rec.Code, rec.Body.String())
	}
	if !operatorMembershipExists(t, db, "google:op") {
		t.Fatal("root ** POST did not write an operator membership edge")
	}
	if aclRowCount(t, db, "**") != 0 {
		t.Fatal("root ** POST wrote an acl row instead of a membership edge")
	}
	if rec := doJSON(t, h, "DELETE", "/v1/acl", "", aclReq{
		Principal: "google:op", Scope: "**"}); rec.Code != 200 {
		t.Fatalf("root ** DELETE = %d want 200 body=%s", rec.Code, rec.Body.String())
	}
	if operatorMembershipExists(t, db, "google:op") {
		t.Fatal("root ** DELETE did not drop the membership edge")
	}
}

// TestACLRESTDeleteContainmentDenied: DELETE mirrors POST — a tier-1 caller is
// denied a cross-world revoke, so the pre-seeded cross-world row survives.
func TestACLRESTDeleteContainmentDenied(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "user:wa", scope: []string{"acl:write"}, folder: "world/a"})
	if err := db.AddACLRow(core.ACLRow{
		Principal: "google:x", Action: "admin", Scope: "other/x", Effect: "allow", GrantedBy: "seed"}); err != nil {
		t.Fatal(err)
	}
	if rec := doJSON(t, h, "DELETE", "/v1/acl", "", aclReq{
		Principal: "google:x", Scope: "other/x"}); rec.Code != 403 {
		t.Fatalf("cross-world DELETE = %d want 403 body=%s", rec.Code, rec.Body.String())
	}
	if aclRowCount(t, db, "other/x") != 1 {
		t.Fatal("denied cross-world DELETE still dropped the row")
	}
}

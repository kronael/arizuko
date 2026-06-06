package routd

import (
	"testing"

	"github.com/kronael/arizuko/core"
)

// TestACLAddEndpoint: POST /v1/acl (acl:write) writes a row into routd's OWN
// routd.db; ListACL/UserScopes read it back — proving the write lands where the
// scope snapshot looks.
func TestACLAddEndpoint(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "service:dashd", scope: []string{"acl:write"}})

	rec := doJSON(t, h, "POST", "/v1/acl", "", aclWriteBody{
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

	if rec := doJSON(t, h, "POST", "/v1/acl", "", aclWriteBody{
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

// TestACLRemoveEndpoint: DELETE /v1/acl removes a previously-written row.
func TestACLRemoveEndpoint(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "service:dashd", scope: []string{"acl:write"}})

	if err := db.AddACLRow(core.ACLRow{
		Principal: "u1", Action: "admin", Scope: "main", Effect: "allow", GrantedBy: "seed"}); err != nil {
		t.Fatal(err)
	}
	if err := db.AddMembership("u1", "role:operator", "seed"); err != nil {
		t.Fatal(err)
	}

	if rec := doJSON(t, h, "DELETE", "/v1/acl", "", aclWriteBody{
		Principal: "u1", Scope: "main"}); rec.Code != 200 {
		t.Fatalf("DELETE row = %d body=%s", rec.Code, rec.Body.String())
	}
	if rows := db.ListACL("u1"); len(rows) != 0 {
		t.Fatalf("acl rows after DELETE = %d, want 0", len(rows))
	}

	if rec := doJSON(t, h, "DELETE", "/v1/acl", "", aclWriteBody{
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
	if rec := doJSON(t, h, "POST", "/v1/acl", "", aclWriteBody{
		Principal: "x", Scope: "y"}); rec.Code != 403 {
		t.Fatalf("POST /v1/acl without scope = %d want 403", rec.Code)
	}
}

// TestACLEndpointMissingFields: principal+scope are required (400).
func TestACLEndpointMissingFields(t *testing.T) {
	_, h := authSrv(t, fakeVerifier{sub: "service:dashd", scope: []string{"acl:write"}})
	if rec := doJSON(t, h, "POST", "/v1/acl", "", aclWriteBody{Principal: "x"}); rec.Code != 400 {
		t.Fatalf("POST without scope = %d want 400", rec.Code)
	}
}

package routd

// BUGS L658: the REST `acl` add must pass auth.Delegate like the MCP add, so a
// folder-scoped writer cannot grant what it does not hold WITH GRANT OPTION.

import (
	"testing"

	"github.com/kronael/arizuko/core"
)

func TestACLRESTAddDelegates(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "user:wa", scope: []string{"acl:write"}, folder: "world/a"})

	// Holds nothing: inside its own subtree, but not delegable.
	if rec := doJSON(t, h, "POST", "/v1/acl", "", aclReq{
		Principal: "google:x", Scope: "world/a/team", Action: "admin"}); rec.Code != 403 {
		t.Fatalf("grant of an unheld row = %d want 403 body=%s", rec.Code, rec.Body.String())
	}
	if aclRowCount(t, db, "world/a/team") != 0 {
		t.Fatal("refused POST still wrote a row")
	}

	// Holds a narrower action WITH the option: a broader one is refused.
	if err := addACLRow(db, core.ACLRow{Principal: "user:wa", Action: "mcp:fetch_history",
		Scope: "world/a/**", Effect: "allow", GrantOption: true, GrantedBy: "seed"}); err != nil {
		t.Fatal(err)
	}
	if rec := doJSON(t, h, "POST", "/v1/acl", "", aclReq{
		Principal: "google:x", Scope: "world/a/team", Action: "admin"}); rec.Code != 403 {
		t.Fatalf("broader-than-held grant = %d want 403 body=%s", rec.Code, rec.Body.String())
	}
	// The held row itself is delegable.
	if rec := doJSON(t, h, "POST", "/v1/acl", "", aclReq{
		Principal: "google:x", Scope: "world/a/team", Action: "mcp:fetch_history"}); rec.Code != 200 {
		t.Fatalf("subset grant = %d want 200 body=%s", rec.Code, rec.Body.String())
	}
}

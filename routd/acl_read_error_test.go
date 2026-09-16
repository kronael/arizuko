package routd

import (
	"context"
	"strings"
	"testing"

	"github.com/kronael/arizuko/objstore/dynstore"
	"github.com/kronael/arizuko/store"
)

// dropACL takes the acl entity away from the store under test. The store is a
// network service, so an unreadable policy is an ordinary condition — an
// endpoint that is down, a key whose policy no longer covers the table — and a
// missing entity is the one a test can make deterministic.
func dropACL(t *testing.T, db *DB) {
	t.Helper()
	d, ok := db.Obj().(*dynstore.Store)
	if !ok {
		t.Fatalf("this fixture is not served: %T", db.Obj())
	}
	if err := d.DropEntity(context.Background(), store.ACL); err != nil {
		t.Fatalf("drop acl: %v", err)
	}
}

// BUGS A4: a management surface must not answer 200 with an empty body for a
// policy it could not read. "This folder has no grants" and "the acl table is
// unreadable" are opposite facts, and an operator acting on the first when the
// second is true will happily re-grant what is already denied.
func TestUserScopesEndpointReportsAReadFailure(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "service:authd", scope: []string{"grants:read", "acl:write"}})

	rec := doJSON(t, h, "POST", "/v1/acl", "", aclReq{Principal: "github:7", Scope: "main/eng"})
	if rec.Code != 200 {
		t.Fatalf("seed grant: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, h, "GET", "/v1/users/github:7/scopes", "", nil)
	if rec.Code != 200 {
		t.Fatalf("fixture is vacuous: scopes = %d want 200", rec.Code)
	}

	dropACL(t, db)

	rec = doJSON(t, h, "GET", "/v1/users/github:7/scopes", "", nil)
	if rec.Code == 404 {
		t.Fatal("an unreadable acl answered 404 no_grants: authd would snapshot that as a legitimately scopeless user")
	}
	if rec.Code != 500 {
		t.Errorf("scopes on an unreadable store = %d, want 500; body=%s", rec.Code, rec.Body.String())
	}
}

// The same rule for the acl LIST: an empty `acl` array is a claim about the
// folder, not about the store.
func TestListACLToolReportsAReadFailure(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	dropACL(t, db)

	if _, err := db.ListACL(""); err == nil {
		t.Fatal("ListACL on an unreadable store returned no error")
	} else if !strings.Contains(err.Error(), "acl") {
		t.Errorf("error should name the acl read, got: %v", err)
	}
}

package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// proxyd reads routd's `acl` directly, per request, instead of calling routd's
// HTTP face. Spec 5/16 § "The hot-path exception" decides that stays. This test
// pins the property the decision rests on, so the rejected design fails a test
// instead of passing review. (`route_tokens` was the other hot-path read;
// specs/6/22 step 1 moved that resolution to webd.)

// A grant revoked in `acl` is denied on the very next request, even though the
// caller's still-valid JWT carries the folder in its login-time Scope claim.
//
// This is the whole of the trade-off spec 5/16 § "The hot-path exception"
// weighed: swapping the per-request `auth.UserScopes(s.stRoutd, …)` read for
// `sub.Scope` would serve this request from the token's stale snapshot and
// return 200, leaving a revoked grant live until the token refreshes
// (accessTTL, 15 minutes).
func TestGroupsForSub_RevokedGrantDeniedDespiteTokenScope(t *testing.T) {
	st, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateAuthUser("local:dave", "dave", "Dave"); err != nil {
		t.Fatal(err)
	}
	row := core.ACLRow{Principal: "local:dave", Action: "*", Scope: "workspace-z", Effect: "allow"}
	if err := st.PutACLRow(row); err != nil {
		t.Fatal(err)
	}

	s, up, k := davES256Server(t, st)
	defer up.Close()

	// Minted while the grant was live: authd snapshots the scope list into
	// the token (authd/oauth.go issueSession -> TokenClaims{Scope: scope}).
	tok, err := k.Sign(auth.TokenClaims{
		Sub: "local:dave", Typ: "user", Scope: []string{"workspace-z"},
	}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	// Operator revokes. The token is untouched and still valid.
	if err := st.RemoveACLRow(row); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/dav/workspace-z/file.txt", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.route(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: the acl row is gone, so the revocation "+
			"must bite now. A 200 means groups came from the token's stale Scope "+
			"claim, not from routd.db (spec 5/16 § The hot-path exception)", w.Code)
	}
}

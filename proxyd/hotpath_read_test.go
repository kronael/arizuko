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

// proxyd reads routd's `acl` and `route_tokens` directly, per request, instead
// of calling routd's HTTP face. Spec 5/16 § "The hot-path exception" decides
// that stays. These two tests pin the property the decision rests on, so the
// rejected design fails a test instead of passing review. Each is the only
// test in the package that fails for its own bug — the rest of proxyd's
// DB-lookup coverage passes both variants.

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

// An unknown token stamps nothing: the folder comes from the row, so a token
// with no row must not yield a folder. Guards the inverse of the test above —
// without it, a handler that stamped X-Folder from the URL would pass.
func TestDispatchRouteToken_UnknownTokenStampsNoFolder(t *testing.T) {
	st, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Seen-Folder", r.Header.Get("X-Folder"))
		w.WriteHeader(200)
	}))
	defer up.Close()

	s := &server{
		cfg:         config{},
		stRoutd:     st,
		chatAnonDOS: newRateLimiter(10, time.Minute),
		rr:          newRoutesResource(nil, []Route{{Path: "/chat/", Backend: up.URL, Auth: "public"}}),
	}

	req := httptest.NewRequest("GET", "/chat/nosuchtoken/", nil)
	req.RemoteAddr = "10.0.0.8:2222"
	w := httptest.NewRecorder()
	s.route(w, req)

	if got := w.Header().Get("X-Seen-Folder"); got != "" {
		t.Errorf("X-Folder = %q, want empty for an unknown token", got)
	}
}

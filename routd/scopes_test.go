package routd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

// getJSON issues a GET against h and returns the recorder.
func getJSON(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestUserScopesEndpoint: GET /v1/users/{sub}/scopes returns the scopes the sub
// holds against routd's OWN acl table (the empty-scope bug fix — authd's
// FetchGrants now reaches a real server). A grants:read token is required.
func TestUserScopesEndpoint(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "service:authd", scope: []string{"grants:read"}})
	addACL(t, db, "user:alice", "admin", "atlas/main", "allow")

	rec := getJSON(t, h, "/v1/users/user:alice/scopes")
	if rec.Code != 200 {
		t.Fatalf("scopes = %d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Scope  []string `json:"scope"`
		Folder string   `json:"folder"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if !slices.Equal(out.Scope, []string{"atlas/main"}) {
		t.Errorf("scope = %v want [atlas/main]", out.Scope)
	}
	// Single scope → that scope is the folder bound.
	if out.Folder != "atlas/main" {
		t.Errorf("folder = %q want atlas/main", out.Folder)
	}
}

// TestUserScopesEndpoint_Membership: a sub reaches scopes via acl_membership
// (e.g. role:operator's `**`) — UserScopes expands membership transitively.
func TestUserScopesEndpoint_Membership(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "service:authd", scope: []string{"grants:read"}})
	addACL(t, db, "role:operator", "*", "**", "allow")
	// membership edge: alice IS-A operator → inherits the `**` scope. Raw INSERT
	// (not store.AddMembership) to avoid the audit_log dependency.
	if _, err := db.SQL().Exec(
		`INSERT INTO acl_membership(child, parent, added_at) VALUES(?,?,?)`,
		"user:alice", "role:operator", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}

	rec := getJSON(t, h, "/v1/users/user:alice/scopes")
	if rec.Code != 200 {
		t.Fatalf("scopes = %d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Scope  []string `json:"scope"`
		Folder string   `json:"folder"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if !slices.Contains(out.Scope, "**") {
		t.Errorf("operator scope = %v want to contain **", out.Scope)
	}
}

// TestUserScopesEndpoint_NoGrants: a sub with no acl rows → 404 no_grants, which
// authd's FetchGrants maps to ErrNoGrants (authenticated-but-unauthorized).
func TestUserScopesEndpoint_NoGrants(t *testing.T) {
	_, h := authSrv(t, fakeVerifier{sub: "service:authd", scope: []string{"grants:read"}})

	rec := getJSON(t, h, "/v1/users/user:nobody/scopes")
	if rec.Code != 404 {
		t.Fatalf("no-grant sub = %d want 404 body=%s", rec.Code, rec.Body.String())
	}
	var e struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &e)
	if e.Error != "no_grants" {
		t.Errorf("error = %q want no_grants", e.Error)
	}
}

// TestUserScopesEndpoint_RequiresGrantsRead: a token without grants:read is 403.
func TestUserScopesEndpoint_RequiresGrantsRead(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "user:u", scope: []string{"routes:read"}})
	addACL(t, db, "user:alice", "admin", "atlas/main", "allow")

	rec := getJSON(t, h, "/v1/users/user:alice/scopes")
	if rec.Code != 403 {
		t.Fatalf("scopes without grants:read = %d want 403 body=%s", rec.Code, rec.Body.String())
	}
}

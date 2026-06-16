package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
	"github.com/kronael/arizuko/tests/testutils"
)

// chatTestDash builds a dashd over a migrated instance DB and a mux. The
// caller seeds groups/messages/tokens against inst.DB.
func chatTestDash(t *testing.T) (*testutils.Inst, *http.ServeMux) {
	t.Helper()
	inst := testutils.NewInstance(t)
	d := &dash{db: inst.DB, dbRW: inst.DB, dbRoutd: inst.DB, dbOnbod: inst.DB, groupsDir: t.TempDir()}
	mux := http.NewServeMux()
	d.registerRoutes(mux)
	return inst, mux
}

func addGroup(t *testing.T, inst *testutils.Inst, folder string) {
	t.Helper()
	if _, err := inst.DB.Exec(
		`INSERT INTO groups (folder, added_at) VALUES (?, ?)`,
		folder, time.Now().Format(time.RFC3339)); err != nil {
		t.Fatalf("add group %q: %v", folder, err)
	}
}

func TestHandleChatPortal_empty(t *testing.T) {
	_, mux := chatTestDash(t)

	req := asOperator(httptest.NewRequest("GET", "/dash/chat/", nil))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "No groups available") {
		t.Errorf("empty state missing: %q", body)
	}
}

func TestHandleChatPortal_groups(t *testing.T) {
	inst, mux := chatTestDash(t)
	addGroup(t, inst, "alpha")
	addGroup(t, inst, "bravo")

	req := asOperator(httptest.NewRequest("GET", "/dash/chat/", nil))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	for _, f := range []string{"alpha", "bravo"} {
		if !strings.Contains(body, f) {
			t.Errorf("group %q missing from portal: %q", f, body)
		}
		if !strings.Contains(body, `/dash/chat/`+f+`/`) {
			t.Errorf("chat link for %q missing", f)
		}
	}
}

// A non-member caller (no grant on the folder, not operator) gets 403 on the
// group page.
func TestHandleChatGroup_access(t *testing.T) {
	inst, mux := chatTestDash(t)
	addGroup(t, inst, "secret")

	req := httptest.NewRequest("GET", "/dash/chat/secret/", nil)
	req.Header.Set("X-User-Sub", "stranger@x")
	req.Header.Set("X-User-Groups", `["other"]`)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

func TestHandleChatGroup_renders(t *testing.T) {
	inst, mux := chatTestDash(t)
	folder := "eng"
	addGroup(t, inst, folder)
	// An existing chat session (web: route token) for this folder.
	if err := inst.Store.InsertRouteToken(store.GenRouteToken(), store.RouteToken{
		JID: "web:" + folder, OwnerFolder: folder,
	}); err != nil {
		t.Fatal(err)
	}

	req := asOperator(httptest.NewRequest("GET", "/dash/chat/"+folder+"/", nil))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d, body = %q", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "New chat session") {
		t.Errorf("new-session form missing: %q", body)
	}
	if !strings.Contains(body, "1 active") {
		t.Errorf("existing session not listed: %q", body)
	}
	// Form POSTs to the same folder path.
	if !strings.Contains(body, `action="/dash/chat/`+folder+`/"`) {
		t.Errorf("form action missing: %q", body)
	}
}

// A non-operator with a direct grant on the folder may see the group page.
func TestHandleChatGroup_grantedMember(t *testing.T) {
	inst, mux := chatTestDash(t)
	folder := "team"
	addGroup(t, inst, folder)
	if err := inst.Store.AddACLRow(core.ACLRow{
		Principal: "member@x", Action: "admin", Scope: folder, Effect: "allow",
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/dash/chat/"+folder+"/", nil)
	req.Header.Set("X-User-Sub", "member@x")
	req.Header.Set("X-User-Groups", `["`+folder+`"]`)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d, body = %q", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "New chat session") {
		t.Errorf("granted member did not get group page")
	}
}

func TestHandleChatNew_creates(t *testing.T) {
	inst, mux := chatTestDash(t)
	folder := "alice"
	addGroup(t, inst, folder)
	if err := inst.Store.AddACLRow(core.ACLRow{
		Principal: "admin@x", Action: "admin", Scope: folder, Effect: "allow",
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("POST", "/dash/chat/"+folder+"/",
		strings.NewReader("label=design"))
	req.Host = "example.com" // same-origin (no Origin header) for CSRF gate
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", "admin@x")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body = %q", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, "/chat/") || !strings.HasSuffix(loc, "/") {
		t.Errorf("redirect = %q, want /chat/<token>/", loc)
	}
	// The minted token resolves back to this folder's web: JID.
	raw := strings.TrimSuffix(strings.TrimPrefix(loc, "/chat/"), "/")
	row, ok := inst.Store.LookupRouteToken(raw)
	if !ok {
		t.Fatalf("minted token %q not found in store", raw)
	}
	if row.JID != "web:"+folder || row.OwnerFolder != folder {
		t.Errorf("token row = %+v, want web:%s / %s", row, folder, folder)
	}
}

// A non-admin caller cannot mint a chat token.
func TestHandleChatNew_forbidden(t *testing.T) {
	inst, mux := chatTestDash(t)
	folder := "alice"
	addGroup(t, inst, folder)

	req := httptest.NewRequest("POST", "/dash/chat/"+folder+"/",
		strings.NewReader(""))
	req.Host = "example.com"
	req.Header.Set("X-User-Sub", "nobody@x")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	if len(inst.Store.ListRouteTokens(folder)) != 0 {
		t.Errorf("forbidden POST must not mint a token")
	}
}

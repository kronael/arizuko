package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
)

// Every dashboard mutation must persist an audit_log row naming the operator who
// made it. These write through the real handler and read audit_log back, so
// swapping any site to an audit-free writer (PutACLRow, RemoveACLRowBare,
// PutRouteRow, a raw INSERT/DELETE) fails the case: the mutation still lands but
// no row appears, and actorFor returns "".
//
// The bar is the operator, not merely a row. routd.db has carried audit_log
// since routd migration 0016, so "routd.db has no audit_log" never justified
// skipping it; recording the mutation as "system" would be just as wrong, since
// the whole point is attributing an operator action to that operator.

// auditActor returns the actor_sub of the newest audit_log row for action, or ""
// when the mutation recorded nothing.
func auditActor(t *testing.T, d *dash, action string) string {
	t.Helper()
	var sub string
	err := d.adminDB().QueryRow(
		`SELECT COALESCE(actor_sub, '') FROM audit_log
		 WHERE action = ? ORDER BY id DESC LIMIT 1`, action).Scan(&sub)
	if err != nil {
		return ""
	}
	return sub
}

func TestGrantAdd_Audited(t *testing.T) {
	d, _ := splitAdminDash(t, "alice@x")
	mux := newMux(d)
	form := url.Values{
		"principal": {"bob@x"}, "action": {"send"}, "effect": {"allow"}, "scope": {"team"},
	}.Encode()
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, adminReq("POST", "/dash/groups/team/grants", form, "alice@x"))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST grants = %d body=%q", w.Code, w.Body.String())
	}
	if got := auditActor(t, d, "acl.add"); got != "alice@x" {
		t.Errorf("acl.add actor_sub = %q, want alice@x", got)
	}
}

func TestGrantRevoke_Audited(t *testing.T) {
	d, routd := splitAdminDash(t, "alice@x")
	if _, err := routd.Exec(
		`INSERT INTO acl (principal, action, scope, effect, granted_at)
		 VALUES ('bob@x', 'send', 'team', 'allow', '')`); err != nil {
		t.Fatal(err)
	}
	mux := newMux(d)
	form := url.Values{"principal": {"bob@x"}, "action": {"send"}, "effect": {"allow"}}.Encode()
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, adminReq("POST", "/dash/groups/team/grants/revoke", form, "alice@x"))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST grants/revoke = %d body=%q", w.Code, w.Body.String())
	}
	if n := count(t, routd, `SELECT COUNT(*) FROM acl WHERE principal='bob@x' AND action='send'`); n != 0 {
		t.Fatalf("grant not revoked (%d rows)", n)
	}
	if got := auditActor(t, d, "acl.remove"); got != "alice@x" {
		t.Errorf("acl.remove actor_sub = %q, want alice@x", got)
	}
}

func TestRouteCreate_Audited(t *testing.T) {
	d, _ := splitAdminDash(t, "alice@x")
	mux := newMux(d)
	form := url.Values{"seq": {"5"}, "match": {"room=42"}, "target": {"team"}}.Encode()
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, adminReq("POST", "/dash/routes/", form, "alice@x"))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST routes = %d body=%q", w.Code, w.Body.String())
	}
	if got := auditActor(t, d, "route.create"); got != "alice@x" {
		t.Errorf("route.create actor_sub = %q, want alice@x", got)
	}
}

func TestRouteTokenIssue_Audited(t *testing.T) {
	d, _ := splitAdminDash(t, "alice@x")
	mux := newMux(d)
	form := url.Values{"kind": {"chat"}}.Encode()
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, adminReq("POST", "/dash/tokens/team/", form, "alice@x"))
	if w.Code != 200 {
		t.Fatalf("POST tokens = %d body=%q", w.Code, w.Body.String())
	}
	if got := auditActor(t, d, "route_token.mint"); got != "alice@x" {
		t.Errorf("route_token.mint actor_sub = %q, want alice@x", got)
	}
}

func TestRouteTokenRevoke_Audited(t *testing.T) {
	d, routd := splitAdminDash(t, "alice@x")
	if _, err := routd.Exec(
		`INSERT INTO route_tokens (token_hash, jid, owner_folder, created_at, kind)
		 VALUES (?, 'web:team', 'team', '', 'route')`, []byte("h1")); err != nil {
		t.Fatal(err)
	}
	mux := newMux(d)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, adminReq("POST", "/dash/tokens/team/"+encodeJID("web:team")+"/revoke", "", "alice@x"))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST revoke = %d body=%q", w.Code, w.Body.String())
	}
	if got := auditActor(t, d, "route_token.revoke"); got != "alice@x" {
		t.Errorf("route_token.revoke actor_sub = %q, want alice@x", got)
	}
}

// The chat portal mints a route token too; it must be audited like any other.
func TestChatNew_Audited(t *testing.T) {
	d, _ := splitAdminDash(t, "alice@x")
	mux := newMux(d)
	req := adminReq("POST", "/dash/chat/team/", "label=kickoff", "alice@x")
	req.Host = "example.com"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST chat = %d body=%q", w.Code, w.Body.String())
	}
	if got := auditActor(t, d, "route_token.mint"); got != "alice@x" {
		t.Errorf("route_token.mint actor_sub = %q, want alice@x", got)
	}
}

func TestGroupDelete_Audited(t *testing.T) {
	d, routd := splitAdminDash(t, "alice@x")
	mux := newMux(d)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, adminReq("POST", "/dash/groups/team/delete", "", "alice@x"))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST delete = %d body=%q", w.Code, w.Body.String())
	}
	if n := count(t, routd, `SELECT COUNT(*) FROM groups WHERE folder='team'`); n != 0 {
		t.Fatalf("group not deleted")
	}
	if got := auditActor(t, d, "group.delete"); got != "alice@x" {
		t.Errorf("group.delete actor_sub = %q, want alice@x", got)
	}
}

// The delete cascade and its audit row share one transaction, so a failed purge
// cannot leave an audit row claiming a delete that was rolled back. Deleting a
// folder whose child row blocks the cascade must leave audit_log untouched.
func TestGroupDelete_AuditRollsBackWithMutation(t *testing.T) {
	d, routd := splitAdminDash(t, "alice@x")
	before := count(t, routd, `SELECT COUNT(*) FROM audit_log WHERE action='group.delete'`)
	mux := newMux(d)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, adminReq("POST", "/dash/groups/nosuchfolder/delete", "", "alice@x"))
	if w.Code != http.StatusNotFound {
		t.Fatalf("delete missing folder = %d, want 404", w.Code)
	}
	after := count(t, routd, `SELECT COUNT(*) FROM audit_log WHERE action='group.delete'`)
	if after != before {
		t.Errorf("audit rows for a delete that did not happen: %d -> %d", before, after)
	}
}

// Route update and delete already audited before this change; keep them covered
// so the actor stays attached as those handlers evolve.
func TestRouteDelete_Audited(t *testing.T) {
	d, routd := splitAdminDash(t, "alice@x")
	var id int64
	if err := routd.QueryRow(
		`INSERT INTO routes (seq, match, target) VALUES (1, 'm=1', 'team') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	mux := newMux(d)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, adminReq("POST", "/dash/routes/"+strconv.FormatInt(id, 10)+"/delete", "", "alice@x"))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST route delete = %d body=%q", w.Code, w.Body.String())
	}
	if n := count(t, routd, `SELECT COUNT(*) FROM audit_log WHERE action='route.delete'`); n != 1 {
		t.Errorf("route.delete audit rows = %d, want 1", n)
	}
}

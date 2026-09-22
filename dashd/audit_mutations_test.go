package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/store"
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
	rows := auditRowsFor(t, d.routd, action)
	if len(rows) == 0 {
		return ""
	}
	return rows[0].ActorSub // newest first
}

// auditRowsFor reads back every recorded row carrying one action, newest first,
// through the method every daemon answers GET /v1/audit with.
func auditRowsFor(t *testing.T, h objstore.Store, action string) []audit.Row {
	t.Helper()
	rows, err := audit.Query(t.Context(), h, audit.Filter{Limit: audit.MaxLimit})
	if err != nil {
		t.Fatalf("read the audit trail: %v", err)
	}
	var out []audit.Row
	for _, r := range rows {
		if r.Action == action {
			out = append(out, r)
		}
	}
	return out
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
	seedGrant(t, routd, core.ACLRow{
		Principal: "bob@x", Action: "send", Scope: "team", Effect: "allow",
	})
	mux := newMux(d)
	form := url.Values{"principal": {"bob@x"}, "action": {"send"}, "effect": {"allow"}}.Encode()
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, adminReq("POST", "/dash/groups/team/grants/revoke", form, "alice@x"))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST grants/revoke = %d body=%q", w.Code, w.Body.String())
	}
	if n := countGrants(t, routd, "bob@x", "send"); n != 0 {
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

// The cockpit MINTS NOTHING (BUGS V2, spec 6/30 §webd). A mint form here was a
// second writer over route_tokens beside routd's resource, with its own label
// validation — the shape that drifts. Issuance is routd's
// POST /v1/route_tokens/{chat,hook}, the agent's issue_* tools, and
// `arizuko token`. Guard both halves: no route, and no form to post to.
func TestRouteTokenMint_NotInCockpit(t *testing.T) {
	d, _ := splitAdminDash(t, "alice@x")
	mux := newMux(d)

	form := url.Values{"kind": {"chat"}}.Encode()
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, adminReq("POST", "/dash/tokens/team/", form, "alice@x"))
	if w.Code == http.StatusOK {
		t.Fatalf("POST /dash/tokens/{folder}/ returned 200 — the mint route is back")
	}
	n, err := d.routdStore().CountRouteTokens(t.Context())
	if err != nil {
		t.Fatalf("count the route tokens: %v", err)
	}
	if n != 0 {
		t.Fatalf("route_tokens = %d rows, want 0 — the cockpit minted a token", n)
	}

	w = httptest.NewRecorder()
	mux.ServeHTTP(w, adminReq("GET", "/dash/tokens/team/", "", "alice@x"))
	if w.Code != http.StatusOK {
		t.Fatalf("GET tokens = %d body=%q", w.Code, w.Body.String())
	}
	if body := w.Body.String(); strings.Contains(body, `action="/dash/tokens/team/"`) {
		t.Error("tokens page still renders a mint form posting to itself")
	}
}

func TestRouteTokenRevoke_Audited(t *testing.T) {
	d, routd := splitAdminDash(t, "alice@x")
	seedRouteToken(t, routd, store.RouteToken{
		JID: "web:team", OwnerFolder: "team", TargetFolder: "team",
	})
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

func TestGroupDelete_Audited(t *testing.T) {
	d, routd := splitAdminDash(t, "alice@x")
	mux := newMux(d)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, adminReq("POST", "/dash/groups/team/delete", "", "alice@x"))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST delete = %d body=%q", w.Code, w.Body.String())
	}
	if hasFolder(t, routd, "team") {
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
	before := len(auditRowsFor(t, routd, "group.delete"))
	mux := newMux(d)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, adminReq("POST", "/dash/groups/nosuchfolder/delete", "", "alice@x"))
	if w.Code != http.StatusNotFound {
		t.Fatalf("delete missing folder = %d, want 404", w.Code)
	}
	after := len(auditRowsFor(t, routd, "group.delete"))
	if after != before {
		t.Errorf("audit rows for a delete that did not happen: %d -> %d", before, after)
	}
}

// Route update and delete record the operator, like every other mutation here;
// a handler that emits its own row as the daemon ("rest:dashd") fails these.
func TestRouteDelete_Audited(t *testing.T) {
	d, routd := splitAdminDash(t, "alice@x")
	id := seedRoute(t, routd, core.Route{Seq: 1, Match: "m=1", Target: "team"})
	mux := newMux(d)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, adminReq("POST", "/dash/routes/"+strconv.FormatInt(id, 10)+"/delete", "", "alice@x"))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST route delete = %d body=%q", w.Code, w.Body.String())
	}
	if n := len(auditRowsFor(t, routd, "route.delete")); n != 1 {
		t.Errorf("route.delete audit rows = %d, want 1", n)
	}
	if got := auditActor(t, d, "route.delete"); got != "alice@x" {
		t.Errorf("route.delete actor_sub = %q, want alice@x", got)
	}
}

func TestRouteUpdate_Audited(t *testing.T) {
	d, routd := splitAdminDash(t, "alice@x")
	id := seedRoute(t, routd, core.Route{Seq: 1, Match: "m=1", Target: "team"})
	form := url.Values{"seq": {"7"}, "match": {"m=updated"}, "target": {"team"}}.Encode()
	w := httptest.NewRecorder()
	newMux(d).ServeHTTP(w, adminReq("PATCH", "/dash/routes/"+strconv.FormatInt(id, 10), form, "alice@x"))
	if w.Code != http.StatusNoContent {
		t.Fatalf("PATCH route = %d body=%q", w.Code, w.Body.String())
	}
	if got := auditActor(t, d, "route.update"); got != "alice@x" {
		t.Errorf("route.update actor_sub = %q, want alice@x", got)
	}
}

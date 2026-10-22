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
	"github.com/kronael/arizuko/tests/testutils"
	"time"
)

// splitAdminDash wires a dash on routd.db, the ONLY store dashd opens. The admin
// ACL row the requireAdmin gate consults is seeded there, so a passing write also
// proves the gate's read path reads routd.db. dashd no longer carries a
// messages.db handle at all — the monolith is unreachable by construction, which
// is why these tests assert on routd.db alone.
func splitAdminDash(t *testing.T, adminSub string) (*dash, objstore.Store) {
	t.Helper()
	inst := testutils.NewRoutdInstance(t)
	routd := inst.Store.Obj()
	// dashd/main.go points the package-level audit sink at routd.db at startup;
	// without the same wiring here the handlers that use audit.Emit (rather than
	// an in-tx write) would silently drop their rows and a missing-audit
	// regression would read as a pass.
	audit.Init(routd, "test")
	t.Cleanup(func() { audit.Init(nil, "") })
	// Operator grant lives in routd.db only.
	seedAdmin(t, routd, adminSub, "**")
	// "team" is the folder these tests act on. It must exist: a mint checks the
	// token's target against the groups it may deliver to, so a token cannot be
	// minted for a folder that was never created.
	seedFolder(t, routd, "team")
	// routdURL: the pages read through routd's /v1 now (6/28), so the tests need
	// something serving the rows they seed into routd.db.
	d := &dash{routd: routd, groupsDir: t.TempDir(), routdURL: stubRoutd(t, routd)}
	withRuned(d, stubRuned(t, http.StatusOK, nil))
	return d, routd
}

func adminReq(method, path, body, sub string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	r.Header.Set("X-User-Sub", sub)
	return r
}

// TestGrantAdd_TargetsRoutdDB: POST .../grants inserts the acl row into routd.db,
// not messages.db. The gate also reading routd.db proves the admin-UI read path.
func TestGrantAdd_TargetsRoutdDB(t *testing.T) {
	d, routd := splitAdminDash(t, "alice@x")
	mux := newMux(d)

	form := url.Values{
		"principal": {"bob@x"}, "action": {"send"}, "effect": {"allow"}, "scope": {"team"},
	}.Encode()
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, adminReq("POST", "/dash/groups/team/grants", form, "alice@x"))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST grants = %d body=%q", w.Code, w.Body.String())
	}

	if n := countGrants(t, routd, "bob@x", "send"); n != 1 {
		t.Errorf("routd.db acl rows = %d, want 1", n)
	}
}

// TestGrantList_ReadsRoutdV1: the grants page renders a row seeded into routd.db
// (proves the admin-UI READ reflects the live routd.db table).
func TestGrantList_ReadsRoutdV1(t *testing.T) {
	d, routd := splitAdminDash(t, "alice@x")
	seedGrant(t, routd, core.ACLRow{
		Principal: "carol@x", Action: "reply", Scope: "team", Effect: "allow",
	})
	mux := newMux(d)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, adminReq("GET", "/dash/groups/team/grants", "", "alice@x"))
	if w.Code != 200 {
		t.Fatalf("GET grants = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "carol@x") {
		t.Errorf("grants page missing routd.db row carol@x: %s", w.Body.String())
	}
}

// TestGroupDelete_TargetsRoutdDB: deleting a group removes the row from routd.db.
func TestGroupDelete_TargetsRoutdDB(t *testing.T) {
	d, routd := splitAdminDash(t, "alice@x")
	mux := newMux(d)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, adminReq("POST", "/dash/groups/team/delete", "", "alice@x"))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST delete = %d body=%q", w.Code, w.Body.String())
	}
	if hasFolder(t, routd, "team") {
		t.Error("the group survived its own delete in routd.db")
	}
}

// TestGroupList_ReadsRoutdDB: the groups page lists a folder seeded into routd.db.
func TestGroupList_ReadsRoutdDB(t *testing.T) {
	d, routd := splitAdminDash(t, "alice@x")
	seedFolder(t, routd, "liveteam")
	mux := newMux(d)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, adminReq("GET", "/dash/groups/", "", "alice@x"))
	if w.Code != 200 {
		t.Fatalf("GET groups = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "liveteam") {
		t.Errorf("groups page missing routd.db folder liveteam")
	}
}

// TestRouteCreate_TargetsRoutdDB: POST .../routes inserts into routd.db.
func TestRouteCreate_TargetsRoutdDB(t *testing.T) {
	d, routd := splitAdminDash(t, "alice@x")
	mux := newMux(d)

	form := url.Values{"seq": {"5"}, "match": {"room=42"}, "target": {"team"}}.Encode()
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, adminReq("POST", "/dash/routes/", form, "alice@x"))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST routes = %d body=%q", w.Code, w.Body.String())
	}
	if n := countRoutes(t, routd, func(r core.Route) bool {
		return r.Match == "room=42" && r.Target == "team"
	}); n != 1 {
		t.Errorf("routd.db route rows = %d, want 1", n)
	}
}

// TestRouteList_ReadsRoutdV1: the routes page renders a row that routd serves.
//
// Renamed from ReadsRoutdDB: the row is still seeded into routd.db, but the
// page reaches it through routd's /v1 now (6/28 §"Read-path: /v1 only"), so
// the name no longer describes what is being asserted. The property — a route
// in the table reaches the page — is unchanged.
func TestRouteList_ReadsRoutdV1(t *testing.T) {
	d, routd := splitAdminDash(t, "alice@x")
	seedRoute(t, routd, core.Route{Seq: 9, Match: "liveroute=1", Target: "team"})
	mux := newMux(d)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, adminReq("GET", "/dash/routes/", "", "alice@x"))
	if w.Code != 200 {
		t.Fatalf("GET routes = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "liveroute=1") {
		t.Errorf("routes page missing routd.db row liveroute=1")
	}
}

// TestRouteDelete_TargetsRoutdDB: deleting a route removes it from routd.db.
func TestRouteDelete_TargetsRoutdDB(t *testing.T) {
	d, routd := splitAdminDash(t, "alice@x")
	id := seedRoute(t, routd, core.Route{Seq: 1, Match: "m=1", Target: "team"})
	mux := newMux(d)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, adminReq("POST", "/dash/routes/"+strconv.FormatInt(id, 10)+"/delete", "", "alice@x"))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST route delete = %d body=%q", w.Code, w.Body.String())
	}
	if n := countRoutes(t, routd, func(r core.Route) bool { return r.ID == id }); n != 0 {
		t.Errorf("routd.db route rows after delete = %d, want 0", n)
	}
}

// TestRouteTokenList_ReadsRoutdV1: the tokens page renders the JID and its
// context, never a token value — the stored column is a hash and the cockpit
// has no mint (BUGS V2).
//
// It used to be TestRouteTokenList_ReadsRoutdDB and seeded routd.db, which was
// a test OF THE MECHANISM: spec 6/28 says no dashboard reads a SQLite DB
// directly, so the page now asks routd's /v1 and this drives a stub routd
// instead. The property — which rows reach the page — is unchanged; only where
// they come from moved.
func TestRouteTokenList_ReadsRoutdV1(t *testing.T) {
	routd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/route_tokens" {
			http.NotFound(w, r)
			return
		}
		// The folder narrowing is the caller's, so assert it is sent: a page
		// that dropped it would render another tenant's tokens.
		if got := r.URL.Query().Get("folder"); got != "team" {
			t.Errorf("folder param = %q, want team", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"jid":"hook:team/github","owner_folder":"team",` +
			`"created_at":"2026-08-15T00:00:00Z","context":"push events; summarize"}]`))
	}))

	d, _ := splitAdminDash(t, "alice@x")
	d.routdURL = routd.URL

	mux := newMux(d)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, adminReq("GET", "/dash/tokens/team/", "", "alice@x"))
	if w.Code != http.StatusOK {
		t.Fatalf("GET tokens = %d body=%q", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "hook:team/github") {
		t.Error("tokens page did not render the row routd served")
	}
	if !strings.Contains(body, "push events; summarize") {
		t.Error("tokens page did not render the row's context (spec 5/W link context)")
	}
}

// routd being unreachable must SAY so. A blank token list reads as "this folder
// has no tokens", which is the opposite of the truth and the answer an operator
// would act on.
func TestRouteTokenListSaysWhenRoutdIsUnreachable(t *testing.T) {
	d, _ := splitAdminDash(t, "alice@x")
	d.routdURL = "http://127.0.0.1:1" // refused

	mux := newMux(d)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, adminReq("GET", "/dash/tokens/team/", "", "alice@x"))
	if !strings.Contains(w.Body.String(), "could not reach routd") {
		t.Error("an unreachable routd rendered as an empty token list")
	}
}

// TestTaskCreate_TargetsRoutdDB: creating a scheduled task inserts the row into
// routd.db, not messages.db (routd OWNS scheduled_tasks in the split topology).
func TestTaskCreate_TargetsRoutdDB(t *testing.T) {
	d, routd := splitAdminDash(t, "alice@x")
	mux := newMux(d)

	form := url.Values{
		"owner": {"team"}, "chat_jid": {"web:team"},
		"prompt": {"do the thing"}, "cron": {"0 9 * * *"},
	}.Encode()
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, adminReq("POST", "/dash/tasks/", form, "alice@x"))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST tasks = %d body=%q", w.Code, w.Body.String())
	}
	if n := countTasks(t, routd, func(task core.Task) bool {
		return task.Owner == "team" && task.Prompt == "do the thing"
	}); n != 1 {
		t.Errorf("routd.db scheduled_tasks rows = %d, want 1", n)
	}
}

// TestTaskAction_TargetsRoutdDB: pausing a task updates its status in routd.db.
func TestTaskAction_TargetsRoutdDB(t *testing.T) {
	d, routd := splitAdminDash(t, "alice@x")
	seedScheduledTask(t, routd, core.Task{
		ID: "t-1", Owner: "team", ChatJID: "web:team", Prompt: "p", Status: "active",
	})
	mux := newMux(d)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, adminReq("POST", "/dash/tasks/t-1/pause", "", "alice@x"))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST task pause = %d body=%q", w.Code, w.Body.String())
	}
	if got := statusOf(t, routd, "t-1"); got != "paused" {
		t.Errorf("routd.db task status = %q, want paused", got)
	}
}

// TestTaskList_ReadsRoutdDB: the tasks list renders a task seeded into routd.db.
func TestTaskList_ReadsRoutdDB(t *testing.T) {
	d, routd := splitAdminDash(t, "alice@x")
	seedScheduledTask(t, routd, core.Task{
		ID: "t-live", Owner: "liveowner", ChatJID: "web:team", Prompt: "p", Status: "active",
	})
	mux := newMux(d)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, adminReq("GET", "/dash/tasks/x/list", "", "alice@x"))
	if w.Code != 200 {
		t.Fatalf("GET tasks list = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "t-live") {
		t.Errorf("tasks list missing routd.db row t-live")
	}
}

func statusOf(t *testing.T, db objstore.Store, id string) string {
	t.Helper()
	task, ok, err := store.NewOver(db).TaskByID(id)
	if err != nil || !ok {
		t.Fatalf("status of %s: ok=%v err=%v", id, ok, err)
	}
	return task.Status
}

// TestRouteTokenRevoke_TargetsRoutdDB: revoking a token deletes it from routd.db.
func TestRouteTokenRevoke_TargetsRoutdDB(t *testing.T) {
	d, routd := splitAdminDash(t, "alice@x")
	seedRouteToken(t, routd, store.RouteToken{
		JID: "web:team", OwnerFolder: "team", TargetFolder: "team",
	})
	mux := newMux(d)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, adminReq("POST", "/dash/tokens/team/"+encodeJID("web:team")+"/revoke", "", "alice@x"))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST token revoke = %d body=%q", w.Code, w.Body.String())
	}
	if n := countRouteTokens(t, routd, "team", func(tok store.RouteToken) bool {
		return tok.JID == "web:team"
	}); n != 0 {
		t.Errorf("routd.db route_token rows after revoke = %d, want 0", n)
	}
}

// TestActivity_ReadsRoutdDB: the activity feed renders a messages row seeded into
// routd.db (where routd is the sole live appender), not the stale messages.db twin.
func TestActivity_ReadsRoutdDB(t *testing.T) {
	d, routd := splitAdminDash(t, "alice@x")
	seedRoutdMessage(t, routd, core.Message{
		ID: "m-live", ChatJID: "web:team", Sender: "u", Content: "live body",
		Timestamp: time.Date(2026, 6, 14, 0, 0, 0, 0, time.UTC), Source: "web", Verb: "message",
	})
	mux := newMux(d)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, adminReq("GET", "/dash/activity/x/recent", "", "alice@x"))
	if w.Code != 200 {
		t.Fatalf("GET activity = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "live body") {
		t.Errorf("activity missing routd.db row: %s", body)
	}
}

// TestErroredCount_ReadsRoutdDB: the status banner's errored-chat count comes from
// routd.db's messages.errored flags, not the frozen messages.db twin.
func TestErroredCount_ReadsRoutdDB(t *testing.T) {
	d, routd := splitAdminDash(t, "alice@x")
	if err := store.NewOver(routd).MarkChatErrored("web:liveerr"); err != nil {
		t.Fatal(err)
	}
	if n := d.countVisibleErroredChats(nil, true); n != 1 {
		t.Errorf("errored chats = %d, want 1 (routd.db only, not the stale messages.db twin)", n)
	}
}

// TestProfile_ReadsRoutdDB: the profile name resolves from user_profiles in routd.db
// (routd owns it post-split per routd/migrations 0011 + 0025), not messages.db.
func TestProfile_ReadsRoutdDB(t *testing.T) {
	d, routd := splitAdminDash(t, "alice@x")
	if err := store.NewOver(routd).CreateUserProfile("google:bob", "bob", "Bob Live"); err != nil {
		t.Fatal(err)
	}
	mux := newMux(d)
	w := httptest.NewRecorder()
	r := adminReq("GET", "/dash/profile/", "", "alice@x")
	r.Header.Set("X-User-Sub", "google:bob")
	mux.ServeHTTP(w, r)
	body := w.Body.String()
	if !strings.Contains(body, "Bob Live") {
		t.Errorf("profile missing routd.db name: %s", body)
	}
	if strings.Contains(body, "Bob Stale") {
		t.Errorf("profile rendered a stale messages.db name (must read routd.db)")
	}
}

// routd being unreachable must SAY so on the routes page. An empty route table
// reads as "this instance routes nothing", which is both false and the kind of
// thing an operator would act on during an incident.
func TestRouteListSaysWhenRoutdIsUnreachable(t *testing.T) {
	d, _ := splitAdminDash(t, "alice@x")
	d.routdURL = "http://127.0.0.1:1" // refused

	mux := newMux(d)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, adminReq("GET", "/dash/routes/", "", "alice@x"))
	if !strings.Contains(w.Body.String(), "could not reach routd") {
		t.Error("an unreachable routd rendered as an empty route table")
	}
}

// routd being unreachable must SAY so on the grants page. An empty grants
// table reads as "this folder has no permissions", which is the opposite of
// "we could not read them" — BUGS A4, the confusion this page already guards
// against for a DB error and must keep guarding against for an HTTP one.
func TestGrantListSaysWhenRoutdIsUnreachable(t *testing.T) {
	d, _ := splitAdminDash(t, "alice@x")
	d.routdURL = "http://127.0.0.1:1" // refused

	mux := newMux(d)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, adminReq("GET", "/dash/groups/team/grants", "", "alice@x"))
	if !strings.Contains(w.Body.String(), "could not read grants") {
		t.Error("an unreachable routd rendered as an empty grants table")
	}
}

// The task page must distinguish "no such task" from "we could not ask". They
// look identical to an operator and only one means the task is gone.
func TestTaskDetailSeparatesNotFoundFromUnreachable(t *testing.T) {
	d, _ := splitAdminDash(t, "alice@x")
	mux := newMux(d)

	// Reachable routd, absent task → 404.
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, adminReq("GET", "/dash/tasks/no-such-task", "", "alice@x"))
	if w.Code != http.StatusNotFound {
		t.Errorf("absent task = %d, want 404", w.Code)
	}

	// Unreachable routd → 503, not 404 and not 500: a dependency outage says
	// "ask again", an internal error says "this is broken", and only one is true.
	d.routdURL = "http://127.0.0.1:1"
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, adminReq("GET", "/dash/tasks/no-such-task", "", "alice@x"))
	if w2.Code != http.StatusServiceUnavailable {
		t.Errorf("unreachable routd = %d, want 503", w2.Code)
	}
}

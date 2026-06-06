package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// adminSchema is the minimal set of routd-owned admin tables dashd reads+writes
// (mirrors routd/migrations/). Used to stand up an isolated routd.db twin so a
// write can be proven to land in routd.db and NOT messages.db.
const adminSchema = `
CREATE TABLE acl (
  principal TEXT NOT NULL, action TEXT NOT NULL, scope TEXT NOT NULL,
  effect TEXT NOT NULL DEFAULT 'allow', params TEXT NOT NULL DEFAULT '',
  predicate TEXT NOT NULL DEFAULT '', granted_by TEXT, granted_at TEXT NOT NULL,
  PRIMARY KEY (principal, action, scope, params, predicate, effect));
CREATE TABLE acl_membership (
  child TEXT NOT NULL, parent TEXT NOT NULL, added_by TEXT, added_at TEXT NOT NULL,
  PRIMARY KEY (child, parent));
CREATE TABLE groups (
  folder TEXT PRIMARY KEY, added_at TEXT NOT NULL, container_config TEXT,
  updated_at TEXT, product TEXT NOT NULL DEFAULT 'assistant',
  cost_cap_cents_per_day INTEGER NOT NULL DEFAULT 0, open INTEGER NOT NULL DEFAULT 1,
  observe_window_messages INTEGER, observe_window_chars INTEGER, model TEXT);
CREATE TABLE routes (
  id INTEGER PRIMARY KEY AUTOINCREMENT, seq INTEGER NOT NULL DEFAULT 0,
  match TEXT NOT NULL DEFAULT '', target TEXT NOT NULL,
  observe_window_messages INTEGER, observe_window_chars INTEGER);
CREATE TABLE route_tokens (
  token_hash BLOB PRIMARY KEY, jid TEXT NOT NULL,
  owner_folder TEXT NOT NULL, created_at TEXT NOT NULL);
CREATE TABLE scheduled_tasks (
  id TEXT PRIMARY KEY, owner TEXT NOT NULL, chat_jid TEXT NOT NULL,
  prompt TEXT NOT NULL, cron TEXT, next_run TEXT,
  status TEXT NOT NULL DEFAULT 'active', created_at TEXT NOT NULL,
  context_mode TEXT NOT NULL DEFAULT 'group');
CREATE TABLE task_run_logs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id TEXT NOT NULL REFERENCES scheduled_tasks(id) ON DELETE CASCADE,
  run_at TEXT NOT NULL, duration_ms INTEGER, status TEXT NOT NULL,
  result TEXT, error TEXT);
`

// splitAdminDash wires a dash with DISTINCT messages.db (db/dbRW) and routd.db
// (dbRoutd) handles, each carrying the admin schema. The admin ACL row that the
// requireAdmin gate consults is seeded into routd.db ONLY — so a passing write
// also proves the gate's read path reads routd.db, not messages.db.
func splitAdminDash(t *testing.T, adminSub string) (*dash, *sql.DB, *sql.DB) {
	t.Helper()
	msg, err := sql.Open("sqlite", "file:admin_msg?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := msg.Exec(adminSchema); err != nil {
		t.Fatalf("messages.db schema: %v", err)
	}
	routd, err := sql.Open("sqlite", "file:admin_routd?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := routd.Exec(adminSchema); err != nil {
		t.Fatalf("routd.db schema: %v", err)
	}
	// Operator grant lives in routd.db only.
	if _, err := routd.Exec(
		`INSERT INTO acl (principal, action, scope, effect, granted_at)
		 VALUES (?, 'admin', '**', 'allow', '')`, adminSub); err != nil {
		t.Fatalf("seed admin grant: %v", err)
	}
	t.Cleanup(func() { msg.Close(); routd.Close() })
	return &dash{db: msg, dbRW: msg, dbRoutd: routd, groupsDir: t.TempDir()}, msg, routd
}

func adminReq(method, path, body, sub string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	r.Header.Set("X-User-Sub", sub)
	return r
}

func count(t *testing.T, db *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", q, err)
	}
	return n
}

// TestGrantAdd_TargetsRoutdDB: POST .../grants inserts the acl row into routd.db,
// not messages.db. The gate also reading routd.db proves the admin-UI read path.
func TestGrantAdd_TargetsRoutdDB(t *testing.T) {
	d, msg, routd := splitAdminDash(t, "alice@x")
	mux := newMux(d)

	form := url.Values{
		"principal": {"bob@x"}, "action": {"send"}, "effect": {"allow"}, "scope": {"team"},
	}.Encode()
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, adminReq("POST", "/dash/groups/team/grants", form, "alice@x"))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST grants = %d body=%q", w.Code, w.Body.String())
	}

	if n := count(t, routd, `SELECT COUNT(*) FROM acl WHERE principal='bob@x' AND action='send'`); n != 1 {
		t.Errorf("routd.db acl rows = %d, want 1", n)
	}
	if n := count(t, msg, `SELECT COUNT(*) FROM acl WHERE principal='bob@x'`); n != 0 {
		t.Errorf("messages.db acl rows = %d, want 0 (must not write the monolith)", n)
	}
}

// TestGrantList_ReadsRoutdDB: the grants page renders a row seeded into routd.db
// (proves the admin-UI READ reflects the live routd.db table).
func TestGrantList_ReadsRoutdDB(t *testing.T) {
	d, _, routd := splitAdminDash(t, "alice@x")
	if _, err := routd.Exec(
		`INSERT INTO acl (principal, action, scope, effect, granted_at)
		 VALUES ('carol@x', 'reply', 'team', 'allow', '')`); err != nil {
		t.Fatal(err)
	}
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
	d, msg, routd := splitAdminDash(t, "alice@x")
	// Seed the same folder in BOTH DBs; only routd's row should be deleted.
	for _, db := range []*sql.DB{msg, routd} {
		if _, err := db.Exec(`INSERT INTO groups (folder, added_at) VALUES ('team', '')`); err != nil {
			t.Fatal(err)
		}
	}
	mux := newMux(d)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, adminReq("POST", "/dash/groups/team/delete", "", "alice@x"))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST delete = %d body=%q", w.Code, w.Body.String())
	}
	if n := count(t, routd, `SELECT COUNT(*) FROM groups WHERE folder='team'`); n != 0 {
		t.Errorf("routd.db group rows = %d, want 0", n)
	}
	if n := count(t, msg, `SELECT COUNT(*) FROM groups WHERE folder='team'`); n != 1 {
		t.Errorf("messages.db group rows = %d, want 1 (must not touch the monolith)", n)
	}
}

// TestGroupList_ReadsRoutdDB: the groups page lists a folder seeded into routd.db.
func TestGroupList_ReadsRoutdDB(t *testing.T) {
	d, _, routd := splitAdminDash(t, "alice@x")
	if _, err := routd.Exec(`INSERT INTO groups (folder, added_at) VALUES ('liveteam', '')`); err != nil {
		t.Fatal(err)
	}
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
	d, msg, routd := splitAdminDash(t, "alice@x")
	mux := newMux(d)

	form := url.Values{"seq": {"5"}, "match": {"room=42"}, "target": {"team"}}.Encode()
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, adminReq("POST", "/dash/routes/", form, "alice@x"))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST routes = %d body=%q", w.Code, w.Body.String())
	}
	if n := count(t, routd, `SELECT COUNT(*) FROM routes WHERE match='room=42' AND target='team'`); n != 1 {
		t.Errorf("routd.db route rows = %d, want 1", n)
	}
	if n := count(t, msg, `SELECT COUNT(*) FROM routes WHERE match='room=42'`); n != 0 {
		t.Errorf("messages.db route rows = %d, want 0", n)
	}
}

// TestRouteList_ReadsRoutdDB: the routes page renders a row seeded into routd.db.
func TestRouteList_ReadsRoutdDB(t *testing.T) {
	d, _, routd := splitAdminDash(t, "alice@x")
	if _, err := routd.Exec(
		`INSERT INTO routes (seq, match, target) VALUES (9, 'liveroute=1', 'team')`); err != nil {
		t.Fatal(err)
	}
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
	d, _, routd := splitAdminDash(t, "alice@x")
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
	if n := count(t, routd, `SELECT COUNT(*) FROM routes WHERE id=?`, id); n != 0 {
		t.Errorf("routd.db route rows after delete = %d, want 0", n)
	}
}

// TestRouteTokenIssue_TargetsRoutdDB: issuing a route token writes route_tokens
// into routd.db and the issued token appears in the page.
func TestRouteTokenIssue_TargetsRoutdDB(t *testing.T) {
	d, msg, routd := splitAdminDash(t, "alice@x")
	mux := newMux(d)

	form := url.Values{"kind": {"chat"}}.Encode()
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, adminReq("POST", "/dash/tokens/team/", form, "alice@x"))
	if w.Code != 200 {
		t.Fatalf("POST tokens = %d body=%q", w.Code, w.Body.String())
	}
	if n := count(t, routd, `SELECT COUNT(*) FROM route_tokens WHERE jid='web:team' AND owner_folder='team'`); n != 1 {
		t.Errorf("routd.db route_token rows = %d, want 1", n)
	}
	if n := count(t, msg, `SELECT COUNT(*) FROM route_tokens`); n != 0 {
		t.Errorf("messages.db route_token rows = %d, want 0", n)
	}
}

// TestTaskCreate_TargetsRoutdDB: creating a scheduled task inserts the row into
// routd.db, not messages.db (routd OWNS scheduled_tasks in the split topology).
func TestTaskCreate_TargetsRoutdDB(t *testing.T) {
	d, msg, routd := splitAdminDash(t, "alice@x")
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
	if n := count(t, routd, `SELECT COUNT(*) FROM scheduled_tasks WHERE owner='team' AND prompt='do the thing'`); n != 1 {
		t.Errorf("routd.db scheduled_tasks rows = %d, want 1", n)
	}
	if n := count(t, msg, `SELECT COUNT(*) FROM scheduled_tasks WHERE owner='team'`); n != 0 {
		t.Errorf("messages.db scheduled_tasks rows = %d, want 0 (must not write the monolith)", n)
	}
}

// TestTaskAction_TargetsRoutdDB: pausing a task updates its status in routd.db.
func TestTaskAction_TargetsRoutdDB(t *testing.T) {
	d, msg, routd := splitAdminDash(t, "alice@x")
	// Seed the same task id in BOTH DBs; only routd's row should flip to paused.
	for _, db := range []*sql.DB{msg, routd} {
		if _, err := db.Exec(
			`INSERT INTO scheduled_tasks (id, owner, chat_jid, prompt, status, created_at)
			 VALUES ('t-1', 'team', 'web:team', 'p', 'active', '')`); err != nil {
			t.Fatal(err)
		}
	}
	mux := newMux(d)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, adminReq("POST", "/dash/tasks/t-1/pause", "", "alice@x"))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST task pause = %d body=%q", w.Code, w.Body.String())
	}
	if got := statusOf(t, routd, "t-1"); got != "paused" {
		t.Errorf("routd.db task status = %q, want paused", got)
	}
	if got := statusOf(t, msg, "t-1"); got != "active" {
		t.Errorf("messages.db task status = %q, want active (must not touch the monolith)", got)
	}
}

// TestTaskList_ReadsRoutdDB: the tasks list renders a task seeded into routd.db.
func TestTaskList_ReadsRoutdDB(t *testing.T) {
	d, _, routd := splitAdminDash(t, "alice@x")
	if _, err := routd.Exec(
		`INSERT INTO scheduled_tasks (id, owner, chat_jid, prompt, status, created_at)
		 VALUES ('t-live', 'liveowner', 'web:team', 'p', 'active', '')`); err != nil {
		t.Fatal(err)
	}
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

func statusOf(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var s string
	if err := db.QueryRow(`SELECT status FROM scheduled_tasks WHERE id=?`, id).Scan(&s); err != nil {
		t.Fatalf("status of %s: %v", id, err)
	}
	return s
}

// TestRouteTokenRevoke_TargetsRoutdDB: revoking a token deletes it from routd.db.
func TestRouteTokenRevoke_TargetsRoutdDB(t *testing.T) {
	d, _, routd := splitAdminDash(t, "alice@x")
	if _, err := routd.Exec(
		`INSERT INTO route_tokens (token_hash, jid, owner_folder, created_at)
		 VALUES (x'00', 'web:team', 'team', '')`); err != nil {
		t.Fatal(err)
	}
	mux := newMux(d)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, adminReq("POST", "/dash/tokens/team/"+encodeJID("web:team")+"/revoke", "", "alice@x"))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST token revoke = %d body=%q", w.Code, w.Body.String())
	}
	if n := count(t, routd, `SELECT COUNT(*) FROM route_tokens WHERE jid='web:team'`); n != 0 {
		t.Errorf("routd.db route_token rows after revoke = %d, want 0", n)
	}
}

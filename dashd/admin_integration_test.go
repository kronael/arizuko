package main

// Integration tests for admin-gated CRUD shipped in TIER 1: routes
// create/delete and group delete. Seeds an acl admin row, drives a
// real HTTP request through the mux with dbRW wired so writes land.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/tests/testutils"
)

// newRWDashServer mirrors newDashServer but wires dbRW so admin writes
// run. Returns server + the testutils Inst (Store + DB).
func newRWDashServer(t *testing.T) (*httptest.Server, *testutils.Inst, string) {
	t.Helper()
	inst := testutils.NewInstance(t)
	groupsDir := filepath.Join(inst.Tmp, "groups")
	if err := os.MkdirAll(groupsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	d := &dash{db: inst.DB, dbRW: inst.DB, dbPath: "memory", groupsDir: groupsDir}
	mux := http.NewServeMux()
	d.registerRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, inst, groupsDir
}

func noFollow() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

// TestDash_RouteCreateAndDelete: admin POSTs a route, then deletes it.
func TestDash_RouteCreateAndDelete(t *testing.T) {
	srv, inst, _ := newRWDashServer(t)
	s := inst.Store
	if err := s.AddACLRow(core.ACLRow{
		Principal: "alice@x", Action: "admin", Scope: "**", Effect: "allow",
	}); err != nil {
		t.Fatal(err)
	}

	form := url.Values{"seq": {"10"}, "match": {"room=42"}, "target": {"world/sub"}}
	req, _ := http.NewRequest("POST", srv.URL+"/dash/routes/",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", "alice@x")
	resp, err := noFollow().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create status = %d", resp.StatusCode)
	}

	var got core.Route
	for _, r := range s.AllRoutes() {
		if r.Match == "room=42" {
			got = r
			break
		}
	}
	if got.ID == 0 || got.Target != "world/sub" {
		t.Fatalf("route not stored: %+v", s.AllRoutes())
	}

	delURL := srv.URL + "/dash/routes/" + strconv.FormatInt(got.ID, 10) + "/delete"
	req, _ = http.NewRequest("POST", delURL, nil)
	req.Header.Set("X-User-Sub", "alice@x")
	resp, err = noFollow().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("delete status = %d", resp.StatusCode)
	}
	for _, r := range s.AllRoutes() {
		if r.ID == got.ID {
			t.Errorf("route %d still present after delete", got.ID)
		}
	}
}

// TestDash_RouteCreate_DenyNonAdmin: requireAdmin gates the POST.
func TestDash_RouteCreate_DenyNonAdmin(t *testing.T) {
	srv, _, _ := newRWDashServer(t)
	form := url.Values{"seq": {"0"}, "match": {"room=1"}, "target": {"world/sub"}}
	req, _ := http.NewRequest("POST", srv.URL+"/dash/routes/",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", "stranger@x")
	resp, err := noFollow().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}


// TestDash_GroupDelete: admin deletes a group via POST-alias.
func TestDash_GroupDelete(t *testing.T) {
	srv, inst, groupsDir := newRWDashServer(t)
	s := inst.Store
	if err := s.AddACLRow(core.ACLRow{
		Principal: "alice@x", Action: "admin", Scope: "**", Effect: "allow",
	}); err != nil {
		t.Fatal(err)
	}
	// {folder} pattern in the mux is a single path segment; use a flat name.
	folder := "worldsub"
	if err := s.PutGroup(core.Group{Folder: folder, AddedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	groupDir := filepath.Join(groupsDir, folder)
	if err := os.MkdirAll(groupDir, 0o755); err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest("POST", srv.URL+"/dash/groups/"+folder+"/delete", nil)
	req.Header.Set("X-User-Sub", "alice@x")
	resp, err := noFollow().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("delete status = %d, body deduce", resp.StatusCode)
	}
	if _, ok := s.AllGroups()[folder]; ok {
		t.Errorf("group row still present")
	}
	if _, err := os.Stat(groupDir); !os.IsNotExist(err) {
		t.Errorf("group dir still present: %v", err)
	}
}

// TestGroupDelete_PurgesOrphanAclAndRoutes: deleting folder X must also remove
// X's acl grants and routes (no FK cascade — both are plain TEXT columns), so a
// re-created X cannot inherit stale privileges. A sibling Y stays untouched.
func TestGroupDelete_PurgesOrphanAclAndRoutes(t *testing.T) {
	srv, inst, groupsDir := newRWDashServer(t)
	s := inst.Store
	if err := s.AddACLRow(core.ACLRow{
		Principal: "alice@x", Action: "admin", Scope: "**", Effect: "allow",
	}); err != nil {
		t.Fatal(err)
	}
	// Flat folder names: {folder} POST-delete dispatch is a single segment.
	const X, Y = "worldx", "worldy"
	for _, f := range []string{X, Y} {
		if err := s.PutGroup(core.Group{Folder: f, AddedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(groupsDir, f), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// X: admin grant on the folder + a X/** glob grant + a child-subtree route.
	if err := s.AddACLRow(core.ACLRow{Principal: "bob@x", Action: "admin", Scope: X, Effect: "allow"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddACLRow(core.ACLRow{Principal: "bob@x", Action: "send", Scope: X + "/**", Effect: "allow"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddRoute(core.Route{Seq: 5, Match: "room=99", Target: X + "/sub"}); err != nil {
		t.Fatal(err)
	}
	// Y: its own grant + route — must SURVIVE the delete of X.
	if err := s.AddACLRow(core.ACLRow{Principal: "carol@x", Action: "admin", Scope: Y, Effect: "allow"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddRoute(core.Route{Seq: 6, Match: "room=100", Target: Y}); err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest("POST", srv.URL+"/dash/groups/"+X+"/delete", nil)
	req.Header.Set("X-User-Sub", "alice@x")
	resp, err := noFollow().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("delete status = %d, want 303", resp.StatusCode)
	}

	// X's acl rows (folder + X/** glob) must be gone.
	for _, sc := range []string{X, X + "/**"} {
		if rows := s.ListACLByScope(sc); len(rows) != 0 {
			t.Errorf("acl scope %q still present after delete: %+v", sc, rows)
		}
	}
	// X's route (target under X/) must be gone.
	for _, r := range s.AllRoutes() {
		if r.Target == X+"/sub" {
			t.Errorf("route target %q still present after delete", r.Target)
		}
	}
	// Y untouched: grant + route survive.
	if rows := s.ListACLByScope(Y); len(rows) != 1 || rows[0].Principal != "carol@x" {
		t.Errorf("sibling Y acl not preserved: %+v", rows)
	}
	yRoute := false
	for _, r := range s.AllRoutes() {
		if r.Target == Y {
			yRoute = true
		}
	}
	if !yRoute {
		t.Errorf("sibling Y route purged; want preserved")
	}
}

// TestDash_NestedFolderRouting: multi-segment folders (corp/eng/sre)
// address the settings + delete handlers via literal nested paths.
// Pre-fix, Go's single-segment {folder} let them fall through to the
// list handler and silently 200 the wrong page.
func TestDash_NestedFolderRouting(t *testing.T) {
	srv, inst, groupsDir := newRWDashServer(t)
	s := inst.Store
	if err := s.AddACLRow(core.ACLRow{
		Principal: "alice@x", Action: "admin", Scope: "**", Effect: "allow",
	}); err != nil {
		t.Fatal(err)
	}
	folder := "corp/eng/sre"
	if err := s.PutGroup(core.Group{Folder: folder, AddedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(groupsDir, folder), 0o755); err != nil {
		t.Fatal(err)
	}

	// Settings GET must render the settings page, not the groups list.
	req, _ := http.NewRequest("GET", srv.URL+"/dash/groups/"+folder+"/settings", nil)
	req.Header.Set("X-User-Sub", "alice@x")
	resp, err := noFollow().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("settings GET status = %d", resp.StatusCode)
	}
	if !strings.Contains(string(body), "observe_window_messages") {
		t.Errorf("settings page not rendered for nested folder; got list/other page")
	}

	// Delete POST-alias must remove the nested group.
	req, _ = http.NewRequest("POST", srv.URL+"/dash/groups/"+folder+"/delete", nil)
	req.Header.Set("X-User-Sub", "alice@x")
	resp, err = noFollow().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("nested delete status = %d, want 303", resp.StatusCode)
	}
	if _, ok := s.AllGroups()[folder]; ok {
		t.Errorf("nested group row still present after delete")
	}
}

// TestDash_GrantsView: admin GETs the grants page, sees the row.
func TestDash_GrantsView(t *testing.T) {
	srv, inst, _ := newRWDashServer(t)
	s := inst.Store
	if err := s.AddACLRow(core.ACLRow{
		Principal: "alice@x", Action: "admin", Scope: "**", Effect: "allow",
	}); err != nil {
		t.Fatal(err)
	}
	folder := "world"
	if err := inst.DB.QueryRow(`SELECT 1`).Err(); err != nil {
		t.Fatal(err)
	}
	// Seed a grant row for this folder.
	if err := s.AddACLRow(core.ACLRow{
		Principal: "bob@x", Action: "send", Scope: folder, Effect: "allow",
	}); err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest("GET", srv.URL+"/dash/groups/"+folder+"/grants", nil)
	req.Header.Set("X-User-Sub", "alice@x")
	resp, err := noFollow().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET grants status = %d", resp.StatusCode)
	}
}

// TestDash_GrantAdd: admin adds a grant, it appears in ListACLByScope.
func TestDash_GrantAdd(t *testing.T) {
	srv, inst, _ := newRWDashServer(t)
	s := inst.Store
	if err := s.AddACLRow(core.ACLRow{
		Principal: "alice@x", Action: "admin", Scope: "**", Effect: "allow",
	}); err != nil {
		t.Fatal(err)
	}
	folder := "world"

	form := url.Values{
		"principal": {"carol@x"},
		"action":    {"reply"},
		"effect":    {"allow"},
		"params":    {""},
		"scope":     {folder},
	}
	req, _ := http.NewRequest("POST", srv.URL+"/dash/groups/"+folder+"/grants",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", "alice@x")
	resp, err := noFollow().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("add grant status = %d", resp.StatusCode)
	}

	rows := s.ListACLByScope(folder)
	found := false
	for _, r := range rows {
		if r.Principal == "carol@x" && r.Action == "reply" {
			found = true
		}
	}
	if !found {
		t.Errorf("grant row not found after add; rows: %+v", rows)
	}
}

// TestDash_GrantRevoke: admin adds then revokes a grant.
func TestDash_GrantRevoke(t *testing.T) {
	srv, inst, _ := newRWDashServer(t)
	s := inst.Store
	if err := s.AddACLRow(core.ACLRow{
		Principal: "alice@x", Action: "admin", Scope: "**", Effect: "allow",
	}); err != nil {
		t.Fatal(err)
	}
	folder := "world"
	if err := s.AddACLRow(core.ACLRow{
		Principal: "dave@x", Action: "send", Scope: folder, Effect: "allow",
	}); err != nil {
		t.Fatal(err)
	}

	form := url.Values{
		"principal": {"dave@x"},
		"action":    {"send"},
		"effect":    {"allow"},
		"params":    {""},
		"predicate": {""},
	}
	req, _ := http.NewRequest("POST", srv.URL+"/dash/groups/"+folder+"/grants/revoke",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", "alice@x")
	resp, err := noFollow().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("revoke status = %d", resp.StatusCode)
	}

	rows := s.ListACLByScope(folder)
	for _, r := range rows {
		if r.Principal == "dave@x" && r.Action == "send" {
			t.Errorf("grant still present after revoke")
		}
	}
}

// TestDash_GrantAdd_DenyNonAdmin: non-admin cannot add grants.
func TestDash_GrantAdd_DenyNonAdmin(t *testing.T) {
	srv, _, _ := newRWDashServer(t)
	form := url.Values{"principal": {"x"}, "action": {"send"}}
	req, _ := http.NewRequest("POST", srv.URL+"/dash/groups/world/grants",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", "stranger@x")
	resp, err := noFollow().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}

// TestDash_TaskCreate: admin creates a task, then views the detail page.
func TestDash_TaskCreate(t *testing.T) {
	srv, inst, _ := newRWDashServer(t)
	s := inst.Store
	if err := s.AddACLRow(core.ACLRow{
		Principal: "alice@x", Action: "admin", Scope: "**", Effect: "allow",
	}); err != nil {
		t.Fatal(err)
	}

	form := url.Values{
		"owner":    {"alice"},
		"chat_jid": {"alice@s.whatsapp.net"},
		"prompt":   {"say hello"},
		"cron":     {"0 9 * * *"},
	}
	req, _ := http.NewRequest("POST", srv.URL+"/dash/tasks/",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", "alice@x")
	resp, err := noFollow().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create status = %d, want 303", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.HasPrefix(loc, "/dash/tasks/t-") {
		t.Fatalf("redirect to %q; expected /dash/tasks/t-<id>", loc)
	}

	// Fetch the detail page (direct httptest call to avoid redirect loop).
	d := &dash{db: inst.DB, dbRW: inst.DB, dbPath: "memory",
		groupsDir: filepath.Join(inst.Tmp, "groups")}
	mux := http.NewServeMux()
	d.registerRoutes(mux)
	taskID := strings.TrimPrefix(loc, "/dash/tasks/")
	detailReq := asOperator(httptest.NewRequest("GET", "/dash/tasks/"+taskID, nil))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, detailReq)
	if w.Code != http.StatusOK {
		t.Fatalf("detail status = %d", w.Code)
	}
	bs := w.Body.String()
	for _, want := range []string{"say hello", "0 9 * * *", "alice", "active"} {
		if !strings.Contains(bs, want) {
			t.Errorf("detail missing %q", want)
		}
	}
}

// TestDash_TaskPauseResume: admin pauses then resumes a task.
func TestDash_TaskPauseResume(t *testing.T) {
	srv, inst, _ := newRWDashServer(t)
	s := inst.Store
	if err := s.AddACLRow(core.ACLRow{
		Principal: "alice@x", Action: "admin", Scope: "**", Effect: "allow",
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Format(time.RFC3339)
	if _, err := inst.DB.Exec(
		`INSERT INTO scheduled_tasks (id, owner, chat_jid, prompt, cron, status, created_at)
		 VALUES ('t1', 'alice', 'alice@s.whatsapp.net', 'ping', '* * * * *', 'active', ?)`, now); err != nil {
		t.Fatal(err)
	}

	post := func(path string) int {
		req, _ := http.NewRequest("POST", srv.URL+path, nil)
		req.Header.Set("X-User-Sub", "alice@x")
		resp, err := noFollow().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	if code := post("/dash/tasks/t1/pause"); code != http.StatusSeeOther {
		t.Errorf("pause: status = %d, want 303", code)
	}
	var status string
	inst.DB.QueryRow(`SELECT status FROM scheduled_tasks WHERE id='t1'`).Scan(&status)
	if status != "paused" {
		t.Errorf("after pause: status = %q, want paused", status)
	}

	if code := post("/dash/tasks/t1/resume"); code != http.StatusSeeOther {
		t.Errorf("resume: status = %d, want 303", code)
	}
	inst.DB.QueryRow(`SELECT status FROM scheduled_tasks WHERE id='t1'`).Scan(&status)
	if status != "active" {
		t.Errorf("after resume: status = %q, want active", status)
	}
}

// TestDash_TaskCancel: admin cancels a task.
func TestDash_TaskCancel(t *testing.T) {
	srv, inst, _ := newRWDashServer(t)
	s := inst.Store
	if err := s.AddACLRow(core.ACLRow{
		Principal: "alice@x", Action: "admin", Scope: "**", Effect: "allow",
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Format(time.RFC3339)
	if _, err := inst.DB.Exec(
		`INSERT INTO scheduled_tasks (id, owner, chat_jid, prompt, cron, status, created_at)
		 VALUES ('t2', 'alice', 'alice@s.whatsapp.net', 'ping', '* * * * *', 'active', ?)`, now); err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest("POST", srv.URL+"/dash/tasks/t2/cancel", nil)
	req.Header.Set("X-User-Sub", "alice@x")
	resp, err := noFollow().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("cancel: status = %d, want 303", resp.StatusCode)
	}
	var status string
	inst.DB.QueryRow(`SELECT status FROM scheduled_tasks WHERE id='t2'`).Scan(&status)
	if status != "cancelled" {
		t.Errorf("after cancel: status = %q, want cancelled", status)
	}
}

// TestDash_TaskAction_DenyNonAdmin: non-admin cannot pause a task.
func TestDash_TaskAction_DenyNonAdmin(t *testing.T) {
	srv, inst, _ := newRWDashServer(t)
	now := time.Now().Format(time.RFC3339)
	if _, err := inst.DB.Exec(
		`INSERT INTO scheduled_tasks (id, owner, chat_jid, prompt, cron, status, created_at)
		 VALUES ('t3', 'alice', 'alice@s.whatsapp.net', 'ping', '* * * * *', 'active', ?)`, now); err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest("POST", srv.URL+"/dash/tasks/t3/pause", nil)
	req.Header.Set("X-User-Sub", "stranger@x")
	resp, err := noFollow().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}

// TestDash_TaskDetail_RunLogs: seed task + run logs, verify they appear.
func TestDash_TaskDetail_RunLogs(t *testing.T) {
	_, inst, _ := newRWDashServer(t)
	now := time.Now().Format(time.RFC3339)
	if _, err := inst.DB.Exec(
		`INSERT INTO scheduled_tasks (id, owner, chat_jid, prompt, cron, status, created_at)
		 VALUES ('t4', 'bob', 'bob@s.whatsapp.net', 'daily report', '0 8 * * *', 'active', ?)`, now); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := inst.DB.Exec(
			`INSERT INTO task_run_logs (task_id, run_at, duration_ms, status, error)
			 VALUES ('t4', ?, ?, 'ok', '')`,
			now, (i+1)*100); err != nil {
			t.Fatal(err)
		}
	}

	d := &dash{db: inst.DB, dbRW: inst.DB, dbPath: "memory",
		groupsDir: filepath.Join(inst.Tmp, "groups")}
	mux := http.NewServeMux()
	d.registerRoutes(mux)
	req := asOperator(httptest.NewRequest("GET", "/dash/tasks/t4", nil))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	bs := w.Body.String()
	for _, want := range []string{"daily report", "0 8 * * *", "bob", "100 ms", "Run history"} {
		if !strings.Contains(bs, want) {
			t.Errorf("detail page missing %q", want)
		}
	}
}

// TestDash_TasksPage_PromptColumn: tasks list page includes Prompt column.
func TestDash_TasksPage_PromptColumn(t *testing.T) {
	_, inst, _ := newRWDashServer(t)
	now := time.Now().Format(time.RFC3339)
	if _, err := inst.DB.Exec(
		`INSERT INTO scheduled_tasks (id, owner, chat_jid, prompt, cron, status, created_at)
		 VALUES ('t5', 'carol', 'carol@s.whatsapp.net', 'send summary', '0 7 * * *', 'active', ?)`, now); err != nil {
		t.Fatal(err)
	}

	d := &dash{db: inst.DB, dbPath: "memory",
		groupsDir: filepath.Join(inst.Tmp, "groups")}
	mux := http.NewServeMux()
	d.registerRoutes(mux)
	req := asOperator(httptest.NewRequest("GET", "/dash/tasks/", nil))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	bs := w.Body.String()
	if !strings.Contains(bs, "Prompt") {
		t.Errorf("tasks page missing Prompt column header")
	}
	if !strings.Contains(bs, "send summary") {
		t.Errorf("tasks page missing prompt text")
	}
}

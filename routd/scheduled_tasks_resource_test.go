package routd

// Parity tests for the spec 5/16 scheduled_tasks migration: the agent's
// schedule_task/pause_task/resume_task/cancel_task/list_tasks now ride resreg's
// MCP mechanism through the ServeMCP postBuild seam instead of five hand-rolled
// ipc bodies. Each test drives the REAL unix socket end-to-end (not the handler
// directly) so the seam + the injected contain seam (auth.Authorize on the resolved
// task owner) + Visible predicate are all exercised.
//
// 5/33: no task tool is in the role:member floor, so every test delegates the ones it
// drives with grantMCPTools (scope <folder>/**). The PER-TASK-ID containment is what
// still denies a caller acting on another folder's task;
// TestScheduledTasksMCP_CrossFolderTaskDenied is the one that fails if the contain
// seam is dropped (the routes/acl-class containment regression).

import (
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/ipc"
)

// taskToolNames is the five task tools — the set every task test delegates.
var taskToolNames = []string{"schedule_task", "pause_task", "resume_task", "cancel_task", "list_tasks"}

// serveTasksMCP stands up the agent socket for folder + the scheduled_tasks resreg
// seam, and returns the socket path. Authz reads the folder's acl rows.
func serveTasksMCP(t *testing.T, db *DB, folder, callerSub string) string {
	t.Helper()
	srv := NewServer(db, nil, nil, nil, 0, "")
	ipcDir := t.TempDir()
	sock := groupfolder.IpcSocket(ipcDir)
	pb := srv.scheduledTasksPostBuild(folder, callerSub, srv.db.Authorize,
		agentVisibleFor(srv, callerSub, false), auth.Resolve(folder))
	stop, err := ipc.ServeMCP(sock, srv.buildGatedFns(turnMCP{folder: folder}),
		srv.buildStoreFns(turnMCP{folder: folder}), folder, false, 0, callerSub, pb)
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	t.Cleanup(stop)
	deadline := time.Now().Add(2 * time.Second)
	for !fileExists(sock) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	return sock
}

// serveTasksMCPElevated stands up the agent socket as an operator /root turn would:
// an allow-all row-ACL (turnAuthorize(true)), an all-visible view and a ROOT
// effective identity (turnIdentity(folder, true)) — the exact wiring ServeTurnMCP
// hands the postBuild for an elevated turn.
func serveTasksMCPElevated(t *testing.T, db *DB, folder string) string {
	t.Helper()
	srv := NewServer(db, nil, nil, nil, 0, "")
	ipcDir := t.TempDir()
	sock := groupfolder.IpcSocket(ipcDir)
	callerSub := "folder:" + folder
	pb := srv.scheduledTasksPostBuild(folder, callerSub, srv.turnAuthorize(true),
		agentVisibleFor(srv, callerSub, true), turnIdentity(folder, true))
	stop, err := ipc.ServeMCP(sock, srv.buildGatedFns(turnMCP{folder: folder}),
		srv.buildStoreFns(turnMCP{folder: folder}), folder, true, 0, callerSub, pb)
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	t.Cleanup(stop)
	deadline := time.Now().Add(2 * time.Second)
	for !fileExists(sock) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	return sock
}

// TestScheduledTasksMCP_ScheduleParsesCron: schedule_task folds in the three
// `cron` forms (5-field cron / ms interval / RFC3339 one-shot) + rejects a bad
// expr. A one-shot stores an EMPTY cron (so timed completes it after one firing);
// a broken parser that stored the RFC3339 string as cron is caught here.
func TestScheduledTasksMCP_ScheduleParsesCron(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "hq"})
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "hq"}})
	const jid = "slack:team/channel/c1"
	grantMCPTools(t, db, "hq", taskToolNames...)
	sock := serveTasksMCP(t, db, "hq", "folder:hq")

	// 5-field cron: stored verbatim, next_run computed.
	res, e := callToolOverSock(t, sock, "schedule_task", map[string]any{
		"targetJid": jid, "prompt": "standup", "cron": "0 9 * * *",
	})
	if e != "" {
		t.Fatalf("schedule_task (cron) errored: %s", e)
	}
	tk, _ := db.GetTask(res["taskId"].(string))
	if tk.Cron != "0 9 * * *" || tk.NextRun == nil {
		t.Fatalf("cron form: cron=%q next_run=%v, want cron kept + next_run set", tk.Cron, tk.NextRun)
	}

	// Millisecond interval: stored verbatim, next_run ~now+interval.
	res, e = callToolOverSock(t, sock, "schedule_task", map[string]any{
		"targetJid": jid, "prompt": "ping", "cron": "60000",
	})
	if e != "" {
		t.Fatalf("schedule_task (interval) errored: %s", e)
	}
	tk, _ = db.GetTask(res["taskId"].(string))
	if tk.Cron != "60000" || tk.NextRun == nil {
		t.Fatalf("interval form: cron=%q next_run=%v, want interval kept + next_run set", tk.Cron, tk.NextRun)
	}

	// RFC3339 one-shot: EMPTY stored cron, next_run = the timestamp.
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	res, e = callToolOverSock(t, sock, "schedule_task", map[string]any{
		"targetJid": jid, "prompt": "one-shot", "cron": future,
	})
	if e != "" {
		t.Fatalf("schedule_task (one-shot) errored: %s", e)
	}
	tk, _ = db.GetTask(res["taskId"].(string))
	if tk.Cron != "" || tk.NextRun == nil {
		t.Fatalf("one-shot form: cron=%q next_run=%v, want EMPTY cron + next_run set", tk.Cron, tk.NextRun)
	}

	// Invalid cron: rejected, nothing persisted.
	before := len(db.Tasks("hq", true))
	if _, e := callToolOverSock(t, sock, "schedule_task", map[string]any{
		"targetJid": jid, "prompt": "bad", "cron": "not-a-cron-expr",
	}); !strings.Contains(e, "invalid cron") {
		t.Fatalf("invalid cron: got %q, want 'invalid cron'", e)
	}
	if got := len(db.Tasks("hq", true)); got != before {
		t.Fatalf("invalid cron persisted a task: %d → %d", before, got)
	}
}

// TestScheduledTasksMCP_ScheduleDedup: an active task with the same cron + prompt
// is returned as-is, never duplicated — the deleted body's dedup guard.
func TestScheduledTasksMCP_ScheduleDedup(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "hq"})
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "hq"}})
	const jid = "slack:team/channel/c1"
	grantMCPTools(t, db, "hq", taskToolNames...)
	sock := serveTasksMCP(t, db, "hq", "folder:hq")

	args := map[string]any{"targetJid": jid, "prompt": "daily", "cron": "0 9 * * *"}
	first, e := callToolOverSock(t, sock, "schedule_task", args)
	if e != "" {
		t.Fatalf("first schedule_task errored: %s", e)
	}
	second, e := callToolOverSock(t, sock, "schedule_task", args)
	if e != "" {
		t.Fatalf("second schedule_task errored: %s", e)
	}
	if first["taskId"] != second["taskId"] {
		t.Fatalf("dedup: second id %v != first %v", second["taskId"], first["taskId"])
	}
	if got := len(db.Tasks("hq", true)); got != 1 {
		t.Fatalf("dedup: %d tasks, want 1", got)
	}
}

// TestScheduledTasksMCP_ListOwnFolderOnly: a non-root folder's list_tasks returns
// only its own tasks, never another folder's — the ownership filter.
func TestScheduledTasksMCP_ListOwnFolderOnly(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, f := range []string{"world", "world/a", "world/b"} {
		_ = db.PutGroup(core.Group{Folder: f})
	}
	seedTask(t, db, "task-a", "world/a", "slack:a", "mine")
	seedTask(t, db, "task-b", "world/b", "slack:b", "theirs")
	grantMCPTools(t, db, "world/a", taskToolNames...)
	sock := serveTasksMCP(t, db, "world/a", "folder:world/a")

	arr, e := callToolArray(t, sock, "list_tasks", nil)
	if e != "" {
		t.Fatalf("list_tasks errored: %s", e)
	}
	if len(arr) != 1 {
		t.Fatalf("list_tasks returned %d tasks, want 1 (own folder only): %v", len(arr), arr)
	}
	if m, _ := arr[0].(map[string]any); m["Owner"] != "world/a" {
		t.Fatalf("list_tasks leaked another folder's task: %v", arr[0])
	}
}

// TestScheduledTasksMCP_PauseResumeCancelOwn: pause/resume/cancel on the caller's
// OWN task work end-to-end for a folder delegated the task tools.
func TestScheduledTasksMCP_PauseResumeCancelOwn(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "world"})
	_ = db.PutGroup(core.Group{Folder: "world/a"})
	seedTask(t, db, "task-a", "world/a", "slack:a", "mine")
	grantMCPTools(t, db, "world/a", taskToolNames...)
	sock := serveTasksMCP(t, db, "world/a", "folder:world/a")

	if _, e := callToolOverSock(t, sock, "pause_task", map[string]any{"taskId": "task-a"}); e != "" {
		t.Fatalf("pause_task errored: %s", e)
	}
	if got, _ := db.GetTask("task-a"); got.Status != core.TaskPaused {
		t.Fatalf("pause_task: status=%q want paused", got.Status)
	}
	if _, e := callToolOverSock(t, sock, "resume_task", map[string]any{"taskId": "task-a"}); e != "" {
		t.Fatalf("resume_task errored: %s", e)
	}
	if got, _ := db.GetTask("task-a"); got.Status != core.TaskActive {
		t.Fatalf("resume_task: status=%q want active", got.Status)
	}
	if _, e := callToolOverSock(t, sock, "cancel_task", map[string]any{"taskId": "task-a"}); e != "" {
		t.Fatalf("cancel_task errored: %s", e)
	}
	if _, ok := db.GetTask("task-a"); ok {
		t.Fatal("cancel_task: task-a still present")
	}
}

// TestScheduledTasksMCP_CrossFolderTaskDenied: THE per-task containment guard. A
// folder GRANTED cancel_task scoped to itself may cancel its OWN task but MUST NOT
// cancel a sibling folder's task — contain resolves the task's owner from
// GetTask(id) and auth.Authorize denies the out-of-scope target. Fails on a
// migration that drops the contain seam (the sibling task would be deleted).
func TestScheduledTasksMCP_CrossFolderTaskDenied(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "world/a/b"}) // caller
	_ = db.PutGroup(core.Group{Folder: "world/a/c"}) // sibling
	// The operator delegates cancel_task scoped EXACTLY to the caller's own folder —
	// so the tool is held and visible, and only the per-task target can still deny.
	if err := db.AddACLRow(core.ACLRow{
		Principal: "folder:world/a/b", Action: "mcp:cancel_task",
		Scope: "world/a/b", Effect: "allow",
	}); err != nil {
		t.Fatalf("AddACLRow: %v", err)
	}
	seedTask(t, db, "own", "world/a/b", "slack:own", "mine")
	seedTask(t, db, "sibling", "world/a/c", "slack:sib", "theirs")
	sock := serveTasksMCP(t, db, "world/a/b", "folder:world/a/b")

	// Visible (operator granted it), but a sibling's task is denied by the cap.
	if !listToolNames(t, sock)["cancel_task"] {
		t.Fatal("cancel_task should be visible (operator granted it)")
	}
	if _, e := callToolOverSock(t, sock, "cancel_task", map[string]any{"taskId": "sibling"}); e == "" {
		t.Fatal("cancel of a SIBLING folder's task must be denied")
	}
	if _, ok := db.GetTask("sibling"); !ok {
		t.Fatal("denied cross-folder cancel still deleted the sibling task")
	}
	// Own task: allowed — proves the gate isn't blanket-denying.
	if _, e := callToolOverSock(t, sock, "cancel_task", map[string]any{"taskId": "own"}); e != "" {
		t.Fatalf("cancel of own task errored: %s", e)
	}
	if _, ok := db.GetTask("own"); ok {
		t.Fatal("own task not cancelled")
	}
}

// TestScheduledTasksMCP_RootElevationCancelsCrossFolder is the elevated
// counterpart to TestScheduledTasksMCP_CrossFolderTaskDenied: a scoped folder
// cannot cancel a sibling's task over the plain agent socket, but an operator /root
// turn from that SAME folder can — elevation swaps in the allow-all authorize.
// Regression guard for the class of bug d452d6ef fixed.
func TestScheduledTasksMCP_RootElevationCancelsCrossFolder(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "world/a/b"}) // caller
	_ = db.PutGroup(core.Group{Folder: "world/a/c"}) // sibling
	seedTask(t, db, "sibling", "world/a/c", "slack:sib", "theirs")
	sock := serveTasksMCPElevated(t, db, "world/a/b")

	if _, e := callToolOverSock(t, sock, "cancel_task", map[string]any{"taskId": "sibling"}); e != "" {
		t.Fatalf("/root cancel of a sibling folder's task should be allowed: %s", e)
	}
	if _, ok := db.GetTask("sibling"); ok {
		t.Fatal("/root cancel_task did not delete the sibling task")
	}
}

// TestScheduledTasksMCP_Visibility: the Visible predicate (auth.EffectiveActions)
// keeps tools/list gating — a delegated folder sees the five tools; an ungranted
// folder sees none (no task tool is in the role:member floor).
func TestScheduledTasksMCP_Visibility(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "hq"})
	_ = db.PutGroup(core.Group{Folder: "world/a/b"})
	grantMCPTools(t, db, "hq", taskToolNames...)

	granted := listToolNames(t, serveTasksMCP(t, db, "hq", "folder:hq"))
	for _, name := range taskToolNames {
		if !granted[name] {
			t.Fatalf("%s not visible to a folder granted it", name)
		}
	}
	ungranted := listToolNames(t, serveTasksMCP(t, db, "world/a/b", "folder:world/a/b"))
	for _, name := range taskToolNames {
		if ungranted[name] {
			t.Fatalf("%s visible to a folder that isn't granted it", name)
		}
	}
}

// TestScheduledTasksMCP_GateDenies: a tool that is VISIBLE (an allow row grants it)
// but overridden by a DENY row is rejected at call time by the contain seam
// (auth.Authorize is deny-wins), before the write runs. The route is seeded so the
// target folder RESOLVES — otherwise the handler's "target group not registered"
// 400 would mask the authz decision.
func TestScheduledTasksMCP_GateDenies(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "hq"})
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "hq"}})
	grantMCPTools(t, db, "hq", "schedule_task")
	// A deny row on the same (tool, scope) wins over the allow — visible, uncallable.
	if err := db.AddACLRow(core.ACLRow{
		Principal: "folder:hq", Action: "mcp:schedule_task", Scope: "hq/**", Effect: "deny",
	}); err != nil {
		t.Fatalf("AddACLRow: %v", err)
	}
	sock := serveTasksMCP(t, db, "hq", "folder:hq")

	if !listToolNames(t, sock)["schedule_task"] {
		t.Fatal("schedule_task should be visible (an allow row grants it)")
	}
	if _, e := callToolOverSock(t, sock, "schedule_task", map[string]any{
		"targetJid": "slack:team/channel/c1", "prompt": "p", "cron": "60000",
	}); e == "" {
		t.Fatal("schedule_task should be denied by the deny row")
	}
	if got := len(db.Tasks("hq", true)); got != 0 {
		t.Fatalf("denied schedule still wrote a task: %d", got)
	}
}

// TestScheduledTasksMCP_AuditRowLands: an agent mutation writes one audit_log row
// in routd.db via resreg's tx-bound EmitInTx (was emitSys/store.CreateTask's own
// task.create row). The action is scheduled_tasks:schedule, surface mcp.
func TestScheduledTasksMCP_AuditRowLands(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "hq"})
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "hq"}})
	grantMCPTools(t, db, "hq", taskToolNames...)
	sock := serveTasksMCP(t, db, "hq", "folder:hq")

	if _, e := callToolOverSock(t, sock, "schedule_task", map[string]any{
		"targetJid": "slack:team/channel/c1", "prompt": "ping", "cron": "60000",
	}); e != "" {
		t.Fatalf("schedule_task errored: %s", e)
	}
	var n int
	err = db.SQL().QueryRow(
		`SELECT count(*) FROM audit_log WHERE action='scheduled_tasks:schedule' AND outcome='ok' AND folder='hq' AND surface='mcp'`,
	).Scan(&n)
	if err != nil {
		t.Fatalf("query audit_log: %v", err)
	}
	if n != 1 {
		t.Fatalf("audit_log has %d scheduled_tasks:schedule rows, want 1", n)
	}
}

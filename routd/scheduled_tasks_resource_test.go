package routd

// Parity tests for the spec 5/16 scheduled_tasks migration: the agent's
// schedule_task/pause_task/resume_task/cancel_task/list_tasks now ride resreg's
// MCP mechanism through the ServeMCP postBuild seam instead of five hand-rolled
// ipc bodies. Each test drives the REAL unix socket end-to-end (not the handler
// directly) so the seam + the injected contain seam (auth.Authorize on the resolved
// task owner) + Visible predicate are all exercised.
//
// 5/33-paths-roles: no task tool is in the role:member floor, so every test delegates the ones it
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
	"github.com/kronael/arizuko/store"
)

// taskToolNames is the six task tools — the set every task test delegates.
var taskToolNames = []string{"schedule_task", "pause_task", "resume_task", "cancel_task", "list_tasks", "reschedule_task"}

// serveTasksMCP stands up the agent socket for folder + the scheduled_tasks resreg
// seam, and returns the socket path. Authz reads the folder's acl rows.
func serveTasksMCP(t *testing.T, db *DB, folder, callerSub string) string {
	t.Helper()
	srv := NewServer(db, nil, nil, nil, 0, "")
	ipcDir := t.TempDir()
	sock := groupfolder.IpcSocket(ipcDir)
	pb := srv.scheduledTasksPostBuild(folder, callerSub, srv.turnAuthorize(turnMCP{folder: folder}),
		srv.turnVisible(turnMCP{folder: folder}), auth.Resolve(folder))
	stop, err := ipc.ServeMCP(sock, srv.buildRoutdFns(turnMCP{folder: folder}),
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
	pb := srv.scheduledTasksPostBuild(folder, callerSub, srv.turnAuthorize(turnMCP{folder: folder, elevated: true}),
		srv.turnVisible(turnMCP{folder: folder, elevated: true}), turnIdentity(folder, true))
	stop, err := ipc.ServeMCP(sock, srv.buildRoutdFns(turnMCP{folder: folder}),
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

	// 5-field cron: stored verbatim, next_run left to the scheduler.
	res, e := callToolOverSock(t, sock, "schedule_task", map[string]any{
		"targetJid": jid, "prompt": "standup", "cron": "0 9 * * *",
	})
	if e != "" {
		t.Fatalf("schedule_task (cron) errored: %s", e)
	}
	tk, _ := db.GetTask(res["taskId"].(string))
	if tk.Cron != "0 9 * * *" || tk.NextRun != nil {
		t.Fatalf("cron form: cron=%q next_run=%v, want cron kept + next_run empty", tk.Cron, tk.NextRun)
	}

	// Millisecond interval: stored verbatim, next_run left to the scheduler.
	res, e = callToolOverSock(t, sock, "schedule_task", map[string]any{
		"targetJid": jid, "prompt": "ping", "cron": "60000",
	})
	if e != "" {
		t.Fatalf("schedule_task (interval) errored: %s", e)
	}
	tk, _ = db.GetTask(res["taskId"].(string))
	if tk.Cron != "60000" || tk.NextRun != nil {
		t.Fatalf("interval form: cron=%q next_run=%v, want interval kept + next_run empty", tk.Cron, tk.NextRun)
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

// TestScheduledTasksMCP_RescheduleOverrideSurvivesTheFire: timed re-arms a task
// when it enqueues the prompt, so a job that calls reschedule_task during its
// own turn writes AFTER that re-arm and its next_run stands. The next poll
// leaves it alone; a refused call (cross-folder, bad time) writes nothing.
func TestScheduledTasksMCP_RescheduleOverrideSurvivesTheFire(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "world"})
	_ = db.PutGroup(core.Group{Folder: "world/a"})
	_ = db.PutGroup(core.Group{Folder: "world/b"})
	seedTask(t, db, "task-a", "world/a", "slack:a", "mine")
	seedTask(t, db, "task-b", "world/b", "slack:b", "theirs")
	grantMCPTools(t, db, "world/a", taskToolNames...)
	sock := serveTasksMCP(t, db, "world/a", "folder:world/a")

	// The scheduler fills each seeded task's first run; two days on, both are due.
	if filled, err := db.store().DueTasks(time.Now()); err != nil || len(filled) != 0 {
		t.Fatalf("first poll claimed %d tasks, err %v; it only fills next_run", len(filled), err)
	}
	later := time.Now().Add(48 * time.Hour)

	// The fire: timed claims the due task, enqueues, and asks routd to re-arm.
	if due, err := db.store().DueTasks(later); err != nil || len(due) != 2 {
		t.Fatalf("DueTasks = %d tasks, err %v; want both seeded tasks", len(due), err)
	}
	if err := db.RescheduleTask("task-a", ""); err != nil {
		t.Fatalf("RescheduleTask: %v", err)
	}

	// The job's own turn: it moves its next run.
	moved := time.Now().Add(72 * time.Hour).UTC().Truncate(time.Second)
	if _, e := callToolOverSock(t, sock, "reschedule_task", map[string]any{
		"taskId": "task-a", "next_run": moved.Format(time.RFC3339),
	}); e != "" {
		t.Fatalf("reschedule_task errored: %s", e)
	}
	got, _ := db.GetTask("task-a")
	if got.NextRun == nil || !got.NextRun.Equal(moved) {
		t.Fatalf("next_run = %v, want the job's %v", got.NextRun, moved)
	}
	if again, err := db.store().DueTasks(later); err != nil || len(again) != 0 {
		t.Fatalf("next poll claimed %d tasks, err %v; the override must stand", len(again), err)
	}
	if got, _ := db.GetTask("task-a"); got.NextRun == nil || !got.NextRun.Equal(moved) {
		t.Fatalf("next_run after the poll = %v, want %v", got.NextRun, moved)
	}

	if _, e := callToolOverSock(t, sock, "reschedule_task", map[string]any{
		"taskId": "task-b", "next_run": moved.Format(time.RFC3339),
	}); e == "" {
		t.Fatal("reschedule_task moved a sibling folder's task")
	}
	if _, e := callToolOverSock(t, sock, "reschedule_task", map[string]any{
		"taskId": "task-a", "next_run": "tomorrow",
	}); !strings.Contains(e, "RFC3339") {
		t.Fatalf("bad time: got %q, want an RFC3339 refusal", e)
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
	if err := addACLRow(db, core.ACLRow{
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
// keeps tools/list gating — a delegated folder sees the six tools; an ungranted
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
	if err := addACLRow(db, core.ACLRow{
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
	n := 0
	for _, r := range auditRowsFor(t, db, "scheduled_tasks:schedule") {
		if r.Outcome == "ok" && r.Folder == "hq" && r.Surface == "mcp" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("the audit trail holds %d scheduled_tasks:schedule rows, want 1", n)
	}
}

func TestScheduledTasksMCP_InternalFolderTarget(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	const owner = "world/a"
	const foreign = "world/b"
	for _, folder := range []string{owner, foreign} {
		if err := db.PutGroup(core.Group{Folder: folder}); err != nil {
			t.Fatal(err)
		}
	}
	grantMCPTools(t, db, owner, "schedule_task")
	sock := serveTasksMCP(t, db, owner, "folder:"+owner)
	schedule := func(target, prompt string) (map[string]any, string) {
		return callToolOverSock(t, sock, "schedule_task", map[string]any{
			"targetJid": target, "prompt": prompt, "cron": "0 2 * * *", "contextMode": "isolated",
		})
	}
	res, e := schedule(owner, "/compact-memories episodes day")
	if e != "" {
		t.Fatalf("registered owner folder without external routes: %s", e)
	}
	task, ok := db.GetTask(res["taskId"].(string))
	if !ok || task.Owner != owner || task.ChatJID != owner || task.ContextMode != "isolated" {
		t.Fatalf("internal task: %+v", task)
	}
	loop := NewLoop(db, nopRunner{}, LoopConfig{})
	loop.StopQueue()
	if route := loop.resolve(task.ChatJID, core.Message{ChatJID: task.ChatJID, Verb: "message"}); !route.ok || route.Folder != owner {
		t.Fatalf("internal reducer did not directly reach owner: %+v", route)
	}
	doSetRoutes(t, db, []core.Route{{Match: "", Target: owner}})
	for _, target := range []string{foreign, "world/missing"} {
		if _, e := schedule(target, "denied "+target); e == "" {
			t.Fatalf("target %s accepted through caller's catch-all route", target)
		}
	}
	if got := len(db.Tasks(owner, false)); got != 1 {
		t.Fatalf("denied targets wrote tasks: %d", got)
	}
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: owner}})
	const external = "slack:T012/channel/C1"
	res, e = schedule(external, "external reminder")
	if e != "" {
		t.Fatalf("external chat route rejected: %s", e)
	}
	task, ok = db.GetTask(res["taskId"].(string))
	if !ok || task.Owner != owner || task.ChatJID != external {
		t.Fatalf("external target changed: %+v", task)
	}
}

// seedTaskAt writes a task with an explicit status, cron and next_run.
func seedTaskAt(t *testing.T, db *DB, id, status, cron string, next *time.Time) {
	t.Helper()
	if err := store.NewOver(db.Obj()).CreateTask(core.Task{
		ID: id, Owner: "world/a", ChatJID: "slack:a", Prompt: "p", Cron: cron,
		NextRun: next, Status: status, Created: time.Now(),
	}); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

// resume_task drops a cron task's stale next_run (the scheduler fills the next
// cron instant) and keeps a one-shot's; reschedule_task revives a completed
// one-shot with the job's new time; a next_run written while the task is still
// firing survives timed's re-arm; schedule_task refuses an empty cron.
func TestScheduledTasksMCP_ResumeAndRescheduleStates(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "world"})
	_ = db.PutGroup(core.Group{Folder: "world/a"})
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "world/a"}})
	grantMCPTools(t, db, "world/a", taskToolNames...)
	sock := serveTasksMCP(t, db, "world/a", "folder:world/a")

	stale := time.Now().Add(-48 * time.Hour).UTC().Truncate(time.Second)
	seedTaskAt(t, db, "cr", core.TaskPaused, "0 9 * * *", &stale)
	seedTaskAt(t, db, "shot", core.TaskPaused, "", &stale)
	for _, id := range []string{"cr", "shot"} {
		if _, e := callToolOverSock(t, sock, "resume_task", map[string]any{"taskId": id}); e != "" {
			t.Fatalf("resume_task %s: %s", id, e)
		}
	}
	if got, _ := db.GetTask("cr"); got.Status != core.TaskActive || got.NextRun != nil {
		t.Errorf("resumed cron task: status %q next_run %v, want active with none", got.Status, got.NextRun)
	}
	if got, _ := db.GetTask("shot"); got.Status != core.TaskActive || got.NextRun == nil || !got.NextRun.Equal(stale) {
		t.Errorf("resumed one-shot: status %q next_run %v, want active keeping %v", got.Status, got.NextRun, stale)
	}

	later := time.Now().Add(96 * time.Hour).UTC().Truncate(time.Second)
	seedTaskAt(t, db, "done", core.TaskCompleted, "", nil)
	if _, e := callToolOverSock(t, sock, "reschedule_task", map[string]any{
		"taskId": "done", "next_run": later.Format(time.RFC3339),
	}); e != "" {
		t.Fatalf("reschedule_task done: %s", e)
	}
	if got, _ := db.GetTask("done"); got.Status != core.TaskActive || got.NextRun == nil || !got.NextRun.Equal(later) {
		t.Errorf("revived one-shot: status %q next_run %v, want active at %v", got.Status, got.NextRun, later)
	}

	seedTaskAt(t, db, "win", store.TaskStatusFiring, "0 9 * * *", &stale)
	if _, e := callToolOverSock(t, sock, "reschedule_task", map[string]any{
		"taskId": "win", "next_run": later.Format(time.RFC3339),
	}); e != "" {
		t.Fatalf("reschedule_task win: %s", e)
	}
	if err := db.RescheduleTask("win", ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.GetTask("win"); got.Status != core.TaskActive || got.NextRun == nil || !got.NextRun.Equal(later) {
		t.Errorf("job moved the run while firing: status %q next_run %v, want the job's %v",
			got.Status, got.NextRun, later)
	}

	if _, e := callToolOverSock(t, sock, "schedule_task", map[string]any{
		"targetJid": "slack:team/channel/c1", "prompt": "no schedule",
	}); !strings.Contains(e, "cron") {
		t.Fatalf("schedule_task without cron: got %q, want a cron refusal", e)
	}
}

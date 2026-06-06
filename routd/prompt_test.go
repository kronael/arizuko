package routd

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

// attachSiblings gives a test DB a writable in-memory stand-in for the sibling
// messages.db (scheduled_tasks + pane_sessions) that other split daemons own.
// Returns the raw handle so the test can seed rows the prompt path reads RO.
// session_log is NOT a sibling anymore: runed owns it and serves it over
// GET /v1/sessions/recent (see TestEmitSystemEvents_NewSession's resolver stub).
func attachSiblings(t *testing.T, d *DB) (msgs *sql.DB) {
	t.Helper()
	msgs, err := sql.Open("sqlite", "file:sib_"+randHex(8)+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := msgs.Exec(`
		CREATE TABLE scheduled_tasks (
		  id TEXT PRIMARY KEY, owner TEXT NOT NULL, chat_jid TEXT NOT NULL,
		  prompt TEXT NOT NULL, cron TEXT, next_run TEXT,
		  status TEXT NOT NULL DEFAULT 'active', created_at TEXT NOT NULL,
		  context_mode TEXT NOT NULL DEFAULT 'group');
		CREATE TABLE pane_sessions (
		  team_id TEXT NOT NULL, user_id TEXT NOT NULL, thread_ts TEXT NOT NULL,
		  channel_id TEXT NOT NULL, context_jid TEXT,
		  opened_at TEXT NOT NULL, last_status_at TEXT,
		  PRIMARY KEY (team_id, user_id, thread_ts));`); err != nil {
		t.Fatal(err)
	}
	d.msgs = msgs
	t.Cleanup(func() { msgs.Close() })
	return msgs
}

// TestBuildAgentPrompt_PaneHints mirrors gateway TestBuildAgentPrompt_PaneHints:
// a Slack-pane trigger gets <surface>slack-pane</surface> and, when the pane
// records a context channel, <pane-context jid="..."/>. Non-slack / no-pane
// triggers stay clean. The pane row lives in the sibling messages.db.
func TestBuildAgentPrompt_PaneHints(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	msgs := attachSiblings(t, db)
	_ = db.PutGroup(core.Group{Folder: "main"})
	loop := NewLoop(db, runnerFn(nil), LoopConfig{})
	loop.StopQueue()

	now := time.Now()
	trigger := []core.Message{{
		ID: "t1", ChatJID: "slack:T1/dm/D0XY", Sender: "u", Content: "hi",
		Timestamp: now, Verb: "message",
	}}

	// No pane row → no hints.
	if got := loop.buildAgentPrompt("main", "", trigger); strings.Contains(got, "slack-pane") {
		t.Errorf("non-pane jid emitted pane surface; prompt:\n%s", got)
	}

	// Pane row without context → surface only.
	if _, err := msgs.Exec(
		`INSERT INTO pane_sessions(team_id,user_id,thread_ts,channel_id,opened_at)
		 VALUES('T1','U99','1700.1','D0XY','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	got := loop.buildAgentPrompt("main", "", trigger)
	if !strings.Contains(got, "<surface>slack-pane</surface>") {
		t.Errorf("missing pane surface; prompt:\n%s", got)
	}
	if strings.Contains(got, "<pane-context") {
		t.Errorf("unexpected pane-context without context_jid; prompt:\n%s", got)
	}

	// Pane row with context → context tag too.
	if _, err := msgs.Exec(
		`UPDATE pane_sessions SET context_jid='slack:T1/channel/C42' WHERE channel_id='D0XY'`); err != nil {
		t.Fatal(err)
	}
	got = loop.buildAgentPrompt("main", "", trigger)
	if !strings.Contains(got, `<pane-context jid="slack:T1/channel/C42" />`) {
		t.Errorf("missing pane-context; prompt:\n%s", got)
	}

	// Non-slack jid → no hints even if pane exists.
	trigger[0].ChatJID = "telegram:99"
	if got := loop.buildAgentPrompt("main", "", trigger); strings.Contains(got, "slack-pane") {
		t.Errorf("non-slack jid leaked pane hint; prompt:\n%s", got)
	}
}

// TestEmitSystemEvents_NewDay: a chat whose cursor crossed midnight enqueues a
// new_day system message that the next prompt flush renders.
func TestEmitSystemEvents_NewDay(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	attachSiblings(t, db)
	_ = db.PutGroup(core.Group{Folder: "grp1"})
	loop := NewLoop(db, runnerFn(nil), LoopConfig{})
	loop.StopQueue()

	yesterday := time.Now().AddDate(0, 0, -1).UTC().Format(time.RFC3339Nano)
	_ = db.SetAgentCursor("jid1", yesterday)
	_ = db.PutSession("grp1", "", "sess-1") // a live session suppresses new_session

	loop.emitSystemEvents("grp1", "jid1")

	out := db.FlushSysMsgs("grp1")
	if !strings.Contains(out, "new_day") {
		t.Errorf("expected new_day event, got: %q", out)
	}
}

// stubSessions is a SessionResolver that returns canned records — the prompt
// path's federated session source. Records the folder/n it was asked for so a
// test can prove the new_session hint resolved through the resolver, not a
// cross-DB runed.db read (which no longer exists).
type stubSessions struct {
	rows     []core.SessionRecord
	gotFold  string
	gotN     int
	gotCalls int
}

func (s *stubSessions) RecentSessions(folder string, n int) []core.SessionRecord {
	s.gotFold, s.gotN, s.gotCalls = folder, n, s.gotCalls+1
	return s.rows
}

// TestEmitSystemEvents_NewSession: with no live session_id, emitSystemEvents
// enqueues new_session carrying the prior session's <previous_session> tail.
// The tail is FEDERATED from runed's GET /v1/sessions/recent (here a
// SessionResolver stub bound via the Server) — NOT a cross-DB runed.db read.
func TestEmitSystemEvents_NewSession(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	attachSiblings(t, db)
	_ = db.PutGroup(core.Group{Folder: "grp2"})
	loop := NewLoop(db, runnerFn(nil), LoopConfig{})
	loop.StopQueue()

	ended := time.Date(2026, 1, 1, 10, 5, 0, 0, time.UTC)
	sess := &stubSessions{rows: []core.SessionRecord{{
		ID: 1, Folder: "grp2", SessionID: "abc123def456",
		StartedAt: time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC),
		EndedAt:   &ended, Result: "ok", MsgCount: 7,
	}}}
	srv := NewServer(db, loop, nil, nil, 0, "")
	srv.SetSessionResolver(sess)
	loop.BindServer(srv)

	_ = db.SetAgentCursor("jid2", time.Now().UTC().Format(time.RFC3339Nano))

	loop.emitSystemEvents("grp2", "jid2")

	if sess.gotCalls != 1 || sess.gotFold != "grp2" || sess.gotN != 1 {
		t.Fatalf("expected one resolver call for (grp2,1), got calls=%d folder=%q n=%d",
			sess.gotCalls, sess.gotFold, sess.gotN)
	}
	out := db.FlushSysMsgs("grp2")
	if !strings.Contains(out, "new_session") {
		t.Fatalf("expected new_session event, got: %q", out)
	}
	if !strings.Contains(out, "previous_session") {
		t.Errorf("expected previous_session tail, got: %q", out)
	}
	if !strings.Contains(out, `msgs="7"`) || !strings.Contains(out, `result="ok"`) ||
		!strings.Contains(out, `"abc123de"`) {
		t.Errorf("previous_session fields wrong, got: %q", out)
	}
}

func TestPreviousSessionXML_Empty(t *testing.T) {
	if got := previousSessionXML(nil); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

// TestSiblingTasks_RootVsChild: a root group sees every task; a child sees
// only its own (port of store.ListTasks owner-filter semantics). routd OWNS
// scheduled_tasks (migration 0009), so the rows seed into routd's OWN db.
func TestSiblingTasks_RootVsChild(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, r := range [][2]string{{"t-main", "main"}, {"t-sub", "main/sub"}} {
		if _, err := db.SQL().Exec(
			`INSERT INTO scheduled_tasks(id,owner,chat_jid,prompt,status,created_at,context_mode)
			 VALUES(?,?,'jid','do it','active','2026-01-01T00:00:00Z','group')`, r[0], r[1]); err != nil {
			t.Fatal(err)
		}
	}
	if got := db.SiblingTasks("main", true); len(got) != 2 {
		t.Errorf("root: want 2 tasks, got %d", len(got))
	}
	got := db.SiblingTasks("main/sub", false)
	if len(got) != 1 || got[0].ID != "t-sub" {
		t.Errorf("child: want [t-sub], got %+v", got)
	}
}

// TestRunTurn_WritesSpawnSnapshots drives a turn with a real ipc dir and
// asserts the per-spawn snapshot files land in the folder's ipc dir
// (available_groups.json always; current_tasks.json carrying the task). routd
// OWNS scheduled_tasks (migration 0009), so the task seeds into routd's OWN db.
// G5: gated writes these right before spawn; routd's twin is runTurn.
func TestRunTurn_WritesSpawnSnapshots(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_ = db.PutGroup(core.Group{Folder: "demo"})
	if _, err := db.SQL().Exec(
		`INSERT INTO scheduled_tasks(id,owner,chat_jid,prompt,status,created_at,context_mode)
		 VALUES('t1','demo','jid','do it','active','2026-01-01T00:00:00Z','group')`); err != nil {
		t.Fatal(err)
	}

	ipcDir := t.TempDir()
	runner := runnerFn(func(_ context.Context, _ runedv1.RunRequest) (runedv1.RunOutcome, error) {
		return runedv1.RunOutcome{Outcome: runedv1.OutcomeOK, SessionID: "sess"}, nil
	})
	loop := NewLoop(db, runner, LoopConfig{IpcDir: ipcDir})
	loop.StopQueue()
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "demo"}})
	_ = db.PutMessage(core.Message{ID: "m1", ChatJID: "slack:T/C/U", Sender: "u1",
		Content: "hi", Timestamp: time.Now().UTC(), Verb: "message"})

	if _, err := loop.processGroupMessages("slack:T/C/U"); err != nil {
		t.Fatalf("process: %v", err)
	}

	groupsSnap := filepath.Join(ipcDir, "demo", "available_groups.json")
	tasksSnap := filepath.Join(ipcDir, "demo", "current_tasks.json")
	g, err := os.ReadFile(groupsSnap)
	if err != nil {
		t.Fatalf("groups snapshot not written: %v", err)
	}
	if !strings.Contains(string(g), `"demo"`) {
		t.Errorf("groups snapshot missing demo group: %s", g)
	}
	ts, err := os.ReadFile(tasksSnap)
	if err != nil {
		t.Fatalf("tasks snapshot not written: %v", err)
	}
	if !strings.Contains(string(ts), `"t1"`) {
		t.Errorf("tasks snapshot missing seeded task: %s", ts)
	}
}

// TestTasksReadOwnDBNotSibling proves routd reads tasks from its OWN routd.db,
// not the sibling messages.db: a task seeded ONLY in the sibling has NO effect
// (the sibling-read for tasks is gone), while the same task seeded in routd.db
// DOES surface. Mirrors TestACLReadsOwnDBNotSibling.
func TestTasksReadOwnDBNotSibling(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	// Attach a fresh migrated messages.db as the sibling and seed a task ONLY
	// there. If routd still sibling-read tasks, this would surface; it must NOT.
	sib := attachMsgsSibling(t, db)
	if err := sib.CreateTask(core.Task{
		ID: "sib-task", Owner: "main", ChatJID: "jid", Prompt: "from sibling",
		Status: core.TaskActive, Created: time.Now(),
	}); err != nil {
		t.Fatalf("seed sibling task: %v", err)
	}
	if got := db.SiblingTasks("main", true); len(got) != 0 {
		t.Errorf("sibling-only task must NOT surface (reads routd.db), got %+v", got)
	}

	// The same task in routd's OWN db DOES surface.
	seedTask(t, db, "own-task", "main", "jid", "from routd")
	got := db.SiblingTasks("main", true)
	if len(got) != 1 || got[0].ID != "own-task" {
		t.Errorf("routd.db task should surface, got %+v", got)
	}
}

// TestSiblings_NilHandles: with no sibling messages.db attached (OpenMem leaves
// the handle nil) every accessor returns the empty result, never panics.
func TestSiblings_NilHandles(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if got := db.SiblingTasks("main", true); got != nil {
		t.Errorf("nil msgs handle: want nil tasks, got %+v", got)
	}
	if _, ok := db.SiblingPaneContextJID("D0XY"); ok {
		t.Error("nil msgs handle: want no pane")
	}
}

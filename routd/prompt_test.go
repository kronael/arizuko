package routd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

// TestBuildAgentPrompt_PaneHints mirrors gateway TestBuildAgentPrompt_PaneHints:
// a Slack-pane trigger gets <surface>slack-pane</surface> and, when the pane
// records a context channel, <pane-context jid="..."/>. Non-slack / no-pane
// triggers stay clean. The pane row lives in routd's OWN routd.db — routd OWNS
// pane_sessions (migration 0010); it opens NO sibling messages.db.
func TestBuildAgentPrompt_PaneHints(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
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

	// Pane row without context → surface only. Seed into routd's OWN routd.db.
	if _, err := db.SQL().Exec(
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

	// Pane row with context (set via the POST /v1/pane backing) → context tag too.
	if err := db.SetPaneContext("D0XY", "slack:T1/channel/C42"); err != nil {
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

// TestTasksReadOwnDB proves routd reads tasks from its OWN routd.db: an empty
// db yields no tasks; a task seeded in routd.db DOES surface. routd opens NO
// sibling messages.db. Mirrors TestACLReadsOwnDB.
func TestTasksReadOwnDB(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	// Empty routd.db → no tasks (no cross-DB read to leak a sibling task).
	if got := db.SiblingTasks("main", true); len(got) != 0 {
		t.Errorf("empty routd.db must surface no tasks, got %+v", got)
	}

	// A task in routd's OWN db DOES surface.
	seedTask(t, db, "own-task", "main", "jid", "from routd")
	got := db.SiblingTasks("main", true)
	if len(got) != 1 || got[0].ID != "own-task" {
		t.Errorf("routd.db task should surface, got %+v", got)
	}
}

// TestSiblings_EmptyOwnDB: an empty routd.db (OpenMem, no rows) returns the
// empty result for every federation accessor, never panics. routd opens NO
// sibling DB; the tables are its own and just empty.
func TestSiblings_EmptyOwnDB(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if got := db.SiblingTasks("main", true); got != nil {
		t.Errorf("empty routd.db: want nil tasks, got %+v", got)
	}
	if _, ok := db.SiblingPaneContextJID("D0XY"); ok {
		t.Error("empty routd.db: want no pane")
	}
}

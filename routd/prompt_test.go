package routd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/store"
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
	if got, _ := loop.buildAgentPrompt("main", "", trigger); strings.Contains(got, "slack-pane") {
		t.Errorf("non-pane jid emitted pane surface; prompt:\n%s", got)
	}

	// A pane without a context → surface only. It is opened through the writer:
	// pane_sessions is an objstore entity in routd's OWN routd.db.
	if err := db.UpsertPane("T1", "U99", "1700.1", "D0XY"); err != nil {
		t.Fatal(err)
	}
	got, _ := loop.buildAgentPrompt("main", "", trigger)
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
	got, _ = loop.buildAgentPrompt("main", "", trigger)
	if !strings.Contains(got, `<pane-context jid="slack:T1/channel/C42" />`) {
		t.Errorf("missing pane-context; prompt:\n%s", got)
	}

	// Non-slack jid → no hints even if pane exists.
	trigger[0].ChatJID = "telegram:99"
	if got, _ := loop.buildAgentPrompt("main", "", trigger); strings.Contains(got, "slack-pane") {
		t.Errorf("non-slack jid leaked pane hint; prompt:\n%s", got)
	}
}

// TestBuildAgentPrompt_LinkContext drives the full carry (spec 5/W § link
// context): a POST /v1/messages whose wire body carries link_context (webd's
// ingest snapshot of the route token's context) persists it, and the built
// prompt renders one <link-context> tag. A message without it → no tag.
// Newest trigger wins when a batch mixes tokens.
func TestBuildAgentPrompt_LinkContext(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_ = db.PutGroup(core.Group{Folder: "main"})
	loop := NewLoop(db, runnerFn(nil), LoopConfig{})
	loop.StopQueue()
	srv := NewServer(db, loop, nil, nil, 0, "")
	h := srv.Handler()

	// Wire ingest, exactly what webd sends for a context-bearing token.
	rec := doJSON(t, h, "POST", "/v1/messages", "", apiv1.Message{
		ID: "lc1", ChatJID: "web:main", Sender: "anon:1", Content: "hello",
		Topic: "tt", LinkContext: "bug reports; triage, don't chat",
	})
	if rec.Code != 200 {
		t.Fatalf("ingest = %d body=%s", rec.Code, rec.Body.String())
	}
	trigger, err := db.MessagesSince("web:main", "")
	if err != nil || len(trigger) != 1 {
		t.Fatalf("trigger = %d err=%v", len(trigger), err)
	}
	if trigger[0].LinkContext != "bug reports; triage, don't chat" {
		t.Fatalf("persisted link_context = %q", trigger[0].LinkContext)
	}
	got, _ := loop.buildAgentPrompt("main", "tt", trigger)
	want := "<link-context>\nbug reports; triage, don't chat\n</link-context>\n"
	if !strings.Contains(got, want) {
		t.Errorf("missing link-context tag; prompt:\n%s", got)
	}

	// No link_context → no tag.
	plain := []core.Message{{
		ID: "lc2", ChatJID: "web:main", Sender: "anon:2", Content: "hi",
		Timestamp: time.Now(), Verb: "message",
	}}
	if got, _ := loop.buildAgentPrompt("main", "tt", plain); strings.Contains(got, "<link-context>") {
		t.Errorf("tokenless trigger emitted link-context; prompt:\n%s", got)
	}

	// Mixed batch: the newest message's context wins.
	mixed := append(trigger, core.Message{
		ID: "lc3", ChatJID: "web:main", Sender: "anon:3", Content: "again",
		Timestamp: time.Now(), Verb: "message", LinkContext: "stripe events; summarize daily",
	})
	got, _ = loop.buildAgentPrompt("main", "tt", mixed)
	if !strings.Contains(got, "<link-context>\nstripe events; summarize daily\n</link-context>\n") {
		t.Errorf("newest context should win; prompt:\n%s", got)
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

	// The cursor is a whole message key now, and the day is read off its lead.
	yesterday := store.MsgStamp(core.TS(time.Now().AddDate(0, 0, -1)), "m0")
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
// The tail is FEDERATED from runed's GET /v1/spawn_sessions/recent (here a
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
		ID: "log-1", Folder: "grp2", SessionID: "abc123def456",
		StartedAt: time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC),
		EndedAt:   &ended, Result: "ok", MsgCount: 7,
	}}}
	srv := NewServer(db, loop, nil, nil, 0, "")
	srv.SetSessionResolver(sess)
	loop.BindServer(srv)

	_ = db.SetAgentCursor("jid2", core.NowTS())

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
// scheduled_tasks, so the rows seed into routd's OWN db.
func TestSiblingTasks_RootVsChild(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, r := range [][2]string{{"t-main", "main"}, {"t-sub", "main/sub"}} {
		seedTask(t, db, r[0], r[1], "jid", "do it")
	}
	if got := db.Tasks("main", true); len(got) != 2 {
		t.Errorf("root: want 2 tasks, got %d", len(got))
	}
	got := db.Tasks("main/sub", false)
	if len(got) != 1 || got[0].ID != "t-sub" {
		t.Errorf("child: want [t-sub], got %+v", got)
	}
}

// TestRunTurn_WritesSpawnSnapshots drives a turn with a real ipc dir and
// asserts the per-spawn snapshot files land in the folder's ipc dir
// (available_groups.json always; current_tasks.json carrying the task). routd
// OWNS scheduled_tasks, so the task seeds into routd's OWN db.
// G5: gated writes these right before spawn; routd's twin is runTurn.
func TestRunTurn_WritesSpawnSnapshots(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_ = db.PutGroup(core.Group{Folder: "demo"})
	seedTask(t, db, "t1", "demo", "jid", "do it")

	ipcDir := t.TempDir()
	runner := runnerFn(func(_ context.Context, _ runedv1.RunRequest) (runedv1.RunOutcome, error) {
		return runedv1.RunOutcome{Outcome: runedv1.OutcomeOK}, nil
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
	// A NON-elevated turn (a tenant, not root) gets its snapshots written but NO
	// groups list — the all-groups view is a root/elevation privilege now.
	if strings.Contains(string(g), `"demo"`) {
		t.Errorf("non-elevated groups snapshot must not list groups: %s", g)
	}
	ts, err := os.ReadFile(tasksSnap)
	if err != nil {
		t.Fatalf("tasks snapshot not written: %v", err)
	}
	// The folder's OWN tasks still surface (owner filter, not the all view).
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
	if got := db.Tasks("main", true); len(got) != 0 {
		t.Errorf("empty routd.db must surface no tasks, got %+v", got)
	}

	// A task in routd's OWN db DOES surface.
	seedTask(t, db, "own-task", "main", "jid", "from routd")
	got := db.Tasks("main", true)
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
	if got := db.Tasks("main", true); got != nil {
		t.Errorf("empty routd.db: want nil tasks, got %+v", got)
	}
	if _, ok := db.PaneContextJID("D0XY"); ok {
		t.Error("empty routd.db: want no pane")
	}
}

// BUGS L58: buildAgentPrompt used to advance the observed cursor itself, at
// render time — before the container spawns. Every failure after that point
// re-feeds the batch (transport failure, busy backoff, runed at capacity), and
// the re-fed turn rebuilt its prompt against a watermark the first attempt had
// already consumed. The ambient window was read once and lost.
//
// The render must therefore be pure with respect to the cursor: it REPORTS the
// watermark and writes nothing. Rendering twice must show the same context both
// times, which is exactly what a re-fed turn does.
func TestBuildAgentPrompt_DoesNotConsumeTheObservedWindow(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_ = db.PutGroup(core.Group{Folder: "main"})
	loop := NewLoop(db, runnerFn(nil), LoopConfig{
		ObserveWindowMessages: 10, ObserveWindowChars: 4000,
	})
	loop.StopQueue()

	now := time.Now()
	ambient := core.Message{ID: "obs1", ChatJID: "tg:9", Sender: "someone",
		Content: "ambient chatter", Timestamp: now, RoutedTo: "main", Verb: "message"}
	if err := db.PutMessage(ambient); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkMessagesObserved("main", []core.Message{ambient}); err != nil {
		t.Fatal(err)
	}

	trigger := []core.Message{{
		ID: "t1", ChatJID: "tg:1", Sender: "u", Content: "hi",
		Timestamp: now.Add(time.Second), Verb: "message",
	}}

	first, mark := loop.buildAgentPrompt("main", "", trigger)
	if !strings.Contains(first, "ambient chatter") {
		t.Fatalf("observed message absent from the first render:\n%s", first)
	}
	if mark == "" {
		t.Fatal("render reported no watermark despite showing an observed message")
	}

	// The re-fed turn. Nothing was committed, so it must see the same window.
	second, mark2 := loop.buildAgentPrompt("main", "", trigger)
	if !strings.Contains(second, "ambient chatter") {
		t.Errorf("re-fed turn lost the observed window — the render consumed it:\n%s", second)
	}
	if mark2 != mark {
		t.Errorf("watermark moved between renders: %q then %q", mark, mark2)
	}

	// Only an explicit advance consumes it — the caller's job, on the same
	// success path that advances the message cursor.
	if err := db.AdvanceObservedCursor("main", "", mark); err != nil {
		t.Fatal(err)
	}
	third, mark3 := loop.buildAgentPrompt("main", "", trigger)
	if strings.Contains(third, "ambient chatter") {
		t.Errorf("observed message re-shown after an explicit advance:\n%s", third)
	}
	if mark3 != "" {
		t.Errorf("empty window still reported a watermark: %q", mark3)
	}
}

// BUGS L54 was closed on the SESSION path — an isolated cron gets its own
// container and never resumes or persists the chat's session. Its rows still
// land routed_to the folder with is_observed=1, so the ambient window served
// every cron prompt back to every topic in that folder on its next turn. On
// sloth's #trading the observed block was dominated by 8-a-day "silent cron"
// prompts, which is the same hole through a second door.
func TestObservedSinceExcludesIsolatedCronRuns(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	now := time.Now().UTC()
	put := func(i int, id, sender, content string) {
		m := core.Message{ID: id, ChatJID: "discord:g/c", Sender: sender, Content: content,
			Timestamp: now.Add(time.Duration(i) * time.Second), RoutedTo: "trading"}
		if err := db.PutMessage(m); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
		if err := db.MarkMessagesObserved("trading", []core.Message{m}); err != nil {
			t.Fatalf("observe %s: %v", id, err)
		}
	}
	put(0, "human-1", "alice", "what is the btc level")
	put(1, "cron-1", core.IsolatedSenderPrefix+"task-9", "Silent cron: run the monitor, stay silent")

	// Prove the cron row qualifies on EVERY other predicate, so this test fails
	// if the sender exclusion is removed rather than passing vacuously: both
	// rows are in the folder's feed, and the cron one carries a body and is not
	// the agent's own output.
	feed, err := db.Obj().Query(t.Context(), objstore.Query{
		Entity: store.MessageFolders, Partition: "trading",
	})
	if err != nil {
		t.Fatalf("precondition query: %v", err)
	}
	if inFeed := len(feed); inFeed != 2 {
		t.Fatalf("the folder feed holds %d rows; the fixture does not reach the ambient "+
			"read at all and the test would pass for the wrong reason", inFeed)
	}
	cron := msgByID(t, db, "discord:g/c", "cron-1")
	if cron.BotMsg || cron.Content == "" || cron.RoutedTo != "trading" {
		t.Fatalf("the cron row fails another predicate already: %+v", cron)
	}

	got := db.ObservedSince("trading", "", 50, 50000)

	var ids []string
	for _, m := range got {
		ids = append(ids, m.ID)
	}
	if len(ids) != 1 || ids[0] != "human-1" {
		t.Fatalf("ambient window = %v, want only [human-1]: an isolated run's prompt must never become another topic's context", ids)
	}
}

// observe_group: the observer sees a watched source's PRIMARY deliveries as
// ambient context even though those rows are not observed — the two halves of
// the join ask for opposite values of the same flag, and routd is what resolves
// the watched set before the read.
func TestObservedSince_WatchedSources(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, f := range []string{"root", "child"} {
		if err := db.PutGroup(core.Group{Folder: f, AddedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	// A primary delivery to child: routed there, never marked observed.
	if err := db.PutMessage(core.Message{
		ID: "c-1", ChatJID: "telegram:1", Sender: "u", Name: "u",
		Content: "child-msg", Timestamp: time.Now().Add(-time.Minute),
		Verb: "message", Source: "telegram", RoutedTo: "child",
	}); err != nil {
		t.Fatal(err)
	}

	// Without watching: root sees nothing.
	if got := db.ObservedSince("root", "", 10, 4000); len(got) != 0 {
		t.Fatalf("no watcher: got %d msgs, want 0", len(got))
	}

	if err := db.AddGroupWatcher("root", "child"); err != nil {
		t.Fatal(err)
	}
	got := db.ObservedSince("root", "", 10, 4000)
	if len(got) != 1 || got[0].ID != "c-1" {
		t.Fatalf("after watching: got %+v, want [c-1]", got)
	}

	if err := db.RemoveGroupWatcher("root", "child"); err != nil {
		t.Fatal(err)
	}
	if got := db.ObservedSince("root", "", 10, 4000); len(got) != 0 {
		t.Fatalf("after unwatch: got %d msgs, want 0", len(got))
	}
}

// Regression: with a watched source AND a non-empty cursor, the two halves used
// to bind their arguments in the wrong order and the watched-source rows
// vanished. A cursor below the message's instant must still surface it.
func TestObservedSince_WatchedSourcesWithCursor(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, f := range []string{"root", "child"} {
		if err := db.PutGroup(core.Group{Folder: f, AddedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	if err := db.PutMessage(core.Message{
		ID: "c-1", ChatJID: "telegram:1", Sender: "u", Name: "u",
		Content: "child-msg", Timestamp: now,
		Verb: "message", Source: "telegram", RoutedTo: "child",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.AddGroupWatcher("root", "child"); err != nil {
		t.Fatal(err)
	}

	got := db.ObservedSince("root", core.TS(now.Add(-time.Minute)), 10, 4000)
	if len(got) != 1 || got[0].ID != "c-1" {
		t.Fatalf("watched + cursor: got %+v, want [c-1]", got)
	}
}

// BUGS L521: on krons a turn's own message also stood in its observed window,
// so the prompt carried it as <message> and again as <observed>.
func TestBuildAgentPrompt_TriggerNotRepeatedAsObserved(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_ = db.PutGroup(core.Group{Folder: "main"})
	loop := NewLoop(db, runnerFn(nil), LoopConfig{
		ObserveWindowMessages: 10, ObserveWindowChars: 4000,
	})
	loop.StopQueue()

	msg := core.Message{ID: "m239", ChatJID: "tg:1", Sender: "u",
		Content: "the zebrak listing", Timestamp: time.Now(), RoutedTo: "main", Verb: "message"}
	if err := db.PutMessage(msg); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkMessagesObserved("main", []core.Message{msg}); err != nil {
		t.Fatal(err)
	}

	got, mark := loop.buildAgentPrompt("main", "", []core.Message{msg})
	if n := strings.Count(got, "the zebrak listing"); n != 1 {
		t.Errorf("trigger text appears %d times, want 1:\n%s", n, got)
	}
	if strings.Contains(got, "<observed") {
		t.Errorf("trigger repeated as observed:\n%s", got)
	}
	if mark == "" {
		t.Error("watermark lost: the filtered row must still advance the observed cursor")
	}
}

// BUGS L527: on krons a topic-232 turn answered message 238, then the topic-""
// turn built after it saw 238 as an unseen observed row and answered it again,
// because the observed window dropped the folder's own replies. The reply a
// sibling topic sent must reach the next turn as an observed bot row, rendered
// bot="true" so the agent reads it as its own answer, under the rule that says
// observed rows are context.
func TestBuildAgentPrompt_SiblingTopicReplyIsObservedBotRow(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_ = db.PutGroup(core.Group{Folder: "nemo"})
	loop := NewLoop(db, runnerFn(nil), LoopConfig{
		ObserveWindowMessages: 10, ObserveWindowChars: 4000,
	})
	loop.StopQueue()

	now := time.Now().UTC()
	ask := core.Message{ID: "238", ChatJID: "tg:1", Sender: "kron", Content: "jak je to daleko",
		Timestamp: now, RoutedTo: "nemo", Topic: "232", Verb: "message"}
	reply := core.Message{ID: "out-238", ChatJID: "tg:1", Sender: "nemo",
		Content: "the answer: 40 km to go", Timestamp: now.Add(time.Minute),
		RoutedTo: "nemo", Topic: "232", BotMsg: true, FromMe: true}
	for _, m := range []core.Message{ask, reply} {
		if err := db.PutMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.MarkMessagesObserved("nemo", []core.Message{ask}); err != nil {
		t.Fatal(err)
	}

	trigger := []core.Message{{ID: "239", ChatJID: "tg:1", Sender: "kron",
		Content: "and the weather?", Timestamp: now.Add(2 * time.Minute), Verb: "message"}}
	got, mark := loop.buildAgentPrompt("nemo", "", trigger)

	if !strings.Contains(got, `<observed id="out-238" sender="nemo"`) ||
		!strings.Contains(got, `bot="true">the answer: 40 km to go</observed>`) {
		t.Errorf("the sibling topic's reply is not an observed bot row:\n%s", got)
	}
	if mark != core.TS(reply.Timestamp) {
		t.Errorf("watermark = %q, want the reply's instant %q", mark, core.TS(reply.Timestamp))
	}
}

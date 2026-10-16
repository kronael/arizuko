package routd

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
)

// newForkLoop builds a Loop on an in-memory DB with a temp groups dir so
// l.folders.GroupPath resolves under it (mirrors gateway_test's testGateway
// for the fork/session-copy path).
func newForkLoop(t *testing.T) (*DB, *Loop) {
	t.Helper()
	db, err := OpenMem()
	if err != nil {
		t.Fatalf("open mem db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	loop := NewLoop(db, nopRunner{}, LoopConfig{GroupsDir: t.TempDir()})
	loop.StopQueue()
	return db, loop
}

// seedParentSession writes a parent topic's session row + its jsonl under the
// group's container-mounted projects dir, returning the projects dir.
func seedParentSession(t *testing.T, l *Loop, db *DB, folder, parentTopic, uuid string, body []byte) string {
	t.Helper()
	if err := db.PutSession(folder, parentTopic, uuid); err != nil {
		t.Fatalf("put parent session: %v", err)
	}
	groupDir, err := l.folders.GroupPath(folder)
	if err != nil {
		t.Fatalf("group path: %v", err)
	}
	projDir := filepath.Join(groupDir, ".claude", "projects", "-home-node")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projDir, uuid+".jsonl"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	return projDir
}

// Spec 5/F-topic-lineage (G7): fork_topic copies the parent's Claude Code session jsonl so
// the child resumes with full history. routd's ForkTopic MCP fn wraps
// db.ForkTopic with the cp step (mirrors gateway.forkTopic).
func TestForkTopic_CopiesParentSession(t *testing.T) {
	db, loop := newForkLoop(t)
	folder := "main"

	parentUUID := "parent-uuid"
	parentBody := []byte(`{"type":"summary"}` + "\n")
	projDir := seedParentSession(t, loop, db, folder, "", parentUUID, parentBody)

	// Drive the same logic the MCP ForkTopic fn does: fork the row with a
	// fresh child uuid, then copy the parent session under it.
	childUUID := "child-uuid"
	if err := db.ForkTopic(folder, "", "#deploy", childUUID, false); err != nil {
		t.Fatalf("ForkTopic: %v", err)
	}
	loop.copyParentSession(folder, "", childUUID)

	got, err := os.ReadFile(filepath.Join(projDir, childUUID+".jsonl"))
	if err != nil {
		t.Fatalf("child file not created: %v", err)
	}
	if string(got) != string(parentBody) {
		t.Errorf("child file content drift\nwant: %q\n got: %q", parentBody, got)
	}
}

// Default-fork-from-main path: the first turn on a previously-unseen topic
// creates the lineage row AND copies the parent topic's session, so the child
// resumes from the parent's tail instead of cold (mirrors
// gateway.ensureTopicWithFork). A second call is a no-op.
func TestEnsureTopicWithFork_CopiesParentSession(t *testing.T) {
	db, loop := newForkLoop(t)
	folder := "main"

	parentUUID := "main-uuid"
	projDir := seedParentSession(t, loop, db, folder, "", parentUUID, []byte("parent\n"))

	loop.ensureTopicWithFork(folder, "#deploy", "")

	childUUID := db.SessionID(folder, "#deploy")
	if childUUID == "" {
		t.Fatal("child session row missing")
	}
	got, err := os.ReadFile(filepath.Join(projDir, childUUID+".jsonl"))
	if err != nil {
		t.Fatalf("child file not copied: %v", err)
	}
	if string(got) != "parent\n" {
		t.Errorf("child content drift\nwant: %q\n got: %q", "parent\n", got)
	}

	// Second call must be a no-op: same child uuid, no re-fork (lineage row
	// already exists so EnsureTopicLineage returns inserted=false).
	loop.ensureTopicWithFork(folder, "#deploy", "")
	if got2 := db.SessionID(folder, "#deploy"); got2 != childUUID {
		t.Errorf("second call clobbered session id: %q -> %q", childUUID, got2)
	}
}

// First turn on a fresh folder whose parent (main) has no session yet: the
// lineage row is still created with a fresh uuid, but no jsonl is copied
// (child starts cold without erroring). Guards the brand-new-folder case.
func TestEnsureTopicWithFork_NoParentSessionIsNoOp(t *testing.T) {
	db, loop := newForkLoop(t)
	folder := "main"

	loop.ensureTopicWithFork(folder, "#deploy", "")

	childUUID := db.SessionID(folder, "#deploy")
	if childUUID == "" {
		t.Fatal("child lineage row should still be created")
	}
	groupDir, _ := loop.folders.GroupPath(folder)
	dst := filepath.Join(groupDir, ".claude", "projects", "-home-node", childUUID+".jsonl")
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Errorf("child jsonl created with no parent session: %v", err)
	}
}

// A fork stamps observed_cursor=now so the child reads only post-fork context —
// which is right when it inherits the parent transcript and wrong when it does
// not. With no parent session there is nothing to inherit, so the child must
// keep its ambient window; otherwise its first turn carries no context at all,
// which is what users saw as "it starts over without context" (BUGS L54).
func TestEnsureTopicWithFork_NoParentSessionKeepsObservedWindow(t *testing.T) {
	db, loop := newForkLoop(t)
	folder := "main"

	loop.ensureTopicWithFork(folder, "#deploy", "")

	line, ok := db.TopicLineage(folder, "#deploy")
	if !ok {
		t.Fatal("child lineage row missing")
	}
	if line.ObservedCursor != "" {
		t.Errorf("observed cursor = %q; the child inherited no transcript, so it must still see ambient context",
			line.ObservedCursor)
	}
}

// The mirror case: the child DID inherit the parent transcript, so its cursor
// survives the fork. The value it keeps is the PARENT's cursor, never `now` —
// stamping now discarded the channel's ambient backlog at the moment a thread
// was created, so a new thread opened blind to everything said before it
// existed (BUGS L87, fixed 64aae191). This test asserted the pre-L87 rule and
// failed from that commit until 2026-08-24 (BUGS L90).
func TestEnsureTopicWithFork_CopiedSessionKeepsParentCursor(t *testing.T) {
	db, loop := newForkLoop(t)
	folder := "main"

	seedParentSession(t, loop, db, folder, "", "main-uuid", []byte("parent\n"))
	parentCursor := core.TS(time.Date(2026, 8, 24, 6, 0, 0, 0, time.UTC))
	if err := db.AdvanceObservedCursor(folder, "", parentCursor); err != nil {
		t.Fatalf("seed parent cursor: %v", err)
	}

	loop.ensureTopicWithFork(folder, "#deploy", "")

	line, ok := db.TopicLineage(folder, "#deploy")
	if !ok {
		t.Fatal("child lineage row missing")
	}
	if line.ObservedCursor != parentCursor {
		t.Errorf("observed cursor = %q, want the parent's %q — a fork inherits the parent's ambient window, and a transcript it copied must not clear it",
			line.ObservedCursor, parentCursor)
	}
}

// A parent with no cursor of its own hands down no cursor. The child still
// inherited the transcript, so nothing may CLEAR anything here — the empty
// value is the parent's, and the cold-root path reads the whole trailing
// window (capped by observe_window_*), which is what a fresh thread wants.
func TestEnsureTopicWithFork_CursorlessParentHandsDownNothing(t *testing.T) {
	db, loop := newForkLoop(t)
	folder := "main"

	seedParentSession(t, loop, db, folder, "", "main-uuid", []byte("parent\n"))
	loop.ensureTopicWithFork(folder, "#deploy", "")

	line, ok := db.TopicLineage(folder, "#deploy")
	if !ok {
		t.Fatal("child lineage row missing")
	}
	if line.ObservedCursor != "" {
		t.Errorf("observed cursor = %q, want empty — the parent had none to hand down", line.ObservedCursor)
	}
}

// The explicit fork_topic MCP tool forks through the same rule as an implicit
// thread fork: both call inheritParentSession, so neither can drift into
// stamping a cursor over a child that inherited nothing.
func TestInheritParentSession_ExplicitForkWithNoParentKeepsObservedWindow(t *testing.T) {
	db, loop := newForkLoop(t)
	folder := "main"

	childUUID := "child-uuid"
	if err := db.ForkTopic(folder, "", "#deploy", childUUID, false); err != nil {
		t.Fatalf("ForkTopic: %v", err)
	}
	loop.inheritParentSession(folder, "", "#deploy", childUUID)

	line, ok := db.TopicLineage(folder, "#deploy")
	if !ok {
		t.Fatal("child lineage row missing")
	}
	if line.ObservedCursor != "" {
		t.Errorf("observed cursor = %q; the child inherited no transcript, so it must still see ambient context",
			line.ObservedCursor)
	}
}

// A first turn in a nested topic (`a/b/c`) with no reply to inherit from forks
// from the nearest session above it on its path, through the real dispatch
// path. An inline GitHub review thread (`pr/5/review/77`) has no reply_to on
// its root comment; it opened blind to the PR it is on.
func TestRunTurn_PathTopicForksFromNearestSessionAbove(t *testing.T) {
	const (
		jid         = "github:o/r/pr/5"
		reviewTopic = "pr/5/review/77"
	)
	cases := []struct {
		name    string
		replyTo string
		seedMsg bool
	}{
		{name: "no reply"},
		{name: "reply to the thread's own ambient root", replyTo: "root77", seedMsg: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, loop := newForkLoop(t)
			folder := "demo"
			if err := db.PutGroup(core.Group{Folder: folder}); err != nil {
				t.Fatal(err)
			}
			doSetRoutes(t, db, []core.Route{{Match: "platform=github", Target: folder}})
			projDir := seedParentSession(t, loop, db, folder, "pr/5", "pr-uuid", []byte("pr history\n"))
			if tc.seedMsg {
				if err := db.PutMessage(core.Message{ID: tc.replyTo, ChatJID: jid, Sender: "u1",
					Content: "root", Topic: reviewTopic, Timestamp: time.Now().UTC().Add(-time.Minute),
					Verb: "message"}); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.PutMessage(core.Message{ID: "m1", ChatJID: jid, Sender: "u1",
				Content: "on a.go:\nwhy?", Topic: reviewTopic, ReplyToID: tc.replyTo,
				Timestamp: time.Now().UTC(), Verb: "message"}); err != nil {
				t.Fatal(err)
			}

			if _, err := loop.processGroupMessages(jid); err != nil {
				t.Fatalf("process: %v", err)
			}

			childUUID := db.SessionID(folder, reviewTopic)
			if childUUID == "" {
				t.Fatal("review topic has no session row")
			}
			got, err := os.ReadFile(filepath.Join(projDir, childUUID+".jsonl"))
			if err != nil {
				t.Fatalf("review topic did not inherit the PR session: %v", err)
			}
			if string(got) != "pr history\n" {
				t.Errorf("review topic transcript = %q, want the PR's", got)
			}
			if line, _ := db.TopicLineage(folder, reviewTopic); line.ParentTopic == nil || *line.ParentTopic != "pr/5" {
				t.Errorf("parent topic = %v, want pr/5", line.ParentTopic)
			}
		})
	}
}

// The first event on a PR is an inline comment, so no `pr/5` session exists:
// the review topic forks from main as any parentless topic does, and no row is
// invented for a prefix.
func TestRunTurn_PathTopicWithNoSessionAboveForksFromMain(t *testing.T) {
	const (
		jid         = "github:o/r/pr/5"
		reviewTopic = "pr/5/review/77"
	)
	db, loop := newForkLoop(t)
	folder := "demo"
	if err := db.PutGroup(core.Group{Folder: folder}); err != nil {
		t.Fatal(err)
	}
	doSetRoutes(t, db, []core.Route{{Match: "platform=github", Target: folder}})
	projDir := seedParentSession(t, loop, db, folder, "", "main-uuid", []byte("main history\n"))
	if err := db.PutMessage(core.Message{ID: "m1", ChatJID: jid, Sender: "u1",
		Content: "on a.go:\nwhy?", Topic: reviewTopic, Timestamp: time.Now().UTC(),
		Verb: "message"}); err != nil {
		t.Fatal(err)
	}

	if _, err := loop.processGroupMessages(jid); err != nil {
		t.Fatalf("process: %v", err)
	}

	childUUID := db.SessionID(folder, reviewTopic)
	if childUUID == "" {
		t.Fatal("review topic has no session row")
	}
	got, err := os.ReadFile(filepath.Join(projDir, childUUID+".jsonl"))
	if err != nil || string(got) != "main history\n" {
		t.Errorf("review topic transcript = %q (err %v), want main's", got, err)
	}
	for _, prefix := range []string{"pr/5", "pr/5/review", "pr"} {
		if _, ok := db.GetSession(folder, prefix); ok {
			t.Errorf("a session was invented for %q", prefix)
		}
	}
}

// The nearest session wins, and a topic with no "/" has no path to climb.
func TestPathParent_NearestWins(t *testing.T) {
	db, loop := newForkLoop(t)
	folder := "demo"
	for _, topic := range []string{"pr/5", "pr/5/review"} {
		if err := db.PutSession(folder, topic, topic+"-uuid"); err != nil {
			t.Fatal(err)
		}
	}
	if got := loop.pathParent(folder, "pr/5/review/77"); got != "pr/5/review" {
		t.Errorf("pathParent = %q, want pr/5/review", got)
	}
	if got := loop.pathParent(folder, "#deploy"); got != "" {
		t.Errorf("pathParent of a flat topic = %q, want main", got)
	}
}

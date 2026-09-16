package tests

// Conversation-continuity feature tests. BUGS L54 lived for 11 weeks because
// nothing at any tier pinned the CONTRACT between routd (which mints, copies,
// and ships a topic's resume session id) and ant (which refuses to `claude
// --resume` anything that is not a UUID with a transcript on disk). The old
// unit tests asserted bytes-copied with literal "parent-uuid"/"child-uuid"
// ids — non-UUIDs production always rejected — so every fork passed the tests
// and started blind live.
//
// Context reaches a turn in layers with different owners (the harness owns
// the transcript; the platform owns the observed window and the group files),
// and a fork trades the observed window away BECAUSE it inherits the
// transcript (observed_cursor=now). These tests pin the two halves of that
// trade end-to-end: a fork with a parent transcript ships a resumable copy
// and keeps its fork cursor; a fork with none clears the cursor so the
// ambient window covers for the missing transcript. Neither axis may be dark
// on both sides at once.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/routd"
	routdv1 "github.com/kronael/arizuko/routd/api/v1"
	"github.com/kronael/arizuko/store"
	"github.com/kronael/arizuko/tests/testutils"
)

// antResumeRE extracts UUID_RE from ant/src/claude.ts — the ACTUAL guard the
// agent applies to a session id before `claude --resume`. Compiling ant's own
// pattern (not a Go copy that can drift) makes these tests fail if either
// side moves: ant tightening the guard, or routd minting something the guard
// refuses. core/session_id_test.go holds a hand-copied twin; this one cannot
// go stale.
func antResumeRE(t *testing.T) *regexp.Regexp {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("..", "ant", "src", "claude.ts"))
	if err != nil {
		t.Fatalf("read ant/src/claude.ts: %v", err)
	}
	m := regexp.MustCompile(`(?m)^const UUID_RE = /(.+)/(\w*);`).FindSubmatch(src)
	if m == nil {
		t.Fatal("ant/src/claude.ts no longer declares `const UUID_RE = /…/;` — update this extractor to ant's current resume guard")
	}
	pattern := string(m[1])
	if strings.Contains(string(m[2]), "i") {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		t.Fatalf("ant's UUID_RE %q does not compile as a Go regexp: %v", pattern, err)
	}
	return re
}

// TestFeature_MintedSessionIsResumableByAnt is the cross-component drift
// guard: every id core mints must pass ant's own committed resume guard.
// L54 fault 1 in one line — core minted "sess-<nano>", ant refused it, and
// every forked topic answered its first turn with no context.
func TestFeature_MintedSessionIsResumableByAnt(t *testing.T) {
	re := antResumeRE(t)
	for range 8 {
		if id := core.NewSessionID(); !re.MatchString(id) {
			t.Fatalf("core.NewSessionID() = %q; ant's UUID_RE %q refuses it — the agent would start fresh", id, re)
		}
	}
}

// transcriptDir is Claude Code's project dir for a group home: every
// container mounts the group at $HOME=/home/node, so the slug is fixed
// (matches container.CopySession and ant transcriptPath).
func transcriptDir(groupsDir, folder string) string {
	return filepath.Join(groupsDir, folder, ".claude", "projects", "-home-node")
}

func TestFeature_ForkContinuity(t *testing.T) {
	// A topic born inside a chat with a root session must resume a COPY of
	// that session: the id routd ships to the agent is one ant's guard
	// accepts, its transcript exists on disk, and the transcript names the
	// child (a copy still naming the parent is a file two topics claim).
	// Because the transcript carries the history, the fork keeps a cursor —
	// and the value it keeps is the PARENT's, never `now` (BUGS L87).
	t.Run("fork-with-parent-session-resumes-a-restamped-copy", func(t *testing.T) {
		groupsDir := t.TempDir()
		f := bootFederation(t, func(lc *routd.LoopConfig) { lc.GroupsDir = groupsDir })
		if err := f.routdDB.PutGroup(core.Group{Folder: "demo"}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.routdDB.AddRoute(core.Route{Match: "", Target: "demo"}); err != nil {
			t.Fatal(err)
		}

		// The chat's main conversation: a persisted root session with its
		// transcript on disk, as N turns on the root topic leave behind.
		parentUUID := core.NewSessionID()
		if err := f.routdDB.PutSession("demo", "", parentUUID); err != nil {
			t.Fatal(err)
		}
		// The root has read some of the channel's ambient backlog. The fork must
		// hand that exact cursor down: stamping `now` instead discarded the
		// backlog at the moment a thread was created, so a new thread opened
		// blind to everything said before it existed (BUGS L87).
		parentCursor := core.TS(time.Date(2026, 8, 24, 6, 0, 0, 0, time.UTC))
		if err := f.routdDB.AdvanceObservedCursor("demo", "", parentCursor); err != nil {
			t.Fatalf("seed parent cursor: %v", err)
		}
		projDir := transcriptDir(groupsDir, "demo")
		if err := os.MkdirAll(projDir, 0o755); err != nil {
			t.Fatal(err)
		}
		transcript := `{"sessionId":"` + parentUUID + `","type":"user","text":"the launch codeword is heron"}` + "\n" +
			`{"sessionId":"` + parentUUID + `","type":"assistant","text":"noted"}` + "\n"
		if err := os.WriteFile(filepath.Join(projDir, parentUUID+".jsonl"), []byte(transcript), 0o644); err != nil {
			t.Fatal(err)
		}

		// A user replies inside a platform thread: same chat, new topic.
		in := routdv1.Message{ID: "fork.1", ChatJID: "slack:T/C/F", Sender: "u1",
			Content: "what was the codeword?", Verb: "message", Topic: "thread-1"}
		if rec := postBearer(t, f.routdTS.URL, "POST", "/v1/messages", f.authd.mintAdapter(t, "slakd"), "", in); rec.StatusCode != 200 {
			t.Fatalf("ingest status=%d", rec.StatusCode)
		}
		testutils.WaitForBotRow(t, store.NewOver(f.routdDB.Obj()), "slack:T/C/F", 5*time.Second)

		childSID := f.dispatchedSession("fork.1")
		if childSID == "" {
			t.Fatal("routd shipped no session id for the forked topic's first turn")
		}
		if childSID == parentUUID {
			t.Fatal("child topic dispatched the PARENT's session id — two topics would append to one transcript")
		}
		if re := antResumeRE(t); !re.MatchString(childSID) {
			t.Fatalf("dispatched session id %q fails ant's resume guard — the fork's first turn starts blind (BUGS L54)", childSID)
		}
		data, err := os.ReadFile(filepath.Join(projDir, childSID+".jsonl"))
		if err != nil {
			t.Fatalf("child transcript missing — ant would refuse the resume and start fresh: %v", err)
		}
		if !strings.Contains(string(data), `"sessionId":"`+childSID+`"`) {
			t.Fatalf("child transcript not restamped to the child id")
		}
		if strings.Contains(string(data), parentUUID) {
			t.Fatalf("child transcript still names the parent %s", parentUUID)
		}
		if !strings.Contains(string(data), "heron") {
			t.Fatal("child transcript lost the parent conversation's content")
		}
		line, ok := f.routdDB.TopicLineage("demo", "thread-1")
		if !ok {
			t.Fatal("no lineage row for the forked topic")
		}
		if line.ObservedCursor != parentCursor {
			t.Fatalf("observed cursor = %q, want the parent's %q — a fork inherits the parent's ambient window, and a transcript it copied must not clear it",
				line.ObservedCursor, parentCursor)
		}
	})

	// A topic born in a chat whose main has NO session forks from nothing.
	// The transcript axis is dark by construction, so the platform axis must
	// not be: the fork clears its observed_cursor stamp and the child's first
	// prompt gets the folder's ambient window instead. L54 fault 3 was
	// exactly this pair both dark.
	t.Run("fork-without-parent-session-keeps-the-observed-window", func(t *testing.T) {
		groupsDir := t.TempDir()
		f := bootFederation(t, func(lc *routd.LoopConfig) { lc.GroupsDir = groupsDir })
		if err := f.routdDB.PutGroup(core.Group{Folder: "demo"}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.routdDB.AddRoute(core.Route{Match: "", Target: "demo"}); err != nil {
			t.Fatal(err)
		}

		in := routdv1.Message{ID: "fork.2", ChatJID: "slack:T/C/G", Sender: "u1",
			Content: "hello new thread", Verb: "message", Topic: "thread-2"}
		if rec := postBearer(t, f.routdTS.URL, "POST", "/v1/messages", f.authd.mintAdapter(t, "slakd"), "", in); rec.StatusCode != 200 {
			t.Fatalf("ingest status=%d", rec.StatusCode)
		}
		testutils.WaitForBotRow(t, store.NewOver(f.routdDB.Obj()), "slack:T/C/G", 5*time.Second)

		childSID := f.dispatchedSession("fork.2")
		if _, err := os.Stat(filepath.Join(transcriptDir(groupsDir, "demo"), childSID+".jsonl")); err == nil {
			t.Fatalf("a transcript exists for %s though main had no session — where did it come from?", childSID)
		}
		line, ok := f.routdDB.TopicLineage("demo", "thread-2")
		if !ok {
			t.Fatal("no lineage row for the forked topic")
		}
		if line.ObservedCursor != "" {
			t.Fatalf("fork with NO inherited transcript kept observed_cursor=%q — both context axes dark, the child answers its first turn blind (BUGS L54)", line.ObservedCursor)
		}
	})
}

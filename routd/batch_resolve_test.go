package routd

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/store"
)

// PR #311 on krons: three inline mentions arrived in the same second as a
// review row. The review row was newer, matched only the `#observe` fallback,
// and decided the whole batch, so the mentions were marked observed and the
// cursor passed them (BUGS L551, L75b). A batch resolves by its newest row that
// dispatches on its own.
const prJID = "github:kronael/arizuko/pr/311"

func githubBatchLoop(t *testing.T, runner Runner) (*DB, *Loop) {
	t.Helper()
	db, err := OpenMem()
	if err != nil {
		t.Fatalf("open mem: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.PutGroup(core.Group{Folder: "eng"}); err != nil {
		t.Fatal(err)
	}
	routes, err := store.GitHubSourceRoutes("repo/kronael/arizuko", "eng")
	if err != nil {
		t.Fatal(err)
	}
	doSetRoutes(t, db, routes)
	loop := NewLoop(db, runner, LoopConfig{ObserveWindowMessages: 20, ObserveWindowChars: 1000})
	t.Cleanup(loop.StopQueue)
	return db, loop
}

func putPRRow(t *testing.T, db *DB, id, sender, verb, content string, at time.Time) core.Message {
	t.Helper()
	m := core.Message{ID: id, ChatJID: prJID, Sender: sender, Topic: "pr/311",
		Content: content, Timestamp: at, Verb: verb}
	if err := db.PutMessage(m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestBatchResolve_AMentionBeatsANewerReviewRowInTheWorker(t *testing.T) {
	rr := &recRunner{}
	db, loop := githubBatchLoop(t, rr)
	now := time.Now().UTC()
	putPRRow(t, db, "gh-rc1", "github:user/alice", "mention", "inline mention", now)
	review := putPRRow(t, db, "gh-r1", "github:user/alice", "pull_request_review", "review submitted", now.Add(time.Millisecond))

	if _, err := loop.processGroupMessages(prJID); err != nil {
		t.Fatalf("process: %v", err)
	}
	if len(rr.runs) != 1 {
		t.Fatalf("turns=%d want 1: the mention dispatches on its own: %+v", len(rr.runs), rr.runs)
	}
	run := rr.runs[0]
	if run.folder != "eng" {
		t.Fatalf("folder=%q want eng", run.folder)
	}
	if !strings.Contains(run.batch, `<message id="gh-rc1"`) {
		t.Fatalf("the mention is not the request:\n%s", run.batch)
	}
	if !strings.Contains(run.batch, `<observed id="gh-r1"`) {
		t.Fatalf("the review row is not in the <observed> block:\n%s", run.batch)
	}
	if got := msgByID(t, db, prJID, "gh-r1").RoutedTo; got != "eng" {
		t.Fatalf("review row routed_to=%q want eng", got)
	}
	if got := msgByID(t, db, prJID, "gh-rc1").RoutedTo; got == "eng" {
		t.Fatalf("the mention was stored as observed context")
	}
	if got, want := db.GetAgentCursor(prJID), store.MessageCursor(review); got != want {
		t.Fatalf("cursor=%q want %q (past both rows)", got, want)
	}
}

func TestBatchResolve_ThePollBackstopEnqueuesTheMentionAndObservesNothing(t *testing.T) {
	db, loop := githubBatchLoop(t, &recRunner{})
	got := make(chan string, 4)
	loop.q.SetProcessMessagesFn(func(jid string) (bool, error) { got <- jid; return false, nil })
	now := time.Now().UTC()
	putPRRow(t, db, "gh-rc1", "github:user/alice", "mention", "inline mention", now)
	putPRRow(t, db, "gh-r1", "github:user/alice", "pull_request_review", "review submitted", now.Add(time.Millisecond))

	loop.pollOnce()

	select {
	case jid := <-got:
		if jid != prJID {
			t.Fatalf("enqueued %q want %q", jid, prJID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the chat was not enqueued: the newer review row decided the batch")
	}
	for _, id := range []string{"gh-rc1", "gh-r1"} {
		if got := msgByID(t, db, prJID, id).RoutedTo; got != "" {
			t.Fatalf("the poll backstop stored %s as observed under %q", id, got)
		}
	}
	if cur := db.GetAgentCursor(prJID); cur != "" {
		t.Fatalf("the poll backstop moved the cursor to %q", cur)
	}
}

func TestBatchResolve_ACIRowFromAnotherSenderStartsNoTurnOfItsOwn(t *testing.T) {
	rr := &recRunner{}
	db, loop := githubBatchLoop(t, rr)
	now := time.Now().UTC()
	putPRRow(t, db, "gh-ci1", "github:user/ci", "check_run", "tests failed", now)
	putPRRow(t, db, "gh-rc1", "github:user/alice", "mention", "inline mention", now.Add(time.Millisecond))

	if _, err := loop.processGroupMessages(prJID); err != nil {
		t.Fatalf("process: %v", err)
	}
	if len(rr.runs) != 1 {
		t.Fatalf("turns=%d want exactly 1: %+v", len(rr.runs), rr.runs)
	}
	if rr.runs[0].trigger != "github:user/alice" {
		t.Fatalf("trigger=%q want the mention's sender", rr.runs[0].trigger)
	}
	if !strings.Contains(rr.runs[0].batch, `<observed id="gh-ci1"`) {
		t.Fatalf("the CI row is not in the <observed> block:\n%s", rr.runs[0].batch)
	}
}

func TestBatchResolve_ABusyRefusalThenANewerCIRowStillRunsTheMention(t *testing.T) {
	var batches []string
	busy := true
	db, loop := githubBatchLoop(t, runnerFn(func(_ context.Context, req runedv1.RunRequest) (runedv1.RunOutcome, error) {
		if busy {
			return runedv1.RunOutcome{Busy: true}, nil
		}
		batches = append(batches, req.MessageBatch)
		return runedv1.RunOutcome{RunID: "r", Outcome: runedv1.OutcomeOK}, nil
	}))
	enqueued := make(chan string, 4)
	loop.q.SetProcessMessagesFn(func(jid string) (bool, error) { enqueued <- jid; return false, nil })
	now := time.Now().UTC()
	putPRRow(t, db, "gh-rc1", "github:user/alice", "mention", "inline mention", now)

	if _, err := loop.processGroupMessages(prJID); err != nil {
		t.Fatalf("busy attempt: %v", err)
	}
	if cur := db.GetAgentCursor(prJID); cur != "" {
		t.Fatalf("busy refusal moved the cursor to %q", cur)
	}

	putPRRow(t, db, "gh-ci1", "github:user/ci", "check_run", "tests failed", now.Add(time.Second))
	loop.pollOnce()
	select {
	case <-enqueued:
	case <-time.After(2 * time.Second):
		t.Fatal("the CI row made the poll backstop drop the pending mention")
	}
	if got := msgByID(t, db, prJID, "gh-rc1").RoutedTo; got != "" {
		t.Fatalf("the pending mention was stored as observed under %q", got)
	}

	busy = false
	if _, err := loop.processGroupMessages(prJID); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if len(batches) != 1 || !strings.Contains(batches[0], `<message id="gh-rc1"`) ||
		!strings.Contains(batches[0], `<observed id="gh-ci1"`) {
		t.Fatalf("retry turns=%d, want one for the mention with the CI row observed: %q", len(batches), batches)
	}
}

func TestBatchResolve_ACommandThenAMentionThenAReviewRowStillRoutesTheMention(t *testing.T) {
	rr := &recRunner{}
	db, loop := githubBatchLoop(t, rr)
	now := time.Now().UTC()
	putPRRow(t, db, "gh-c1", "github:user/alice", "message", "/ping", now)
	putPRRow(t, db, "gh-rc1", "github:user/alice", "mention", "inline mention", now.Add(time.Millisecond))
	putPRRow(t, db, "gh-r1", "github:user/alice", "pull_request_review", "review submitted", now.Add(2*time.Millisecond))

	if _, err := loop.processGroupMessages(prJID); err != nil {
		t.Fatalf("first poll: %v", err)
	}
	if len(rr.runs) != 0 {
		t.Fatalf("first poll ran %d turns, want 0: the command is steered: %+v", len(rr.runs), rr.runs)
	}
	if _, err := loop.processGroupMessages(prJID); err != nil {
		t.Fatalf("second poll: %v", err)
	}
	if len(rr.runs) != 1 || !strings.Contains(rr.runs[0].batch, `<message id="gh-rc1"`) ||
		!strings.Contains(rr.runs[0].batch, `<observed id="gh-r1"`) {
		t.Fatalf("second poll turns=%d, want one for the mention with the review observed: %+v", len(rr.runs), rr.runs)
	}
}

func TestBatchResolve_NoRowDispatchesStaysARouteMiss(t *testing.T) {
	rr := &recRunner{}
	db, loop := githubBatchLoop(t, rr)
	now := time.Now().UTC()
	putPRRow(t, db, "gh-ci1", "github:user/ci", "check_run", "tests failed", now)
	review := putPRRow(t, db, "gh-r1", "github:user/alice", "pull_request_review", "review submitted", now.Add(time.Millisecond))

	if _, err := loop.processGroupMessages(prJID); err != nil {
		t.Fatalf("process: %v", err)
	}
	if len(rr.runs) != 0 {
		t.Fatalf("turns=%d want 0: no row dispatches", len(rr.runs))
	}
	for _, id := range []string{"gh-ci1", "gh-r1"} {
		if got := msgByID(t, db, prJID, id).RoutedTo; got != "eng" {
			t.Fatalf("%s routed_to=%q want eng (observe rule)", id, got)
		}
	}
	if got, want := db.GetAgentCursor(prJID), store.MessageCursor(review); got != want {
		t.Fatalf("cursor=%q want %q", got, want)
	}
}

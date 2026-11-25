package routd

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/store"
)

// failedTurnFixture leaves folder demo's turn deferred after one failed run, with
// the breaker open on it. It answers the turn and the breaker as stored.
func failedTurnFixture(t *testing.T) (*DB, *Loop, TurnContext, store.TurnControlState, func(time.Time)) {
	t.Helper()
	db, loop, _, tc, _ := deferredFixture(t)
	now := tc.NextAttemptAt
	loop.q.now = func() time.Time { return now }
	loop.runner = runnerFn(func(context.Context, runedv1.RunRequest) (runedv1.RunOutcome, error) {
		return runedv1.RunOutcome{Outcome: runedv1.OutcomeError}, nil
	})
	if _, err := loop.replay(tc.TurnID); err != nil {
		t.Fatal(err)
	}
	tc, _ = db.GetTurnContext(tc.TurnID)
	control, _, err := db.store().ReadTurnControl(t.Context())
	if err != nil || control.Phase != "open" || control.ProbeTurnID != tc.TurnID {
		t.Fatalf("breaker after one failure: %+v, %v", control, err)
	}
	return db, loop, tc, control, func(at time.Time) { now = at }
}

func otherFolderMessage(t *testing.T, db *DB, folder string, at time.Time) core.Message {
	t.Helper()
	if err := db.PutGroup(core.Group{Folder: folder}); err != nil {
		t.Fatal(err)
	}
	msg := core.Message{ID: "fresh-" + folder, ChatJID: "web:" + folder, Sender: "user",
		Content: "question", Timestamp: at}
	if err := db.PutMessage(msg); err != nil {
		t.Fatal(err)
	}
	return msg
}

// One folder's failed run holds that folder only. Another folder's turn runs
// during the cooldown, and its success leaves the breaker as it was: the failed
// folder still waits for its own probe.
func TestBreakerHoldsOnlyTheFailedFolder(t *testing.T) {
	db, loop, tc, control, _ := failedTurnFixture(t)
	if control.Folder != tc.Folder {
		t.Fatalf("breaker holds %q, want the failed folder %q", control.Folder, tc.Folder)
	}
	fresh := otherFolderMessage(t, db, "other", loop.q.now())
	launches := 0
	loop.runner = runnerFn(func(context.Context, runedv1.RunRequest) (runedv1.RunOutcome, error) {
		launches++
		return runedv1.RunOutcome{Outcome: runedv1.OutcomeSilent}, nil
	})
	if _, _, _, err := loop.runTurn("other", "", fresh.ChatJID, fresh.ID, false, []core.Message{fresh}, nil); err != nil {
		t.Fatal(err)
	}
	if launches != 1 {
		t.Fatalf("another folder's turn launched %d times during the failed folder's cooldown, want 1", launches)
	}
	after, _, _ := db.store().ReadTurnControl(t.Context())
	if after != control {
		t.Fatalf("another folder's success changed the breaker: %+v", after)
	}
	next := core.Message{ID: "next", ChatJID: tc.ChatJID, Sender: "user", Content: "again",
		Timestamp: loop.q.now().Add(time.Second)}
	if err := db.PutMessage(next); err != nil {
		t.Fatal(err)
	}
	_, live, err := loop.q.admit(loop, TurnContext{TurnID: next.ID, Folder: tc.Folder, ChatJID: next.ChatJID},
		&store.TurnAdmission{AttemptID: "held"}, nil)
	if live || !errors.Is(err, errTurnBlocked) {
		t.Fatalf("the failed folder was admitted during its cooldown: live=%v err=%v", live, err)
	}
}

// A second folder that fails while the first is held waits on its own retry:
// the breaker stays on the first folder, and a third folder still runs.
func TestBreakerStaysOnItsFolderWhenASecondFolderFails(t *testing.T) {
	db, loop, _, control, _ := failedTurnFixture(t)
	fresh := otherFolderMessage(t, db, "other", loop.q.now())
	loop.runner = runnerFn(func(context.Context, runedv1.RunRequest) (runedv1.RunOutcome, error) {
		return runedv1.RunOutcome{Outcome: runedv1.OutcomeError}, nil
	})
	if _, _, _, err := loop.runTurn("other", "", fresh.ChatJID, fresh.ID, false, []core.Message{fresh}, nil); err != nil {
		t.Fatal(err)
	}
	after, _, _ := db.store().ReadTurnControl(t.Context())
	if after.Phase != "open" || after.Folder != control.Folder || after.ProbeTurnID != control.ProbeTurnID {
		t.Fatalf("breaker after a second folder failed: %+v", after)
	}
	second, _ := db.GetTurnContext(fresh.ID)
	if second.State != store.TurnPendingRetry || !second.NextAttemptAt.Equal(loop.q.now().Add(30*time.Second)) {
		t.Fatalf("the second folder's failed turn is not waiting on its own retry: %+v", second)
	}
	third := otherFolderMessage(t, db, "third", loop.q.now())
	_, live, err := loop.q.admit(loop, TurnContext{TurnID: third.ID, Folder: "third", ChatJID: third.ChatJID},
		&store.TurnAdmission{AttemptID: "free"}, nil)
	if err != nil || !live {
		t.Fatalf("a third folder was refused: live=%v err=%v", live, err)
	}
}

// A failure that is never retried — the agent replied and then exited, a run
// that runed marked terminal — has no retry to probe with. It holds its folder
// for the first retry delay, and the folder's next turn is the probe. The hour
// is for a turn that used its whole budget.
func TestBreakerHoldsThirtySecondsAfterAFailureThatIsNotRetried(t *testing.T) {
	db, loop, _ := recLoop(t)
	if err := db.PutGroup(core.Group{Folder: "demo"}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	loop.q.now = func() time.Time { return now }
	first := core.Message{ID: "first", ChatJID: "web:demo", Sender: "user", Content: "question", Timestamp: now}
	if err := db.PutMessage(first); err != nil {
		t.Fatal(err)
	}
	launches := 0
	loop.runner = runnerFn(func(context.Context, runedv1.RunRequest) (runedv1.RunOutcome, error) {
		launches++
		return runedv1.RunOutcome{Outcome: runedv1.OutcomeError, Terminal: true}, nil
	})
	if _, _, _, err := loop.runTurn("demo", "", first.ChatJID, first.ID, false, []core.Message{first}, nil); err != nil {
		t.Fatal(err)
	}
	control, _, _ := db.store().ReadTurnControl(t.Context())
	if control.Phase != "open" || control.Folder != "demo" || !control.NextProbeAt.Equal(now.Add(30*time.Second)) {
		t.Fatalf("breaker after a failure that is not retried: %+v", control)
	}
	if err := db.store().AdvanceAgentCursor(first.ChatJID, store.MessageCursor(first)); err != nil {
		t.Fatal(err)
	}
	second := core.Message{ID: "second", ChatJID: "web:demo", Sender: "user", Content: "again",
		Timestamp: now.Add(time.Second)}
	if err := db.PutMessage(second); err != nil {
		t.Fatal(err)
	}
	loop.runner = runnerFn(func(context.Context, runedv1.RunRequest) (runedv1.RunOutcome, error) {
		launches++
		return runedv1.RunOutcome{Outcome: runedv1.OutcomeSilent}, nil
	})
	runSecond := func() {
		t.Helper()
		if _, _, _, err := loop.runTurn("demo", "", second.ChatJID, second.ID, false, []core.Message{second}, nil); err != nil {
			t.Fatal(err)
		}
	}
	runSecond()
	if launches != 1 {
		t.Fatalf("the held folder launched during its cooldown: %d launches", launches)
	}
	now = control.NextProbeAt
	runSecond()
	closed, _, _ := db.store().ReadTurnControl(t.Context())
	if launches != 2 || closed.Phase != "closed" {
		t.Fatalf("the folder's next turn did not probe and close: launches=%d control=%+v", launches, closed)
	}
}

// No admission waits for one named turn. A probe owner that is not due yet, or
// one that waits behind older input of its folder, holds nobody: without this
// the instance stays closed until a person rearms it.
func TestBreakerProbeNeverWaitsOnATurnThatCannotRun(t *testing.T) {
	t.Run("owner not due", func(t *testing.T) {
		db, loop, tc, control, setNow := failedTurnFixture(t)
		everyFolder := control
		everyFolder.Folder = ""
		if ok, err := db.store().WriteTurnControl(t.Context(), &control, everyFolder); err != nil || !ok {
			t.Fatalf("hold every folder: %v %v", ok, err)
		}
		later := tc.NextAttemptAt.Add(time.Hour)
		if ok, err := db.store().RequeueDeferredTurn(t.Context(), tc, later, ""); err != nil || !ok {
			t.Fatalf("requeue the owner: %v %v", ok, err)
		}
		setNow(control.NextProbeAt)
		fresh := otherFolderMessage(t, db, "other", control.NextProbeAt)
		work, live, err := loop.q.admit(loop, TurnContext{TurnID: fresh.ID, Folder: "other", ChatJID: fresh.ChatJID},
			&store.TurnAdmission{AttemptID: "probe"}, nil)
		if err != nil || !live || work.probe == nil {
			t.Fatalf("no turn may probe while the owner waits for %s: live=%v probe=%v err=%v",
				later, live, work.probe, err)
		}
	})
	t.Run("owner behind older input of its folder", func(t *testing.T) {
		db, loop, tc, control, setNow := failedTurnFixture(t)
		setNow(control.NextProbeAt)
		older := core.Message{ID: "older", ChatJID: "web:demo-second", Sender: "user", Content: "first asked",
			Timestamp: control.NextProbeAt.Add(-time.Hour)}
		if err := db.PutMessage(older); err != nil {
			t.Fatal(err)
		}
		work, live, err := loop.q.admit(loop, TurnContext{TurnID: older.ID, Folder: tc.Folder, ChatJID: older.ChatJID},
			&store.TurnAdmission{AttemptID: "probe"}, nil)
		if err != nil || !live || work.probe == nil {
			t.Fatalf("the held folder's older input may not probe: live=%v probe=%v err=%v", live, work.probe, err)
		}
	})
}

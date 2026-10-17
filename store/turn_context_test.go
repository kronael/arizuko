package store

// turn_context_test.go proves the three properties a turn's lifecycle rests on:
// a finished turn is never re-opened, a retry moves its count and its state
// together, and the expiry sweep measures time rather than width.
//
// THE INTERLEAVES ARE WRITTEN OUT rather than raced, and that is stronger than
// a race rather than weaker: every decision here is a write's own condition, so
// two writers are serialised by the engine and not by a read either performed —
// one call commits, the next is the one that lost, and each attempt is its own
// transaction.
//
// WHY THERE IS NO SLIP DOUBLE. conformance.SlipAt forces an interleave by firing
// between a read and the write that follows it, and it wraps an objstore.Store —
// which this package builds per call from its own pool (store.Obj) rather than
// holding, so there is nowhere to hand it one. The single body that reads before
// it writes is RecordRetryAttempt, and its window is driven below by moving the
// count between the read this test performs and the guarded write it then
// issues: the same interleave, named rather than injected.

import (
	"context"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// A turn that has already finished must NOT be re-opened. When an earlier batch
// completes and a later batch in the same poll steers, the cursor does not
// advance and the next poll re-feeds the completed batch; re-dispatching it
// replays finished output into the chat.
//
// Drop the `state != done` condition in OpenTurnContext and the second open
// answers live.
func TestAFinishedTurnIsNotReopened(t *testing.T) {
	s := openEntities(t)
	live, err := s.OpenTurnContext("t1", "demo", "", "tg:1", "u1", "")
	if err != nil || !live {
		t.Fatalf("the first open answered live=%v err=%v", live, err)
	}
	if err := s.SetTurnState("t1", TurnDone); err != nil {
		t.Fatal(err)
	}
	live, err = s.OpenTurnContext("t1", "demo", "", "tg:1", "u1", "")
	if err != nil {
		t.Fatal(err)
	}
	if live {
		t.Fatal("a turn that already reported its outcome was re-opened; the completed " +
			"batch is dispatched again and its answer is delivered twice")
	}
	if tc, ok := s.TurnContextOf("t1"); !ok || tc.State != TurnDone {
		t.Errorf("the turn reads back state=%q, want %q: the refused open still wrote",
			tc.State, TurnDone)
	}

	// A turn that is NOT done still re-opens, or the guard would pass by
	// refusing everything.
	if err := s.SetTurnState("t1", TurnExpired); err != nil {
		t.Fatal(err)
	}
	live, err = s.OpenTurnContext("t1", "demo", "", "tg:1", "u1", "")
	if err != nil || !live {
		t.Fatalf("an expired turn answered live=%v err=%v; expired is terminal for crash "+
			"recovery and NOT for re-dispatch", live, err)
	}
}

// A re-open replaces the binding and clears the run-returned flag in ONE write.
// A turn re-opened with the previous attempt's flag still set answers every late
// callback of the new attempt with turn_done.
func TestAReopenedTurnCarriesNothingFromItsLastAttempt(t *testing.T) {
	s := openEntities(t)
	if _, err := s.OpenTurnContext("t1", "demo", "#old", "tg:1", "u1", "tg:origin"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRunReturned("t1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenTurnContext("t1", "demo", "", "tg:1", "u2", ""); err != nil {
		t.Fatal(err)
	}
	tc, ok := s.TurnContextOf("t1")
	if !ok {
		t.Fatal("the turn lost its item")
	}
	if tc.RunReturned {
		t.Error("the re-opened turn still says its run returned; every callback of the new " +
			"attempt is answered turn_done")
	}
	if tc.ReturnTo != "" {
		t.Errorf("return_to = %q after a re-open with none; the next answer is delivered to "+
			"the previous delegator", tc.ReturnTo)
	}
	if tc.Topic != "" {
		t.Errorf("topic = %q after a re-open with none", tc.Topic)
	}
	if tc.Trigger != "u2" {
		t.Errorf("trigger = %q, want the re-open's own sender", tc.Trigger)
	}
}

// The retry count and the pending_retry state are ONE write. A turn whose count
// moved but whose state stayed running has burned an attempt and is still fed to
// crash recovery as live.
func TestARetryMovesTheCountAndTheStateTogether(t *testing.T) {
	s := openEntities(t)
	if _, err := s.OpenTurnContext("t1", "demo", "", "tg:1", "u1", ""); err != nil {
		t.Fatal(err)
	}
	for want := 1; want <= 3; want++ {
		got, err := s.RecordRetryAttempt("t1")
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("attempt %d reported as %d", want, got)
		}
		tc, _ := s.TurnContextOf("t1")
		if tc.State != TurnPendingRetry {
			t.Fatalf("after attempt %d the turn is %q, want %q: the count moved without the "+
				"state, so crash recovery still feeds it as live", want, tc.State,
				TurnPendingRetry)
		}
		if tc.RetryCount != want {
			t.Fatalf("the turn reads back %d attempts, want %d", tc.RetryCount, want)
		}
		// The re-dispatch is what puts the turn back in flight, so the next
		// attempt is counted from a re-bound turn the way dispatch counts it.
		if _, err := s.OpenTurnContext("t1", "demo", "", "tg:1", "u1", ""); err != nil {
			t.Fatal(err)
		}
	}
}

// The retry is the one body here that READS before it writes, so this drives
// that window by hand: it takes the read, lets a competing attempt commit, and
// then issues the guarded write the loser would have issued. The refusal is what
// sends the loop back for a fresh count instead of writing one already taken.
func TestARetryWriteGuardedOnAStaleCountIsRefused(t *testing.T) {
	s := openEntities(t)
	ctx := context.Background()
	if _, err := s.OpenTurnContext("t1", "demo", "", "tg:1", "u1", ""); err != nil {
		t.Fatal(err)
	}
	key, err := turnKey("t1")
	if err != nil {
		t.Fatal(err)
	}
	// The read a losing attempt would have taken: no count yet.
	it, ok, err := s.itemAt(ctx, TurnContexts, key)
	if err != nil || !ok {
		t.Fatalf("read the turn: ok=%v err=%v", ok, err)
	}
	if _, has := it.Attrs[attrTurnRetryCount]; has {
		t.Fatal("a fresh turn already carries a retry count")
	}
	// The competitor commits inside the window.
	if got, err := s.RecordRetryAttempt("t1"); err != nil || got != 1 {
		t.Fatalf("the winning attempt reported %d err=%v", got, err)
	}
	// The write the loser was about to issue, with the guard it read.
	moved, err := s.TryWriteEntities(ctx, nil, objstore.Write{
		Op: objstore.Update, Entity: TurnContexts, Key: key,
		Attrs: map[string]string{
			attrTurnRetryCount: "1",
			attrTurnState:      TurnPendingRetry,
		},
		Cond: objstore.Cond{Where: []objstore.Cmp{
			{Attr: attrTurnRetryCount, Op: objstore.Absent},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if moved {
		t.Fatal("a retry guarded on a count that had already moved committed anyway; two " +
			"attempts claim one number and one of them is lost")
	}
	// The loop's answer to that refusal is a fresh read, so the next attempt is
	// the next number and not a repeat.
	next, err := s.RecordRetryAttempt("t1")
	if err != nil {
		t.Fatal(err)
	}
	if next != 2 {
		t.Fatalf("the attempt after a lost race reported %d, want 2", next)
	}
}

// The expiry sweep compares an INSTANT, and the live rows carry four widths — so
// with a short value the comparison answers by width and a stale turn survives
// its own sweep. The declaration refuses the short value at write time; this is
// the sweep working on values that got past it.
func TestTheExpirySweepMovesOnlyTheStaleRunningTurns(t *testing.T) {
	s := openEntities(t)
	ctx := context.Background()
	// SeedItem is the only writer that can put `started_at` in the past: the
	// live one stamps the instant itself.
	stale := core.TS(time.Now().Add(-2 * time.Hour))
	for _, seed := range []struct{ turnID, jid, state string }{
		{"stale", "tg:1", TurnRunning},
		{"stale-but-done", "tg:2", TurnDone},
	} {
		if err := s.SeedItem(ctx, TurnContexts, []string{seed.turnID}, map[string]string{
			"folder": "demo", "chat_jid": seed.jid, "state": seed.state,
			"started_at": stale,
		}); err != nil {
			t.Fatalf("seed the stale turn %s: %v", seed.turnID, err)
		}
	}
	if _, err := s.OpenTurnContext("fresh", "demo", "", "tg:3", "u1", ""); err != nil {
		t.Fatal(err)
	}

	swept, err := s.SweepExpiredRunning(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if swept != 1 {
		t.Fatalf("the sweep moved %d turns, want 1: it reached a terminal turn or missed "+
			"the stale one", swept)
	}
	for id, want := range map[string]string{
		"stale": TurnExpired, "stale-but-done": TurnDone, "fresh": TurnRunning,
	} {
		tc, ok := s.TurnContextOf(id)
		if !ok {
			t.Fatalf("%s lost its item", id)
		}
		if tc.State != want {
			t.Errorf("%s is %q, want %q", id, tc.State, want)
		}
	}
}

// Crash recovery re-feeds running and pending_retry turns and NOTHING else. The
// driver drops the terminal majority and the exact set is named in the reader,
// so a state added later stays out of the feed until someone decides it belongs
// there.
func TestCrashRecoveryFeedsOnlyTheTwoLiveStates(t *testing.T) {
	s := openEntities(t)
	for id, state := range map[string]string{
		"a": TurnRunning, "b": TurnPendingRetry, "c": TurnDone, "d": TurnExpired,
	} {
		if _, err := s.OpenTurnContext(id, "demo", "", "tg:"+id, "u1", ""); err != nil {
			t.Fatal(err)
		}
		if err := s.SetTurnState(id, state); err != nil {
			t.Fatal(err)
		}
	}
	live, err := s.RunningTurns()
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 2 {
		t.Fatalf("crash recovery would re-feed %d turns, want 2 (%+v)", len(live), live)
	}
	for _, tc := range live {
		if tc.State != TurnRunning && tc.State != TurnPendingRetry {
			t.Errorf("a %q turn is in the crash-recovery feed", tc.State)
		}
	}
}

// The gateway key is a POINTER and the only one: arizuko stores which key
// metered a turn and never the number, so GET /v1/cost resolves this hash and
// asks the gateway. An empty answer is "no meter holds a number", which the
// endpoint reports as a 404 rather than a zero.
func TestTheGatewayKeyPointerSurvivesTheTurnsWholeLifecycle(t *testing.T) {
	s := openEntities(t)
	if _, err := s.OpenTurnContext("t1", "demo", "", "tg:1", "u1", ""); err != nil {
		t.Fatal(err)
	}
	if hash, err := s.TurnGatewayKey("t1"); err != nil || hash != "" {
		t.Fatalf("a turn that ran with no gateway answered %q err=%v, want empty", hash, err)
	}
	if err := s.SetTurnGatewayKey("t1", "sha256:abc"); err != nil {
		t.Fatal(err)
	}
	// Every later write on this turn is a MERGE, so none of them may drop it.
	if err := s.SetTurnRunID("t1", "run-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordRetryAttempt("t1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenTurnContext("t1", "demo", "", "tg:1", "u1", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTurnState("t1", TurnDone); err != nil {
		t.Fatal(err)
	}
	hash, err := s.TurnGatewayKey("t1")
	if err != nil {
		t.Fatal(err)
	}
	if hash != "sha256:abc" {
		t.Fatalf("the gateway key reads back %q; the turn's cost is unreadable and anteval's "+
			"settle barrier is a timeout", hash)
	}
}

// A turn's own item may only be written through a whole coordinate, and a turn
// with no id has none.
func TestATurnWithNoIdIsRefused(t *testing.T) {
	s := openEntities(t)
	if _, err := s.OpenTurnContext("", "demo", "", "tg:1", "u1", ""); err == nil {
		t.Error("a turn with no id was bound")
	}
	if n, err := s.CountTurnContexts(context.Background()); err != nil || n != 0 {
		t.Fatalf("%d turns landed (err=%v)", n, err)
	}
}

// finishTurn clears presence on every chat the turn's run owns, and it finds
// that run through the turn itself.
func TestATurnReadsBackItsRunID(t *testing.T) {
	s := openEntities(t)
	if _, err := s.OpenTurnContext("t1", "demo", "", "tg:1", "u1", ""); err != nil {
		t.Fatal(err)
	}
	if tc, _ := s.TurnContextOf("t1"); tc.RunID != "" {
		t.Fatalf("a turn nobody ran reads run id %q, want empty", tc.RunID)
	}
	if err := s.SetTurnRunID("t1", "run-1"); err != nil {
		t.Fatal(err)
	}
	if tc, _ := s.TurnContextOf("t1"); tc.RunID != "run-1" {
		t.Fatalf("run id = %q, want run-1", tc.RunID)
	}
}

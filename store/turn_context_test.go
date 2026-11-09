package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

func openTurnEntities(t *testing.T) *Store {
	t.Helper()
	s := openEntities(t)
	if err := s.PutGroup(core.Group{Folder: "demo"}); err != nil {
		t.Fatal(err)
	}
	return s
}

// A turn that has already finished must NOT be re-opened. When an earlier batch
// completes and a later batch in the same poll steers, the cursor does not
// advance and the next poll re-feeds the completed batch; re-dispatching it
// replays finished output into the chat.
//
// Drop the `state != done` condition in OpenTurnContext and the second open
// answers live.
func TestAFinishedTurnIsNotReopened(t *testing.T) {
	s := openTurnEntities(t)
	live, err := s.OpenTurnContext("t1", "demo", "", "tg:1", "u1", "", nil)
	if err != nil || !live {
		t.Fatalf("the first open answered live=%v err=%v", live, err)
	}
	if err := s.SetTurnState("t1", TurnDone); err != nil {
		t.Fatal(err)
	}
	live, err = s.OpenTurnContext("t1", "demo", "", "tg:1", "u1", "", nil)
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
	live, err = s.OpenTurnContext("t1", "demo", "", "tg:1", "u1", "", nil)
	if err != nil || !live {
		t.Fatalf("an expired turn answered live=%v err=%v; expired is terminal for crash "+
			"recovery and NOT for re-dispatch", live, err)
	}
}

// A re-open replaces the binding and clears the run-returned flag in ONE write.
// A turn re-opened with the previous attempt's flag still set answers every late
// callback of the new attempt with turn_done.
func TestAReopenedTurnCarriesNothingFromItsLastAttempt(t *testing.T) {
	s := openTurnEntities(t)
	if _, err := s.OpenTurnContext("t1", "demo", "#old", "tg:1", "u1", "tg:origin", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRunReturned("t1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenTurnContext("t1", "demo", "", "tg:1", "u2", "", nil); err != nil {
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

func deferTurnForTest(t *testing.T, s *Store, turnID string) TurnContext {
	t.Helper()
	tc, _ := s.TurnContextOf(turnID)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if len(tc.InputKeys) == 0 {
		msg, err := s.AppendMessage(core.Message{ID: turnID, ChatJID: tc.ChatJID, Timestamp: now})
		if err != nil {
			t.Fatal(err)
		}
		key, err := messageKeyOf(msg)
		if err != nil {
			t.Fatal(err)
		}
		tc.InputKeys, tc.InputLast = []objstore.Key{key}, MessageCursor(msg)
	}
	tc.NextAttemptAt = now.Add(time.Duration(tc.RetryCount+1) * time.Hour)
	cursor, err := s.AgentCursor(tc.ChatJID)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.DeferTurn(t.Context(), DeferredTurn{Turn: tc, PriorCursor: cursor}); err != nil || !ok {
		t.Fatalf("defer: %v %v", ok, err)
	}
	tc, _ = s.TurnContextOf(turnID)
	return tc
}

func TestDeferredTurnCountAndState(t *testing.T) {
	s := openTurnEntities(t)
	if _, err := s.OpenTurnContext("t1", "demo", "", "tg:1", "u1", "", nil); err != nil {
		t.Fatal(err)
	}
	for want := 1; want <= 3; want++ {
		tc := deferTurnForTest(t, s, "t1")
		if tc.State != TurnPendingRetry {
			t.Fatalf("after attempt %d the turn is %q, want %q: the count moved without the "+
				"state, so crash recovery still feeds it as live", want, tc.State,
				TurnPendingRetry)
		}
		if tc.RetryCount != want {
			t.Fatalf("the turn reads back %d attempts, want %d", tc.RetryCount, want)
		}
		if ok, err := s.ClaimDeferredTurn(t.Context(), tc, tc.AttemptID+"next", tc.NextAttemptAt); err != nil || !ok {
			t.Fatalf("claim: %v %v", ok, err)
		}
	}
}

func TestDeferredTurnStaleCount(t *testing.T) {
	s := openTurnEntities(t)
	if _, err := s.OpenTurnContext("t1", "demo", "", "tg:1", "u1", "", nil); err != nil {
		t.Fatal(err)
	}
	tc := deferTurnForTest(t, s, "t1")
	if ok, err := s.ClaimDeferredTurn(t.Context(), tc, "next", tc.NextAttemptAt); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	tc, _ = s.TurnContextOf("t1")
	tc.RetryCount, tc.NextAttemptAt = 0, time.Date(2026, 1, 1, 2, 0, 0, 0, time.UTC)
	cursor, err := s.AgentCursor(tc.ChatJID)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.DeferTurn(t.Context(), DeferredTurn{Turn: tc, PriorCursor: cursor}); err != nil || ok {
		t.Fatalf("stale count committed: %v %v", ok, err)
	}
	next := deferTurnForTest(t, s, "t1")
	if next.RetryCount != 2 {
		t.Fatalf("the attempt after a lost race reported %d, want 2", next.RetryCount)
	}
}

// The expiry sweep compares an INSTANT, and the live rows carry four widths — so
// with a short value the comparison answers by width and a stale turn survives
// its own sweep. The declaration refuses the short value at write time; this is
// the sweep working on values that got past it.
func TestTheExpirySweepMovesOnlyTheStaleRunningTurns(t *testing.T) {
	s := openTurnEntities(t)
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
	if _, err := s.OpenTurnContext("fresh", "demo", "", "tg:3", "u1", "", nil); err != nil {
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
	s := openTurnEntities(t)
	for id, state := range map[string]string{
		"a": TurnRunning, "b": TurnPendingRetry, "c": TurnDone, "d": TurnExpired,
	} {
		if _, err := s.OpenTurnContext(id, "demo", "", "tg:"+id, "u1", "", nil); err != nil {
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
	s := openTurnEntities(t)
	if _, err := s.OpenTurnContext("t1", "demo", "", "tg:1", "u1", "", nil); err != nil {
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
	tc := deferTurnForTest(t, s, "t1")
	if ok, err := s.ClaimDeferredTurn(t.Context(), tc, "next", tc.NextAttemptAt); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
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
	if _, err := s.OpenTurnContext("", "demo", "", "tg:1", "u1", "", nil); err == nil {
		t.Error("a turn with no id was bound")
	}
	if n, err := s.CountTurnContexts(context.Background()); err != nil || n != 0 {
		t.Fatalf("%d turns landed (err=%v)", n, err)
	}
}

// finishTurn clears presence on every chat the turn's run owns, and it finds
// that run through the turn itself.
func TestATurnReadsBackItsRunID(t *testing.T) {
	s := openTurnEntities(t)
	if _, err := s.OpenTurnContext("t1", "demo", "", "tg:1", "u1", "", nil); err != nil {
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
	if _, err := s.OpenTurnContext("t1", "demo", "", "tg:1", "u1", "", nil); err != nil {
		t.Fatal(err)
	}
	if tc, _ := s.TurnContextOf("t1"); tc.RunID != "" {
		t.Fatalf("rebound turn retained run id %q", tc.RunID)
	}
}

// A turn bound before the addressed attribute existed carries none, and reads
// false: every live turn context predates the field.
//
// Make turnFrom read the absence as true and the first read answers addressed.
func TestATurnWithNoAddressedAttributeReadsFalse(t *testing.T) {
	s := openTurnEntities(t)
	if _, err := s.OpenTurnContext("old", "demo", "", "web:demo", "u1", "", nil); err != nil {
		t.Fatal(err)
	}
	tc, ok := s.TurnContextOf("old")
	if !ok || tc.Addressed {
		t.Fatalf("a turn with no addressed attribute reads ok=%v addressed=%v, want false",
			ok, tc.Addressed)
	}
	it, _, err := s.itemAt(context.Background(), TurnContexts, objstore.Key{Partition: OwnerRoutd, Sort: "old"})
	if err != nil {
		t.Fatal(err)
	}
	if v, set := it.Attrs[attrTurnAddressed]; set {
		t.Fatalf("a turn nobody marked stores addressed=%q, want the attribute absent", v)
	}
}

func TestATurnReadsBackAddressed(t *testing.T) {
	s := openTurnEntities(t)
	if _, err := s.OpenTurnContext("t1", "demo", "", "web:demo", "u1", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTurnAddressed("t1", true); err != nil {
		t.Fatal(err)
	}
	if tc, _ := s.TurnContextOf("t1"); !tc.Addressed {
		t.Fatal("a turn marked addressed reads false")
	}

	if _, err := s.OpenTurnContext("t2", "demo", "", "web:demo", "u1", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTurnAddressed("t2", false); err != nil {
		t.Fatal(err)
	}
	if tc, _ := s.TurnContextOf("t2"); tc.Addressed {
		t.Fatal("a turn marked not addressed reads true")
	}
}

// A retry resolves the turn again and records the answer it got now: a true
// from the first attempt must not survive a second that resolves to false.
func TestARetryReplacesTheAddressedFactOfItsLastAttempt(t *testing.T) {
	s := openTurnEntities(t)
	if _, err := s.OpenTurnContext("t1", "demo", "", "web:demo", "u1", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTurnAddressed("t1", true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenTurnContext("t1", "demo", "", "web:demo", "u1", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTurnAddressed("t1", false); err != nil {
		t.Fatal(err)
	}
	if tc, _ := s.TurnContextOf("t1"); tc.Addressed {
		t.Fatal("a re-dispatch that resolved to not addressed still reads addressed")
	}
}

// Marking a turn nobody bound writes nothing: it must not conjure a context.
func TestMarkingAnUnboundTurnAddressedBindsNothing(t *testing.T) {
	s := openEntities(t)
	for _, addressed := range []bool{true, false} {
		if err := s.SetTurnAddressed("ghost", addressed); !errors.Is(err, ErrRefused) {
			t.Fatalf("marking an unbound turn addressed=%v returned %v, want ErrRefused", addressed, err)
		}
		if _, ok := s.TurnContextOf("ghost"); ok {
			t.Fatalf("marking an unbound turn addressed=%v created a context", addressed)
		}
	}
}

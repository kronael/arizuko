package routd

import (
	"context"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
)

// holdRule installs one hold rule on folder atlas with the given effect.
func holdRule(t *testing.T, d *DB, effect string) {
	t.Helper()
	if err := addACLRow(d, core.ACLRow{
		Principal: "folder:atlas", Action: auth.HoldPrefix + "mcp:delete",
		Scope: "atlas", Effect: effect,
	}); err != nil {
		t.Fatal(err)
	}
}

// awaitHeldID waits for the gate goroutine to record its row and returns its id.
func awaitHeldID(t *testing.T, d *DB) string {
	t.Helper()
	for range 500 {
		rows, err := d.ListPendingActions("atlas", "")
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) == 1 {
			return rows[0].ID
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("the gate never recorded a held row")
	return ""
}

// TestHoldGate_BlockingRunsTheApprovedCall is the default contract: the agent's
// call WAITS inside the turn, and an approval that lands while it waits releases
// it inline. `held=false` is the gate telling ipc to run the call, so the
// operator's yes takes effect in the turn that asked rather than the next one.
func TestHoldGate_BlockingRunsTheApprovedCall(t *testing.T) {
	HoldWait, hitlPoll = 10*time.Second, time.Millisecond
	t.Cleanup(func() { HoldWait, hitlPoll = 0, time.Millisecond })

	d := memDB(t)
	srv := &Server{db: d}
	holdRule(t, d, auth.EffectHoldBlock)
	gate := srv.holdGate(turnMCP{folder: "atlas", turnID: "t1"})

	type verdict struct {
		id   string
		held bool
	}
	out := make(chan verdict, 1)
	go func() {
		id, held := gate("mcp:delete", map[string]any{"target": "prod"})
		out <- verdict{id, held}
	}()

	id := awaitHeldID(t, d)
	if _, err := resolvePending(d, id, PendingApproved, "google:op", ""); err != nil {
		t.Fatal(err)
	}
	select {
	case v := <-out:
		if v.held {
			t.Fatalf("an approval during the wait must release the call inline, got held id %q", v.id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the gate did not notice the approval")
	}
	row, _ := pendingActionRow(d, id)
	if row.Status != PendingReleased {
		t.Fatalf("the waiter must consume the approval one-shot, status is %q", row.Status)
	}
}

// TestHoldGate_BlockingReturnsHeldOnReject — a rejection ends the wait too. The
// call must NOT run, and the agent gets the same "not run" result it gets for a
// call still waiting: the verdict itself reaches the human in chat.
func TestHoldGate_BlockingReturnsHeldOnReject(t *testing.T) {
	HoldWait, hitlPoll = 10*time.Second, time.Millisecond
	t.Cleanup(func() { HoldWait, hitlPoll = 0, time.Millisecond })

	d := memDB(t)
	srv := &Server{db: d}
	holdRule(t, d, auth.EffectHoldBlock)
	gate := srv.holdGate(turnMCP{folder: "atlas", turnID: "t1"})

	out := make(chan bool, 1)
	go func() {
		_, held := gate("mcp:delete", map[string]any{"target": "prod"})
		out <- held
	}()

	id := awaitHeldID(t, d)
	if _, err := resolvePending(d, id, PendingRejected, "google:op", "no"); err != nil {
		t.Fatal(err)
	}
	select {
	case held := <-out:
		if !held {
			t.Fatal("a rejected call must never run")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the gate did not notice the rejection")
	}
}

// TestHoldGate_AsyncDoesNotWait — the non-blocking variant. With a 10s wait
// configured, an `async` rule still returns at once, because the choice is the
// rule's and not the timeout's.
func TestHoldGate_AsyncDoesNotWait(t *testing.T) {
	HoldWait, hitlPoll = 10*time.Second, time.Millisecond
	t.Cleanup(func() { HoldWait, hitlPoll = 0, time.Millisecond })

	d := memDB(t)
	srv := &Server{db: d}
	holdRule(t, d, auth.EffectHoldAsync)
	gate := srv.holdGate(turnMCP{folder: "atlas", turnID: "t1"})

	start := time.Now()
	id, held := gate("mcp:delete", map[string]any{"target": "prod"})
	if !held || id == "" {
		t.Fatal("an async hold still suspends the call")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("an async hold must not wait, took %s", elapsed)
	}
}

// TestAwaitVerdict_TimeoutHandsBackToTheTrigger — when the waiter gives up, the
// row becomes async. Otherwise an approval typed after the timeout would enqueue
// no re-issue trigger and sit unused until some later call of the same shape
// silently consumed it.
func TestAwaitVerdict_TimeoutHandsBackToTheTrigger(t *testing.T) {
	d := memDB(t)
	srv := &Server{db: d}
	holdRule(t, d, auth.EffectHoldBlock)
	gate := srv.holdGate(turnMCP{folder: "atlas", turnID: "t1"})

	id, held := gate("mcp:delete", map[string]any{"target": "prod"})
	if !held || id == "" {
		t.Fatal("a hold rule suspends the call")
	}
	row, ok := pendingActionRow(d, id)
	if !ok || row.Mode != auth.EffectHoldAsync {
		t.Fatalf("a waiter that gave up must mark the row async, mode is %q", row.Mode)
	}
}

// TestResolveHold_BlockingWritesNoTrigger — exactly one of the waiter and the
// agent runs an approved call. While a row is still `block` the waiter owns it,
// so approving must NOT also enqueue the re-issue trigger that would make the
// agent run it a second time.
func TestResolveHold_BlockingWritesNoTrigger(t *testing.T) {
	d := memDB(t)
	for mode, wantTrigger := range map[string]bool{
		auth.EffectHoldBlock: false,
		auth.EffectHoldAsync: true,
	} {
		id := "id-" + mode
		if err := d.PutPendingAction(PendingAction{
			ID: id, GroupFolder: "atlas", CallerAgent: "folder:atlas",
			Tool: "mcp:delete", Args: `{"target":"prod"}`, ArgsFinal: `{"target":"prod"}`,
			ArgsHash: "h" + mode, ChatJID: "tg:1", Mode: mode,
		}); err != nil {
			t.Fatal(err)
		}
		tx, err := d.SQL().Begin()
		if err != nil {
			t.Fatal(err)
		}
		_, enqueue, err := resolveHoldTx(context.Background(), tx, id, PendingApproved, "google:op", "", false)
		if err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if got := enqueue != ""; got != wantTrigger {
			t.Errorf("mode %s: re-issue trigger written=%v, want %v", mode, got, wantTrigger)
		}
	}
}

// TestNotifyHeld_OffersTheVerdictsAsButtons — a button's data IS the command it
// replaces, which is what makes the press and the typed command one path with
// one operator gate. A button carrying a private token would be a second
// authorization path handed to every reader of the chat.
func TestNotifyHeld_OffersTheVerdictsAsButtons(t *testing.T) {
	d := memDB(t)
	dl := &recDeliverer{}
	srv := &Server{db: d, deliver: dl}
	holdRule(t, d, auth.EffectHoldBlock)
	gate := srv.holdGate(turnMCP{folder: "atlas", chatJID: "tg:1", turnID: "t9"})

	id, held := gate("mcp:delete", map[string]any{"target": "prod"})
	if !held || id == "" {
		t.Fatal("a matching hold rule must suspend the call")
	}
	if len(dl.sends) != 1 {
		t.Fatalf("held call must notify once, got %d sends", len(dl.sends))
	}
	var got []string
	for _, o := range dl.sends[0].options {
		got = append(got, o.Data)
	}
	want := []string{"/approve " + id, "/reject " + id, "/always " + id}
	if len(got) != len(want) {
		t.Fatalf("options = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("option %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestNotifyHeld_NoAlwaysButtonWithoutAnExactRule — `always` writes a rule, and
// the surface refuses to offer a rule it could not state. The text already says
// why; the button must not contradict it by being there.
func TestNotifyHeld_NoAlwaysButtonWithoutAnExactRule(t *testing.T) {
	opts := verdictOptions(PendingAction{ID: "ab12", MatcherError: "cannot state this call"})
	for _, o := range opts {
		if o.Data == "/always ab12" {
			t.Fatal("always must not be offered when no exact rule can be written")
		}
	}
	if len(opts) != 2 {
		t.Fatalf("approve and reject stay on offer, got %d options", len(opts))
	}
}

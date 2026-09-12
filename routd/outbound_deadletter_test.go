// Bounded outbound delivery: the retry sweep must stop. A permanent refusal
// dead-letters at once and spends no attempt; a transient failure spends one
// attempt and dead-letters at the cap. Before the bound, the sweep discarded
// the send error and re-sent every pending row every 30s for 24h — about 2880
// attempts on a message the platform had already rejected for good.

package routd

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
)

// attemptsOf reads a row's attempt count off its DELIVERY item: the pending
// feed while the row is still retriable, the dead-letter set once it is not. A
// row that owes no delivery has spent nothing.
func attemptsOf(t *testing.T, db *DB, id string) int {
	t.Helper()
	pending, err := db.PendingOutbound(time.Now().Add(48*time.Hour), 200)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range pending {
		if m.ID == id {
			return m.Attempts
		}
	}
	dead, err := db.DeadLetters("", 200)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range dead {
		if m.ID == id {
			return m.Attempts
		}
	}
	return 0
}

// sweepNow runs one due sweep at now.
func sweepNow(l *Loop, now time.Time) {
	l.nextRetryAt = now.Add(-time.Second)
	l.maybeRetryOutbound(now)
}

// TestOutboundPermanentFailureDeadLettersOnFirstAttempt: the platform refuses
// the message for a reason a retry cannot repair (a 4xx →
// chanlib.ErrPermanent). The sweep dead-letters the row on the FIRST attempt
// and spends no attempt budget, so the retry cap is free for the failures that
// a retry can repair.
func TestOutboundPermanentFailureDeadLettersOnFirstAttempt(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dl := &recDeliverer{permIdem: "refused"}
	loop := NewLoop(db, runnerFn(nil), LoopConfig{Deliver: dl, MaxOutboundRetry: 10})

	now := time.Now().UTC()
	_ = db.PutOutbound(core.Message{ID: "refused", ChatJID: "slack:T/C/U", Sender: "demo",
		Content: "the chat is deleted", Timestamp: now.Add(-2 * time.Minute), BotMsg: true},
		core.MessageStatusPending)

	sweepNow(loop, now)

	if len(dl.sends) != 1 {
		t.Fatalf("sweep attempted %d sends, want 1", len(dl.sends))
	}
	if st := statusOf(t, db, "refused"); st != core.MessageStatusFailed {
		t.Fatalf("status=%q want failed — a permanent refusal must dead-letter at once", st)
	}
	if n := attemptsOf(t, db, "refused"); n != 0 {
		t.Fatalf("delivery_attempts=%d want 0 — a permanent refusal must not spend the retry budget", n)
	}

	// A dead-lettered row leaves the retry feed for good.
	sweepNow(loop, now.Add(time.Minute))
	if len(dl.sends) != 1 {
		t.Fatalf("sweep attempted %d sends after the dead letter, want 1 — a dead letter must never retry", len(dl.sends))
	}
}

// TestOutboundTransientFailureDeadLettersAtCap: an adapter that keeps failing
// with a transient error gets exactly MaxOutboundRetry attempts, then the row
// dead-letters. It never retries forever.
func TestOutboundTransientFailureDeadLettersAtCap(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const maxAttempts = 3
	dl := &recDeliverer{failSend: true}
	loop := NewLoop(db, runnerFn(nil), LoopConfig{Deliver: dl, MaxOutboundRetry: maxAttempts})

	now := time.Now().UTC()
	_ = db.PutOutbound(core.Message{ID: "flaky", ChatJID: "slack:T/C/U", Sender: "demo",
		Content: "adapter is down", Timestamp: now.Add(-2 * time.Minute), BotMsg: true},
		core.MessageStatusPending)

	for i := 1; i <= maxAttempts; i++ {
		sweepNow(loop, now.Add(time.Duration(i)*time.Minute))
		if n := attemptsOf(t, db, "flaky"); n != i {
			t.Fatalf("after sweep %d: delivery_attempts=%d want %d", i, n, i)
		}
		want := core.MessageStatusPending
		if i == maxAttempts {
			want = core.MessageStatusFailed
		}
		if st := statusOf(t, db, "flaky"); st != want {
			t.Fatalf("after sweep %d of %d: status=%q want %q", i, maxAttempts, st, want)
		}
	}

	// Past the cap the sweep must not touch the row again.
	sweepNow(loop, now.Add(time.Hour))
	if len(dl.sends) != maxAttempts {
		t.Fatalf("sweep attempted %d sends, want %d — the cap must stop the retries", len(dl.sends), maxAttempts)
	}
	if n := attemptsOf(t, db, "flaky"); n != maxAttempts {
		t.Fatalf("delivery_attempts=%d want %d after the dead letter", n, maxAttempts)
	}
}

// TestOutboundRetrySucceedsBeforeCap: a transient failure that recovers inside
// the budget delivers, and the row goes 'sent'. The counter must not turn a
// recoverable outage into a lost message.
func TestOutboundRetrySucceedsBeforeCap(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dl := &recDeliverer{failSend: true}
	loop := NewLoop(db, runnerFn(nil), LoopConfig{Deliver: dl, MaxOutboundRetry: 5})

	now := time.Now().UTC()
	_ = db.PutOutbound(core.Message{ID: "recovers", ChatJID: "slack:T/C/U", Sender: "demo",
		Content: "hold on", Timestamp: now.Add(-2 * time.Minute), BotMsg: true},
		core.MessageStatusPending)

	sweepNow(loop, now)
	sweepNow(loop, now.Add(time.Minute))
	if st := statusOf(t, db, "recovers"); st != core.MessageStatusPending {
		t.Fatalf("status=%q want pending inside the budget", st)
	}
	// The attempts live on the DELIVERY item, and a delivered row has none — so
	// the count is checked here, while the row still owes one.
	if n := attemptsOf(t, db, "recovers"); n != 2 {
		t.Fatalf("attempts=%d want 2 — the two failures before the recovery", n)
	}

	dl.failSend = false
	sweepNow(loop, now.Add(2*time.Minute))
	if st := statusOf(t, db, "recovers"); st != core.MessageStatusSent {
		t.Fatalf("status=%q want sent after the adapter recovered", st)
	}
}

// TestExistingPendingRowKeepsFullBudget: a row that has never been retried
// carries NO attempt count — absent is the only null this model has — so a row
// three live instances already hold as 'pending' gets its whole budget and does
// not dead-letter on the first sweep.
func TestExistingPendingRowKeepsFullBudget(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := db.PutOutbound(core.Message{
		ID: "legacy", ChatJID: "slack:T/C/U", Sender: "demo",
		Content: "queued before the upgrade", Timestamp: time.Now().UTC().Add(-48 * time.Hour),
		FromMe: true, BotMsg: true,
	}, core.MessageStatusPending); err != nil {
		t.Fatal(err)
	}
	if n := attemptsOf(t, db, "legacy"); n != 0 {
		t.Fatalf("a row that has never been retried reports %d attempts, want 0", n)
	}

	dl := &recDeliverer{failSend: true}
	loop := NewLoop(db, runnerFn(nil), LoopConfig{Deliver: dl, MaxOutboundRetry: 10})
	sweepNow(loop, time.Now().UTC())

	if st := statusOf(t, db, "legacy"); st != core.MessageStatusPending {
		t.Fatalf("status=%q want pending — a live row must not dead-letter on the first sweep", st)
	}
	if n := attemptsOf(t, db, "legacy"); n != 1 {
		t.Fatalf("delivery_attempts=%d want 1", n)
	}
}

// TestMCPSendFailureLeavesPendingRowForSweep is the L21 half, end to end over
// the REAL per-turn MCP socket: the platform refuses a `send`, the agent gets
// the error, and the message does NOT disappear. Before the fix internalSend
// returned on the error and recordOutbound never ran, so PendingOutbound never
// saw the message and nothing retried it.
func TestMCPSendFailureLeavesPendingRowForSweep(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.PutGroup(core.Group{Folder: "demo"}); err != nil {
		t.Fatal(err)
	}
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "demo"}})
	const jid = "slack:team/channel/c1"
	if _, err := db.PutTurnContext("t1", "demo", "", jid, "u1", ""); err != nil {
		t.Fatal(err)
	}

	deliver := &recDeliverer{failSend: true}
	srv := NewServer(db, nil, deliver, nil, 0, "")
	ipcDir := filepath.Join(t.TempDir(), "ipc", "demo")
	stop, err := srv.ServeTurnMCP(
		turnMCP{folder: "demo", topic: "", chatJID: jid, turnID: "t1", trigger: "u1"}, ipcDir)
	if err != nil {
		t.Fatalf("ServeTurnMCP: %v", err)
	}
	defer stop()

	// The agent must still see the failure — fail-loud stays.
	_, errText := callToolOverSock(t, groupfolder.IpcSocket(ipcDir), "send",
		map[string]any{"chatJid": jid, "text": "do not lose me"})
	if errText == "" {
		t.Fatal("send tool reported success on a failed delivery — the agent must get the error")
	}

	// ... and the row must survive as the sweep's work item.
	rows, err := db.PendingOutbound(time.Now().UTC().Add(time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Content != "do not lose me" {
		t.Fatalf("PendingOutbound=%+v want the one undelivered send — the message disappeared", rows)
	}

	// The sweep owns it from here: it delivers once the adapter recovers.
	deliver.failSend = false
	loop := NewLoop(db, runnerFn(nil), LoopConfig{Deliver: deliver})
	sweepNow(loop, time.Now().UTC().Add(time.Minute))

	if st := statusOf(t, db, rows[0].ID); st != core.MessageStatusSent {
		t.Fatalf("row status=%q want sent — the sweep did not pick up the MCP failure row", st)
	}
	if n := countBots(t, db, jid); n != 1 {
		t.Fatalf("bot rows=%d want 1 — the failure row must be the only one, not a duplicate", n)
	}
}

// L22: the retry sweep used a flat 30s-forever cadence, so a row with
// MAX_OUTBOUND_RETRY=10 dead-lettered ~5 minutes after its first failure —
// too short for the outages that actually happen (a whapd reconnect, an
// adapter container restart). backoffDelay grows the gap with the row's own
// delivery_attempts instead of retrying every row on the same fixed clock.

package routd

import (
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
)

func TestBackoffDelayGrowsAndCaps(t *testing.T) {
	cases := []struct {
		attempts int
		want     time.Duration
	}{
		{0, 30 * time.Second},
		{1, 60 * time.Second},
		{2, 120 * time.Second},
		{3, 240 * time.Second},
		{7, time.Hour},  // uncapped would be 3840s; capped at maxOutboundBackoff
		{20, time.Hour}, // stays capped, never overflows
	}
	for _, c := range cases {
		if got := backoffDelay(c.attempts); got != c.want {
			t.Errorf("backoffDelay(%d) = %v, want %v", c.attempts, got, c.want)
		}
	}
}

// TestOutboundRetryHonorsPerRowBackoff is the sweep-level assertion: a row
// that already failed once must wait backoffDelay(1)=60s, not the flat 30s
// every other pending row gets on its first attempt.
func TestOutboundRetryHonorsPerRowBackoff(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dl := &recDeliverer{pid: "pid-x"}
	loop := NewLoop(db, runnerFn(nil), LoopConfig{Deliver: dl})

	now := time.Now().UTC()
	owed := core.Message{ID: "once-failed", ChatJID: "slack:T/C/U", Sender: "demo",
		Content: "retry me", Timestamp: now.Add(-45 * time.Second), BotMsg: true}
	if err := db.PutOutbound(owed, core.MessageStatusPending); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.RecordDeliveryFailure(owed, 10); err != nil {
		t.Fatal(err)
	}

	// 45s elapsed, backoffDelay(1)=60s: not due yet.
	loop.nextRetryAt = now.Add(-time.Second)
	loop.maybeRetryOutbound(now)
	if len(dl.sends) != 0 {
		t.Fatalf("sends=%+v want none — 45s < backoffDelay(1)=60s", dl.sends)
	}
	if st := statusOf(t, db, "once-failed"); st != core.MessageStatusPending {
		t.Fatalf("status=%q want still pending", st)
	}

	// 65s elapsed: now past backoffDelay(1)=60s, so the sweep retries it.
	later := now.Add(20 * time.Second)
	loop.nextRetryAt = later.Add(-time.Second)
	loop.maybeRetryOutbound(later)
	if len(dl.sends) != 1 || dl.sends[0].idem != "once-failed" {
		t.Fatalf("sends=%+v want one re-dispatch of once-failed", dl.sends)
	}
	if st := statusOf(t, db, "once-failed"); st != core.MessageStatusSent {
		t.Fatalf("status=%q want sent (re-delivered once due)", st)
	}
}

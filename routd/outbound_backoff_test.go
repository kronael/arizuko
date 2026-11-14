package routd

import (
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/retry"
)

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
	if _, _, err := db.RecordDeliveryFailure(owed, retry.Failure{Kind: "transient"}); err != nil {
		t.Fatal(err)
	}

	// The deadline is still in the future.
	loop.nextRetryAt = now.Add(-time.Second)
	loop.maybeRetryOutbound(now)
	if len(dl.sends) != 0 {
		t.Fatalf("sends=%+v want none — 45s < backoffDelay(1)=60s", dl.sends)
	}
	if st := statusOf(t, db, "once-failed"); st != core.MessageStatusPending {
		t.Fatalf("status=%q want still pending", st)
	}

	// The sweep retries the row after its deadline.
	later := now.Add(31 * time.Second)
	loop.nextRetryAt = later.Add(-time.Second)
	loop.maybeRetryOutbound(later)
	if len(dl.sends) != 1 || dl.sends[0].idem != "once-failed" {
		t.Fatalf("sends=%+v want one re-dispatch of once-failed", dl.sends)
	}
	if st := statusOf(t, db, "once-failed"); st != core.MessageStatusSent {
		t.Fatalf("status=%q want sent (re-delivered once due)", st)
	}
}

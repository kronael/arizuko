package routd

import (
	"context"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	"github.com/kronael/arizuko/store"
)

func providerRefusal(t *testing.T, db *DB, p *pauseProvider) TurnContext {
	t.Helper()
	pauseInput(t, db, "web:a", "refused", "u", "saved", time.Now())
	msg, _ := db.MessageByID("web:a", "refused")
	keys, _ := turnMessageKeys([]core.Message{msg})
	ok, err := db.store().OpenTurnContext("refused", "a", "", "web:a", "u", "",
		&store.TurnAdmission{AttemptID: "attempt", InputLast: store.MessageCursor(msg), InputKeys: keys})
	if err != nil || !ok {
		t.Fatalf("open: %v %v", ok, err)
	}
	if err := db.SetTurnRunID("refused", "captured"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.srv.recordTurnResultAttempt("refused", "attempt", apiv1.TurnResult{
		Status: "error", ProviderFailure: &apiv1.ProviderFailure{Reason: "outage"},
	}); err != nil {
		t.Fatal(err)
	}
	tc, _ := db.GetTurnContext("refused")
	return tc
}

func TestTurnControlPollContinuesAfterProviderRecoveryError(t *testing.T) {
	db, _, p, dl := pauseFixture(t)
	tc := providerRefusal(t, db, p)
	if err := db.SetRunReturned(tc.TurnID); err != nil {
		t.Fatal(err)
	}
	p.stopErr = context.DeadlineExceeded
	pauseInput(t, db, "web:b", "command", "u", "/ping", time.Now())
	poller := NewLoop(db, p, LoopConfig{Deliver: dl})
	var polled atomic.Int32
	poller.q.SetProcessMessagesFn(func(jid string) (bool, error) {
		if jid == "web:b" {
			polled.Add(1)
		}
		return false, nil
	})
	poller.pollOnce()
	for poller.q.ActiveCount() != 0 {
		runtime.Gosched()
	}
	poller.StopQueue()
	if polled.Load() == 0 {
		t.Fatal("recovery error suppressed independent command polling")
	}
	after, _ := db.GetTurnContext(tc.TurnID)
	control, _, _ := db.store().ReadTurnControl(t.Context())
	if after.State != store.TurnRunning || control.Phase != "probe" || len(p.kills) != 1 {
		t.Fatalf("failed recovery released ownership: turn=%+v control=%+v kills=%v", after, control, p.kills)
	}
}

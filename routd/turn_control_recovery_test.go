package routd

import (
	"context"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
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
	poller.maybeRecoverTurns(time.Now())
	poller.sweepChats(t.Context())
	for queueHasWorker(poller.q) {
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

func TestTurnControlRefusedInputSurvivesGCBeforeRuntimeReturn(t *testing.T) {
	db, l, p, _ := pauseFixture(t)
	tc := providerRefusal(t, db, p)
	if err := db.store().WriteEntities(t.Context(), nil, objstore.Write{
		Op: objstore.Update, Entity: store.TurnContexts,
		Key:   objstore.Key{Partition: store.OwnerRoutd, Sort: tc.TurnID},
		Attrs: map[string]string{"started_at": core.TS(time.Now().Add(-24 * time.Hour))},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.store().SweepExpiredRunning(time.Second); err != nil {
		t.Fatal(err)
	}
	if err := l.q.recover(l, true); err != nil {
		t.Fatal(err)
	}
	after, _ := db.GetTurnContext(tc.TurnID)
	control, _, _ := db.store().ReadTurnControl(t.Context())
	l.q.now = func() time.Time { return control.NextProbeAt }
	processPauseChat(t, l, tc.ChatJID)
	if after.State == store.TurnExpired || len(p.kills) == 0 || len(p.runs) != 0 {
		t.Fatalf("refused input expired and replayed without reconciliation: state=%s kills=%v runs=%d",
			after.State, p.kills, len(p.runs))
	}
	if after.State != store.TurnPendingRetry || after.RetryCount != 1 || p.kills[0] != "captured" {
		t.Fatalf("refusal recovery lost ownership: turn=%+v kills=%v", after, p.kills)
	}
}

// A legacy pending_retry turn whose input left the pending batch can never
// be bound. It closes as failed, the next legacy turn on the chat still
// migrates, and a second pass neither sees nor logs the closed turn again.
func TestTurnControlUnboundLegacyTurnClosesAndMigrationContinues(t *testing.T) {
	db, l, _, _ := pauseFixture(t)
	jid := "telegram:group/legacy"
	doSetRoutes(t, db, []core.Route{{Match: "chat_jid=" + jid, Target: "a"}})
	pauseInput(t, db, jid, "2-kept", "u", "saved", time.Now())
	for _, id := range []string{"1-gone", "2-kept"} {
		if _, err := db.PutTurnContext(id, "a", "", jid, "u", ""); err != nil {
			t.Fatal(err)
		}
		if err := db.store().WriteEntities(t.Context(), nil, objstore.Write{Op: objstore.Update, Entity: store.TurnContexts,
			Key: objstore.Key{Partition: store.OwnerRoutd, Sort: id}, Attrs: map[string]string{"state": store.TurnPendingRetry}}); err != nil {
			t.Fatal(err)
		}
	}
	logs := captureWarnings(t)
	if err := l.migrateRetries(jid, false); err != nil {
		t.Fatalf("one unbound legacy turn blocked migration: %v", err)
	}
	gone, _ := db.GetTurnContext("1-gone")
	kept, _ := db.GetTurnContext("2-kept")
	if gone.State != store.TurnDone {
		t.Fatalf("unbound legacy turn stays open: %+v", gone)
	}
	if kept.State != store.TurnPendingRetry || len(kept.InputKeys) != 1 || kept.NextAttemptAt.IsZero() {
		t.Fatalf("bindable legacy turn did not migrate: %+v", kept)
	}
	first := logs.String()
	if strings.Count(first, "1-gone") != 1 || !strings.Contains(first, "input boundary is missing") {
		t.Fatalf("want one WARN naming the turn and reason; log:\n%s", first)
	}
	if err := l.migrateRetries(jid, false); err != nil {
		t.Fatal(err)
	}
	if again := logs.String(); again != first {
		t.Fatalf("second pass logged again:\n%s", strings.TrimPrefix(again, first))
	}
}

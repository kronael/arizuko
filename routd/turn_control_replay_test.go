package routd

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/store"
)

func deferredFixture(t *testing.T) (*DB, *Loop, *recRunner, TurnContext, time.Time) {
	t.Helper()
	db, loop, runner := recLoop(t)
	if err := db.PutGroup(core.Group{Folder: "demo"}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	loop.q.now = func() time.Time { return now }
	input := core.Message{ID: "saved", ChatJID: "web:demo", Sender: "user",
		Content: "saved question", Timestamp: now}
	ambient := core.Message{ID: "ambient", ChatJID: input.ChatJID, Sender: "other",
		Content: "saved context", Timestamp: now.Add(-time.Second)}
	for _, msg := range []core.Message{ambient, input} {
		if err := db.PutMessage(msg); err != nil {
			t.Fatal(err)
		}
	}
	keys, _ := turnMessageKeys([]core.Message{input})
	contextKeys, _ := turnMessageKeys([]core.Message{ambient})
	tc := TurnContext{TurnID: input.ID, ChatJID: input.ChatJID, Folder: "demo",
		Topic: "bound-topic", Trigger: "bound-sender", ReturnTo: "web:return",
		InputLast: store.MessageCursor(input), InputKeys: keys, AmbientKeys: contextKeys,
		NextAttemptAt: now.Add(time.Minute)}
	if ok, err := db.store().SavePendingTurn(context.Background(), tc, ""); err != nil || !ok {
		t.Fatalf("save: %v %v", ok, err)
	}
	tc, _ = db.GetTurnContext(tc.TurnID)
	return db, loop, runner, tc, now
}

func TestTurnControlSavedReplay(t *testing.T) {
	db, loop, runner, tc, now := deferredFixture(t)
	if _, err := loop.replay(tc.TurnID); err != nil || len(runner.runs) != 0 {
		t.Fatalf("early replay: runs=%d err=%v", len(runner.runs), err)
	}
	doSetRoutes(t, db, []core.Route{{Match: "platform=web", Target: "other"}})
	loop.pendingElevation.Store(tc.TurnID, struct{}{})
	now = tc.NextAttemptAt
	loop.q.now = func() time.Time { return now }
	done := make(chan error, 1)
	loop.q.shuttingDown = false
	loop.q.SetProcessMessagesFn(func(key string) (bool, error) {
		had, err := loop.processGroupMessages(key)
		done <- err
		return had, err
	})
	loop.q.tick(loop)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(runner.runs) != 1 {
		t.Fatalf("saved replay launches=%d", len(runner.runs))
	}
	run := runner.runs[0]
	if run.folder != tc.Folder || run.topic != tc.Topic || run.trigger != tc.Trigger || run.elevated ||
		!strings.Contains(run.batch, "saved question") || !strings.Contains(run.batch, "saved context") {
		t.Fatalf("saved binding changed: %+v", run)
	}
	if _, err := loop.replay(tc.TurnID); err != nil || len(runner.runs) != 1 {
		t.Fatalf("duplicate replay: runs=%d err=%v", len(runner.runs), err)
	}
	if cursor := db.GetAgentCursor(tc.ChatJID); cursor != tc.InputLast {
		t.Fatalf("replay changed cursor: %s", cursor)
	}
	t.Run("missing keys", func(t *testing.T) {
		db, loop, runner, tc, _ := deferredFixture(t)
		loop.q.now = func() time.Time { return tc.NextAttemptAt }
		if _, err := db.store().TryWriteEntities(context.Background(), nil,
			objstore.Write{Op: objstore.Delete, Entity: store.Messages, Key: tc.InputKeys[0]}); err != nil {
			t.Fatal(err)
		}
		if _, err := loop.replay(tc.TurnID); err == nil || !strings.Contains(err.Error(), "missing") || len(runner.runs) != 0 {
			t.Fatalf("missing input: launches=%d err=%v", len(runner.runs), err)
		}
	})
}

type recoveryRunner struct {
	recRunner
	stopErr error
	killed  string
}

func (r *recoveryRunner) Kill(_ context.Context, runID string) error {
	r.killed = runID
	return r.stopErr
}

func TestTurnControlExecutionRestart(t *testing.T) {
	db, loop, _, tc, _ := deferredFixture(t)
	restarted := NewLoop(db, &recRunner{}, LoopConfig{})
	restarted.StopQueue()
	now := tc.NextAttemptAt
	restarted.q.now = func() time.Time { return now }
	if err := restarted.q.recover(restarted); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.store().ClaimDeferredTurn(context.Background(), tc, "interrupted", now); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if err := db.SetTurnRunID(tc.TurnID, "captured"); err != nil {
		t.Fatal(err)
	}
	runner := &recoveryRunner{stopErr: errors.New("cannot confirm stop")}
	restarted.runner = runner
	if err := restarted.q.recover(restarted); err == nil || runner.killed != "captured" {
		t.Fatalf("uncertain execution: killed=%q err=%v", runner.killed, err)
	}
	if _, err := restarted.replay(tc.TurnID); err != nil || len(runner.runs) != 0 {
		t.Fatalf("uncertain execution replayed: %v", err)
	}
	runner.stopErr = nil
	if err := restarted.q.recover(restarted); err != nil {
		t.Fatal(err)
	}
	bound, _ := db.GetTurnContext(tc.TurnID)
	if bound.State != store.TurnPendingRetry || bound.RetryCount != 1 || !bound.NextAttemptAt.Equal(now.Add(30*time.Second)) {
		t.Fatalf("recovery binding: %+v", bound)
	}
	loop.q.now = func() time.Time { return bound.NextAttemptAt }
	loop.runner = runner
	if _, err := loop.replay(tc.TurnID); err != nil || len(runner.runs) != 1 {
		t.Fatalf("reconciled replay: launches=%d err=%v", len(runner.runs), err)
	}
}

var _ Runner = (*recoveryRunner)(nil)
var _ RunKiller = (*recoveryRunner)(nil)

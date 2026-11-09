package routd

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
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
		if err := db.store().WriteEntities(context.Background(), nil,
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
	run     Runner
	stopErr error
	killed  string
}

func (r *recoveryRunner) Run(ctx context.Context, req runedv1.RunRequest) (runedv1.RunOutcome, error) {
	if r.run != nil {
		return r.run.Run(ctx, req)
	}
	return r.recRunner.Run(ctx, req)
}

func (r *recoveryRunner) StopFolder(_ context.Context, folder string) (runedv1.StopRunResponse, error) {
	r.killed = folder
	return runedv1.StopRunResponse{}, r.stopErr
}

func (r *recoveryRunner) Kill(_ context.Context, runID string) error {
	r.killed = runID
	return r.stopErr
}

func TestTurnControlExecutionRestart(t *testing.T) {
	t.Run("probe", testTurnControlProbeRestart)
	t.Run("early submission", testTurnControlProbeRestart)
	t.Run("missing run id", testTurnControlProbeRestart)
	t.Run("hourly successor", testTurnControlProbeRestart)
	db, loop, _, tc, _ := deferredFixture(t)
	restarted := NewLoop(db, &recRunner{}, LoopConfig{})
	restarted.StopQueue()
	now := tc.NextAttemptAt
	restarted.q.now = func() time.Time { return now }
	if err := restarted.q.recover(restarted, true); err != nil {
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
	if err := restarted.q.recover(restarted, true); err == nil || runner.killed != "captured" {
		t.Fatalf("uncertain execution: killed=%q err=%v", runner.killed, err)
	}
	if _, err := restarted.replay(tc.TurnID); err != nil || len(runner.runs) != 0 {
		t.Fatalf("uncertain execution replayed: %v", err)
	}
	runner.stopErr = nil
	if err := restarted.q.recover(restarted, true); err != nil {
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

func testTurnControlProbeRestart(t *testing.T) {
	db, _, _, tc, _ := deferredFixture(t)
	now := tc.NextAttemptAt
	ctx := context.Background()
	before := store.TurnControlState{Phase: "open", Episode: "outage", NextProbeAt: now, ProbeTurnID: tc.TurnID}
	before.Successor = strings.HasSuffix(t.Name(), "hourly_successor")
	if ok, err := db.store().WriteTurnControl(ctx, nil, before); err != nil || !ok {
		t.Fatalf("open control: %v %v", ok, err)
	}
	probe := before
	probe.Phase, probe.ProbeAttemptID = "probe", "interrupted"
	if ok, err := db.store().ClaimControlledTurn(ctx, tc, probe.ProbeAttemptID, now, &before, &probe); err != nil || !ok {
		t.Fatalf("claim probe: %v %v", ok, err)
	}
	early := strings.HasSuffix(t.Name(), "early_submission")
	runID := "captured"
	if strings.HasSuffix(t.Name(), "missing_run_id") {
		runID = tc.Folder
	} else if err := db.SetTurnRunID(tc.TurnID, runID); err != nil {
		t.Fatal(err)
	}
	if early {
		if _, err := db.store().RecordTurnResult(tc.Folder, tc.TurnID, "", "success", ""); err != nil {
			t.Fatal(err)
		}
		finishTurn(db, nil, tc.TurnID, endDone)
	}
	runner := &recoveryRunner{stopErr: errors.New("stop unconfirmed")}
	loop := NewLoop(db, runner, LoopConfig{})
	loop.StopQueue()
	loop.q.now = func() time.Time { return now }
	if err := loop.q.recover(loop, true); err == nil || runner.killed != runID {
		t.Fatalf("probe reconciliation: killed=%q err=%v", runner.killed, err)
	}
	fresh := core.Message{ID: "fresh", ChatJID: tc.ChatJID, Sender: "user", Timestamp: now, Content: "next"}
	if err := db.PutMessage(fresh); err != nil {
		t.Fatal(err)
	}
	if _, _, busy, _, err := loop.runTurn(tc.Folder, "", tc.ChatJID, fresh.ID, false, []core.Message{fresh}, nil); err != nil || !busy || len(runner.runs) != 0 {
		t.Fatalf("unresolved probe admitted replacement: busy=%v launches=%d err=%v", busy, len(runner.runs), err)
	}
	runner.stopErr = nil
	if err := loop.q.recover(loop, true); err != nil {
		t.Fatal(err)
	}
	current, _, _ := db.store().ReadTurnControl(ctx)
	bound, _ := db.GetTurnContext(tc.TurnID)
	if current.Phase != "open" || early && (bound.State != store.TurnDone || bound.RetryCount != 0 || !current.NextProbeAt.Equal(before.NextProbeAt)) {
		t.Fatalf("reconciled early submission: turn=%+v control=%+v", bound, current)
	}
	if !early {
		if bound.State != store.TurnPendingRetry || bound.RetryCount != 1 || !current.NextProbeAt.Equal(bound.NextAttemptAt) {
			t.Fatalf("reconciled probe: turn=%+v control=%+v", bound, current)
		}
		if before.Successor && bound.NextAttemptAt.Before(now.Add(time.Hour)) {
			t.Fatalf("restart shortened successor cooldown: %+v", bound)
		}
		now = bound.NextAttemptAt
		if _, err := loop.replay(tc.TurnID); err != nil || len(runner.runs) != 1 {
			t.Fatalf("reconciled replay: launches=%d err=%v", len(runner.runs), err)
		}
	}
}

func TestTurnControlExecutionTransport(t *testing.T) {
	db, loop, _ := recLoop(t)
	if err := db.PutGroup(core.Group{Folder: "demo"}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	loop.q.now = func() time.Time { return now }
	launches := 0
	runner := &recoveryRunner{stopErr: errors.New("stop unconfirmed"), run: runnerFn(func(context.Context, runedv1.RunRequest) (runedv1.RunOutcome, error) {
		launches++
		return runedv1.RunOutcome{RunID: "uncertain"}, errors.New("lost response")
	})}
	loop.runner = runner
	if err := db.PutMessage(core.Message{ID: "uncertain", ChatJID: "web:demo", Sender: "user", Timestamp: now, Content: "question"}); err != nil {
		t.Fatal(err)
	}
	if _, err := loop.processGroupMessages("web:demo"); err == nil || runner.killed != "uncertain" {
		t.Fatalf("uncertain transport: killed=%q err=%v", runner.killed, err)
	}
	if _, err := loop.processGroupMessages("web:demo"); err != nil || launches != 1 {
		t.Fatalf("uncertain execution replayed: launches=%d err=%v", launches, err)
	}
	runner.stopErr = nil
	if err := loop.q.recover(loop, false); err != nil {
		t.Fatal(err)
	}
	tc, _ := db.GetTurnContext("uncertain")
	if tc.State != store.TurnPendingRetry || tc.RetryCount != 1 {
		t.Fatalf("transport recovery: %+v", tc)
	}
}

var _ Runner = (*recoveryRunner)(nil)
var _ RunKiller = (*recoveryRunner)(nil)

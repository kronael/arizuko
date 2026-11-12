package routd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/store"
)

type refusePending struct{ objstore.Store }

func (s refusePending) GuardedBatch(ctx context.Context, checks []objstore.Check, writes []objstore.Write) (bool, error) {
	for _, w := range writes {
		if w.Entity == store.TurnContexts && w.Attrs["state"] == store.TurnPendingRetry {
			return false, nil
		}
	}
	return s.Store.GuardedBatch(ctx, checks, writes)
}

func TestTurnControlCommandsPreserveQueuedInput(t *testing.T) {
	db, l, _, tc, _ := deferredFixture(t)
	now := tc.NextAttemptAt
	l.q.now = func() time.Time { return now }
	launches := 0
	l.runner = runnerFn(func(context.Context, runedv1.RunRequest) (runedv1.RunOutcome, error) {
		launches++
		return runedv1.RunOutcome{Outcome: runedv1.OutcomeError}, nil
	})
	if _, err := l.replay(tc.TurnID); err != nil {
		t.Fatal(err)
	}
	control, _, _ := db.store().ReadTurnControl(t.Context())
	for i, text := range []string{"fresh one", "fresh two", "/ping"} {
		if err := db.PutMessage(core.Message{ID: fmt.Sprint(i), ChatJID: tc.ChatJID,
			Sender: fmt.Sprint(i), Topic: fmt.Sprint(i), Content: text,
			Timestamp: now.Add(time.Duration(i+1) * time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	dl := &recDeliverer{}
	l.deliver = dl
	base := db.Obj()
	db.obj = refusePending{base}
	if _, err := l.processGroupMessages(tc.ChatJID); err == nil {
		t.Fatal("command consumed after pending save failed")
	}
	if len(dl.sends) != 0 || db.GetAgentCursor(tc.ChatJID) != tc.InputLast {
		t.Fatal("command response or cursor advance preceded pending save")
	}
	db.obj = base
	if _, err := l.processGroupMessages(tc.ChatJID); err != nil {
		t.Fatal(err)
	}
	if len(dl.sends) != 1 || !strings.HasPrefix(dl.sends[0].text, "pong") || l.q.ActiveCount() != 0 {
		t.Fatalf("command response or slot: sends=%v active=%d", dl.sends, l.q.ActiveCount())
	}
	cursor := db.GetAgentCursor(tc.ChatJID)
	for _, id := range []string{"0", "1"} {
		pending, ok := db.GetTurnContext(id)
		if !ok || pending.State != store.TurnPendingRetry || pending.RetryCount != 0 || len(pending.InputKeys) != 1 {
			t.Fatalf("fresh work not saved at count zero: %+v", pending)
		}
		if _, err := l.replay(id); err != nil && !errors.Is(err, errModelInputOrder) {
			t.Fatal(err)
		}
	}
	after, _, _ := db.store().ReadTurnControl(t.Context())
	if after != control || launches != 1 {
		t.Fatalf("command or fresh input changed cooldown: %+v launches=%d", after, launches)
	}
	now = control.NextProbeAt
	if _, err := l.replay(tc.TurnID); err != nil {
		t.Fatal(err)
	}
	commands := 0
	for _, send := range dl.sends {
		if strings.HasPrefix(send.text, "pong") {
			commands++
		}
	}
	if launches != 2 || db.GetAgentCursor(tc.ChatJID) != cursor || commands != 1 {
		t.Fatal("failed probe rewound cursor, launched fresh work or repeated command")
	}
}

func TestTurnControlCommandsWhileActive(t *testing.T) {
	db, l, _ := recLoop(t)
	if err := db.PutGroup(core.Group{Folder: "demo"}); err != nil {
		t.Fatal(err)
	}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	runner := &recoveryRunner{run: runnerFn(func(_ context.Context, req runedv1.RunRequest) (runedv1.RunOutcome, error) {
		if _, err := db.store().RecordTurnResult("demo", req.TurnID, "", "success", ""); err != nil {
			t.Error(err)
		}
		finishTurn(db, nil, req.TurnID, endDone)
		close(entered)
		<-release
		return runedv1.RunOutcome{Outcome: runedv1.OutcomeError}, nil
	})}
	l.runner = runner
	l.q.shuttingDown, l.q.maxConcurrent = false, 1
	l.q.SetProcessMessagesFn(func(jid string) (bool, error) {
		had, err := l.processGroupMessages(jid)
		close(done)
		return had, err
	})
	dl := &recDeliverer{}
	l.deliver = dl
	base := time.Now().Add(-time.Minute)
	put := func(id, text string, offset time.Duration) {
		t.Helper()
		if err := db.PutMessage(core.Message{ID: id, ChatJID: "web:demo", Sender: "u",
			Content: text, Timestamp: base.Add(offset)}); err != nil {
			t.Fatal(err)
		}
	}
	put("active", "question", 0)
	l.Enqueue("web:demo")
	<-entered
	put("fresh", "next question", time.Second)
	put("ping", "/ping", 2*time.Second)
	l.Enqueue("web:demo")
	fresh, ok := db.GetTurnContext("fresh")
	if !ok || fresh.State != store.TurnPendingRetry || fresh.RetryCount != 0 ||
		len(dl.sends) != 1 || l.q.ActiveCount() != 1 {
		t.Errorf("command waited for active execution: fresh=%+v sends=%v", fresh, dl.sends)
	}
	put("topic", "#next", 3*time.Second)
	l.Enqueue("web:demo")
	if _, topic := db.StickyState("web:demo"); topic != "next" || len(dl.sends) != 2 {
		t.Error("navigation command waited for active execution")
	}
	l.cmdStop("web:demo", "demo")
	closed, _ := db.GetTurnContext("active")
	if !closed.RunReturned || runner.killed != "demo" || l.q.ActiveCount() != 1 {
		t.Error("stop did not close the submitted attempt before kill")
	}
	cursor := db.GetAgentCursor("web:demo")
	// The rows enqueued during the run would take a second pass; this body
	// asserts the first pass's completion alone.
	l.StopQueue()
	close(release)
	<-done
	if db.GetAgentCursor("web:demo") != cursor {
		t.Fatal("active completion rewound command cursor")
	}
	control, exists, err := db.store().ReadTurnControl(t.Context())
	if err != nil || (exists && control.Phase != "closed") {
		t.Fatalf("intentional stop opened breaker: %+v %v", control, err)
	}
}

type cancelObserver struct {
	recRunner
	db       *DB
	turnID   string
	canceled bool
}

func (r *cancelObserver) StopFolder(_ context.Context, _ string) (runedv1.StopRunResponse, error) {
	tc, ok := r.db.GetTurnContext(r.turnID)
	control, _, err := r.db.store().ReadTurnControl(context.Background())
	r.canceled = ok && tc.State == store.TurnDone && tc.RunReturned && control.ProbeTurnID == ""
	return runedv1.StopRunResponse{Killed: true}, err
}

func TestTurnControlCancelBeforeKill(t *testing.T) {
	db, l, _, tc, _ := deferredFixture(t)
	now := tc.NextAttemptAt
	l.q.now = func() time.Time { return now }
	l.runner = runnerFn(func(context.Context, runedv1.RunRequest) (runedv1.RunOutcome, error) {
		return runedv1.RunOutcome{Outcome: runedv1.OutcomeError}, nil
	})
	if _, err := l.replay(tc.TurnID); err != nil {
		t.Fatal(err)
	}
	before, _, _ := db.store().ReadTurnControl(t.Context())
	r := &cancelObserver{db: db, turnID: tc.TurnID}
	l.runner = r
	l.cmdStop(tc.ChatJID, tc.Folder)
	if !r.canceled {
		t.Fatal("kill preceded deferred cancellation")
	}
	canceled, _ := db.GetTurnContext(tc.TurnID)
	if _, err := l.q.complete(l, canceled, runedv1.RunOutcome{Outcome: runedv1.OutcomeError}, nil, turnWork{}); err != nil {
		t.Fatal(err)
	}
	after, _, _ := db.store().ReadTurnControl(t.Context())
	if !after.NextProbeAt.Equal(before.NextProbeAt) || after.ProbeTurnID != "" {
		t.Fatalf("cancel changed cooldown or retained ownership: %+v", after)
	}
	now = before.NextProbeAt
	if _, err := l.replay(tc.TurnID); err != nil || len(r.runs) != 0 {
		t.Fatalf("canceled turn replayed: %v", err)
	}
}

package routd

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/kronael/arizuko/ipc"
	"github.com/kronael/arizuko/objstore"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	"github.com/kronael/arizuko/store"
)

type callbackReadGate struct {
	objstore.Store
	mu            sync.Mutex
	blocked       bool
	read, release chan struct{}
}

func (g *callbackReadGate) Query(ctx context.Context, q objstore.Query) ([]objstore.Item, error) {
	items, err := g.Store.Query(ctx, q)
	if q.Entity == store.TurnContexts {
		g.mu.Lock()
		first := !g.blocked
		g.blocked = true
		g.mu.Unlock()
		if first {
			close(g.read)
			<-g.release
		}
	}
	return items, err
}

func TestModelPauseStaleCallbackAcrossRebind(t *testing.T) {
	for _, status := range []string{"error", "success"} {
		t.Run(status, func(t *testing.T) {
			db, l, p, _ := pauseFixture(t)
			pauseInput(t, db, "web:a", "held", "u", "saved", time.Now())
			msgs, err := db.NewMessagesForChat("web:a", "", 0)
			if err != nil {
				t.Fatal(err)
			}
			live, err := db.store().OpenTurnContext("held", "a", "", "web:a", "u", "", &store.TurnAdmission{AttemptID: "old", InputLast: store.MessageCursor(msgs[0])})
			if err != nil || !live {
				t.Fatalf("open: %v %v", live, err)
			}
			_, err = p.srv.mcpSubmitTurn("a", ipc.TurnResult{TurnID: "held", Status: "error", ProviderFailure: &apiv1.ProviderFailure{Reason: "outage"}})
			if err != nil {
				t.Fatal(err)
			}
			pause, err := db.store().ReadModelPause(t.Context(), "a")
			if err != nil {
				t.Fatal(err)
			}
			ws := &store.WriteSet{}
			if err := db.store().ClearModelPauseInto(ws, *pause, errors.New("changed")); err != nil {
				t.Fatal(err)
			}
			if err := db.store().Apply(t.Context(), ws); err != nil {
				t.Fatal(err)
			}
			p.blockAt, p.started, p.release = 1, make(chan struct{}), make(chan struct{})
			gate := &callbackReadGate{Store: db.obj, read: make(chan struct{}), release: make(chan struct{})}
			db.obj = gate
			result := ipc.TurnResult{TurnID: "held", Status: status, TimedOut: status == "success"}
			if status == "error" {
				result.ProviderFailure = &apiv1.ProviderFailure{Reason: "outage"}
			}
			fns := p.srv.buildRoutdFns(turnMCP{turnID: "held", folder: "a", attempt: "old"})
			done := make(chan error, 1)
			go func() { _, err := fns.SubmitTurn("a", result); done <- err }()
			<-gate.read
			runDone := make(chan error, 1)
			go func() { _, err := l.processGroupMessages("web:a"); runDone <- err }()
			<-p.started
			close(gate.release)
			callbackErr := <-done
			tc, ok := db.GetTurnContext("held")
			currentPause, err := db.store().ReadModelPause(t.Context(), "a")
			if err != nil {
				t.Fatal(err)
			}
			recorded := db.TurnResultRecorded("a", "held")
			if _, err := p.StopFolder(t.Context(), "a"); err != nil {
				t.Fatal(err)
			}
			<-runDone
			if callbackErr == nil || !ok || tc.AttemptID == "old" || tc.State != store.TurnRunning || recorded || currentPause != nil {
				t.Fatalf("stale callback: err=%v context=%+v pause=%+v", callbackErr, tc, currentPause)
			}
		})
	}
}

func (s *Server) mcpSubmitTurn(_ string, result ipc.TurnResult) (ipc.TurnAck, error) {
	return s.mcpSubmitTurnAttempt(result, "")
}

func (s *Server) recordTurnResult(turnID string, result apiv1.TurnResult) (ipc.TurnAck, error) {
	return s.recordTurnResultAttempt(turnID, "", result)
}

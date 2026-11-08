package routd

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/ipc"
	"github.com/kronael/arizuko/objstore"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/store"
)

type admissionDeleteStore struct {
	objstore.Store
	fired bool
}

func (s *admissionDeleteStore) GuardedBatch(ctx context.Context, checks []objstore.Check, writes []objstore.Write) (bool, error) {
	for _, w := range writes {
		if !s.fired && w.Entity == store.TurnContexts && w.Op == objstore.Put {
			s.fired = true
			if _, _, err := store.NewOver(s.Store).DeleteGroup("a"); err != nil {
				return false, err
			}
			break
		}
	}
	return s.Store.GuardedBatch(ctx, checks, writes)
}

type admissionProvider struct {
	srv  *Server
	runs []runedv1.RunRequest
}

func (p *admissionProvider) Run(_ context.Context, req runedv1.RunRequest) (runedv1.RunOutcome, error) {
	p.runs = append(p.runs, req)
	tc, _ := p.srv.db.GetTurnContext(req.TurnID)
	fns := p.srv.buildRoutdFns(turnMCP{folder: tc.Folder, turnID: tc.TurnID, attempt: tc.AttemptID})
	result := ipc.TurnResult{TurnID: req.TurnID, Status: "error", ProviderFailure: &apiv1.ProviderFailure{Reason: "outage"}}
	_, err := fns.SubmitTurn(tc.Folder, result)
	return runedv1.RunOutcome{RunID: "admission-run", Outcome: runedv1.OutcomeError, Terminal: true}, err
}

// A deletion between routing and admission must not restore the deleted input.
func TestAdmissionCannotRestoreDeletedInput(t *testing.T) {
	db, l, fixture, _ := pauseFixture(t)
	p := &admissionProvider{srv: fixture.srv}
	l.runner = p
	jid := "telegram:group/admission"
	doSetRoutes(t, db, []core.Route{{Match: "chat_jid=" + jid, Target: "a"}})
	pauseInput(t, db, jid, "private", "sender", "private prior tenant input", time.Now())
	gate := &admissionDeleteStore{Store: db.Obj()}
	db.obj = gate
	processPauseChat(t, l, jid)
	tc, ok := db.GetTurnContext("private")
	if !gate.fired || db.GroupExists("a") || ok {
		t.Fatalf("late admission state: %+v exists=%v", tc, ok)
	}
	if pause, err := db.store().ReadModelPause(t.Context(), "a"); err != nil || pause != nil {
		t.Fatalf("late pause: %+v err=%v", pause, err)
	}
	if routes, err := db.Routes(); err != nil || len(routes) != 0 {
		t.Fatalf("routes after deletion: %+v err=%v", routes, err)
	}
	if err := db.PutGroup(core.Group{Folder: "a"}); err != nil {
		t.Fatal(err)
	}
	processPauseChat(t, l, jid)
	for _, run := range p.runs {
		if strings.Contains(run.MessageBatch, "private prior tenant input") {
			t.Fatalf("deleted input dispatched: %+v", run)
		}
	}
}

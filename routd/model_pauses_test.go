package routd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/ipc"
	"github.com/kronael/arizuko/objstore"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/store"
)

type pauseProvider struct {
	t            *testing.T
	mu           sync.Mutex
	srv          *Server
	runs         []runedv1.RunRequest
	reasons      []string
	blockAt      int
	started      chan struct{}
	release      chan struct{}
	stopErr      error
	stops        int
	kills        []string
	busy         bool
	noSubmission bool
	localOutcome runedv1.RunOutcome
	runErr       error
}

func (p *pauseProvider) Kill(_ context.Context, runID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.kills = append(p.kills, runID)
	return p.stopErr
}

func (p *pauseProvider) Run(_ context.Context, req runedv1.RunRequest) (runedv1.RunOutcome, error) {
	p.mu.Lock()
	p.runs = append(p.runs, req)
	n := len(p.runs)
	reason := ""
	if n <= len(p.reasons) {
		reason = p.reasons[n-1]
	}
	p.mu.Unlock()
	if n == p.blockAt {
		release := p.release
		close(p.started)
		<-release
		return runedv1.RunOutcome{}, errors.New("interrupted run")
	}
	if p.busy {
		return runedv1.RunOutcome{Busy: true}, nil
	}
	if p.noSubmission {
		return p.localOutcome, nil
	}
	result := ipc.TurnResult{TurnID: req.TurnID, Status: "success", SessionID: "preserved"}
	if reason == "" {
		p.srv.replyForTest(p.t, req.TurnID, req.ChatJID, "recovered")
	}
	if reason != "" {
		result.Status, result.Error = "error", "provider error envelope"
		result.ProviderFailure = &apiv1.ProviderFailure{Reason: reason}
	}
	if _, err := p.srv.mcpSubmitTurn(string(req.Folder), result); err != nil {
		return runedv1.RunOutcome{}, err
	}
	out := runedv1.RunOutcome{RunID: fmt.Sprintf("run-%d", n), Outcome: runedv1.OutcomeOK}
	if reason != "" {
		out.Outcome, out.Terminal = runedv1.OutcomeError, true
	}
	return out, p.runErr
}

func (p *pauseProvider) StopFolder(context.Context, string) (runedv1.StopRunResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stops++
	if p.stopErr != nil {
		return runedv1.StopRunResponse{}, p.stopErr
	}
	if p.release != nil {
		close(p.release)
		p.release = nil
	}
	return runedv1.StopRunResponse{}, nil
}

func pauseFixture(t *testing.T, reasons ...string) (*DB, *Loop, *pauseProvider, *recDeliverer) {
	t.Helper()
	db, loop, _ := recLoop(t)
	dl := &recDeliverer{}
	provider := &pauseProvider{t: t, reasons: reasons}
	loop.runner, loop.deliver = provider, dl
	provider.srv = NewServer(db, loop, dl, nil, 0, "")
	for _, folder := range []string{"a", "b"} {
		if err := db.PutGroup(core.Group{Folder: folder}); err != nil {
			t.Fatal(err)
		}
	}
	return db, loop, provider, dl
}

func pauseInput(t *testing.T, db *DB, jid, id, sender, content string, at time.Time) {
	t.Helper()
	if err := db.PutMessage(core.Message{ChatJID: jid, ID: id, Sender: sender, Content: content, Timestamp: at, Verb: "message"}); err != nil {
		t.Fatal(err)
	}
}

func processPauseChat(t *testing.T, l *Loop, jid string) {
	t.Helper()
	if _, err := l.processGroupMessages(jid); err != nil {
		t.Fatal(err)
	}
}

func pauseChatInFolder(t *testing.T, db *DB, jid, folder string) {
	t.Helper()
	if err := db.store().InsertRouteToken("pause-"+jid, store.RouteToken{JID: jid, OwnerFolder: folder, TargetFolder: folder}); err != nil {
		t.Fatal(err)
	}
}

func TestModelPauseNoticeDurableBeyondAppendWindow(t *testing.T) {
	db, l, _, dl := pauseFixture(t, "rate_limit")
	dl.failSend = true
	pauseInput(t, db, "web:a", "held", "u", "keep", time.Now())
	processPauseChat(t, l, "web:a")
	tc, _ := db.GetTurnContext("held")
	for i := range 260 {
		pauseInput(t, db, "web:a", fmt.Sprintf("extra-%d", i), "u", "extra", time.Now())
	}
	for range 3 {
		if err := l.publishStatus(tc, turnStatus{phase: "waiting", cause: "Model unavailable", retry: 1, max: 3, at: l.q.now().Add(time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	msgs, err := db.MessagesSince("web:a", "")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, m := range msgs {
		if m.Verb == "status" {
			n++
		}
	}
	pending, err := db.store().PendingOutbound(t.Context(), time.Now().Add(time.Hour), 500)
	if err != nil || n != 1 || len(pending) != 1 {
		t.Fatalf("notices=%d pending=%d %v", n, len(pending), err)
	}
	dl.failSend = false
	l.nextRetryAt = time.Time{}
	l.maybeRetryOutbound(time.Now().Add(time.Hour))
	if pending, err := db.store().PendingOutbound(t.Context(), time.Now().Add(2*time.Hour), 500); err != nil || len(pending) != 0 {
		t.Fatalf("notice did not retry: %+v %v", pending, err)
	}
}

func TestModelPauseStopCapturedRunErrors(t *testing.T) {
	for _, tt := range []struct {
		name    string
		err     error
		wantErr bool
	}{
		{"success", nil, false},
		{"missing", &runedv1.APIError{Status: http.StatusNotFound, Code: "unknown_run"}, false},
		{"wrapped missing", fmt.Errorf("wrapped: %w", &runedv1.APIError{Status: http.StatusNotFound, Code: "unknown_run"}), false},
		{"other 404", &runedv1.APIError{Status: http.StatusNotFound, Code: "other"}, true},
		{"wrong status", &runedv1.APIError{Status: http.StatusInternalServerError, Code: "unknown_run"}, true},
		{"auth", &runedv1.APIError{Status: http.StatusUnauthorized, Code: "unauthorized"}, true},
		{"transport", errors.New("connection refused"), true},
		{"kill failure", &runedv1.APIError{Status: http.StatusInternalServerError, Code: "kill_failed"}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := &pauseProvider{stopErr: tt.err}
			l := &Loop{runner: p}
			err := l.stopModelProbe(TurnContext{RunID: "captured", Folder: "a"})
			if (err != nil) != tt.wantErr {
				t.Fatalf("error=%v want error=%t", err, tt.wantErr)
			}
			if tt.wantErr && !errors.Is(err, tt.err) {
				t.Fatalf("error %v does not preserve %v", err, tt.err)
			}
			if len(p.kills) != 1 || p.kills[0] != "captured" || p.stops != 0 {
				t.Fatalf("kills=%v stops=%d", p.kills, p.stops)
			}
		})
	}
}

type deadlinePauseProvider struct{ pauseProvider }

func (p *deadlinePauseProvider) Kill(ctx context.Context, _ string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(6 * time.Second):
		return errors.New("no deadline")
	}
}
func (p *deadlinePauseProvider) StopFolder(ctx context.Context, _ string) (runedv1.StopRunResponse, error) {
	select {
	case <-ctx.Done():
		return runedv1.StopRunResponse{}, ctx.Err()
	case <-time.After(6 * time.Second):
		return runedv1.StopRunResponse{}, errors.New("no deadline")
	}
}

func TestModelPauseStopDeadline(t *testing.T) {
	for _, runID := range []string{"captured", ""} {
		t.Run(fmt.Sprintf("runID=%s", runID), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				l := &Loop{runner: &deadlinePauseProvider{}}
				start := time.Now()
				err := l.stopModelProbe(TurnContext{RunID: runID, Folder: "a"})
				if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 5*time.Second {
					t.Fatalf("stop error=%v duration=%v", err, time.Since(start))
				}
			})
		})
	}
}

type deleteGroupAtAdmission struct {
	objstore.Store
	fired bool
}

func (s *deleteGroupAtAdmission) GuardedBatch(ctx context.Context, checks []objstore.Check, writes []objstore.Write) (bool, error) {
	for _, w := range writes {
		if !s.fired && w.Entity == store.TurnContexts && w.Op == objstore.Put {
			s.fired = true
			_, err := s.Store.GuardedBatch(ctx, nil, []objstore.Write{{Op: objstore.Delete, Entity: store.Groups, Key: objstore.Key{Partition: store.OwnerRoutd, Sort: "a"}}})
			if err != nil {
				return false, err
			}
			break
		}
	}
	return s.Store.GuardedBatch(ctx, checks, writes)
}

func TestMissingGroupAtAdmissionLeavesInputUnanswered(t *testing.T) {
	db, loop, provider, deliver := pauseFixture(t)
	pauseInput(t, db, "web:a", "input", "u", "question", time.Now())
	gate := &deleteGroupAtAdmission{Store: db.obj}
	db.obj = gate
	_, err := loop.processGroupMessages("web:a")
	if !gate.fired || !errors.Is(err, store.ErrGroupMissing) {
		t.Fatalf("admission: fired=%v err=%v", gate.fired, err)
	}
	if db.GetAgentCursor("web:a") != "" || len(provider.runs) != 0 || len(deliver.sentMsgs()) != 0 {
		t.Fatalf("input consumed or answered: cursor=%q runs=%d notices=%v", db.GetAgentCursor("web:a"), len(provider.runs), deliver.sentMsgs())
	}
	if p, err := db.store().ReadModelPause(t.Context(), "a"); err != nil || p != nil {
		t.Fatalf("pause: %+v err=%v", p, err)
	}
}

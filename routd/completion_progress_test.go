package routd

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/store"
)

// A failed terminal edit retries the persisted completion outcome at run return.
func TestCompletionProgressRetryPreservesOutcome(t *testing.T) {
	for _, mode := range []string{"continued silence", "timeout", "delivered", "social"} {
		t.Run(mode, func(t *testing.T) {
			db, srv, _ := newTestRoutd(t)
			dl := &recDeliverer{}
			srv.deliver = dl
			srv.loop.deliver = dl
			if err := db.PutGroup(core.Group{Folder: "demo"}); err != nil {
				t.Fatal(err)
			}
			doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "demo"}})
			srv.loop.runner = runnerFn(func(_ context.Context, req runedv1.RunRequest) (runedv1.RunOutcome, error) {
				if err := srv.mcpSubmitStatus(req.TurnID, "working"); err != nil {
					return runedv1.RunOutcome{}, err
				}
				if mode == "continued silence" {
					check, err := srv.mcpCheckTurn(req.TurnID)
					if err != nil || check.Nudge == "" {
						t.Fatalf("first Stop check=%+v err=%v", check, err)
					}
				}
				if mode == "delivered" {
					srv.replyForTest(t, req.TurnID, req.ChatJID, "answer")
				}
				if mode == "social" {
					bound := turnMCP{folder: "demo", chatJID: req.ChatJID, turnID: req.TurnID, trigger: req.TriggerSender}
					if err := srv.buildRoutdFns(bound).Like(req.ChatJID, "p9", "👍"); err != nil {
						return runedv1.RunOutcome{}, err
					}
				}
				dl.editErr = errors.New("transient terminal edit failed")
				ack, err := srv.recordTurnResult(req.TurnID, apiv1.TurnResult{
					TurnID: req.TurnID, Status: "success", TimedOut: mode == "timeout",
				})
				if err != nil || ack.Nudge != "" {
					t.Fatalf("completion ack=%+v err=%v", ack, err)
				}
				rows := statusRows(t, db, req.ChatJID)
				if len(rows) != 1 || rows[0].Content != "⏳ working" || len(dl.edits) != 1 {
					t.Fatalf("first edit did not fail: rows=%+v edits=%+v", rows, dl.edits)
				}
				dl.editErr = nil
				return runedv1.RunOutcome{RunID: "completion-run", Outcome: runedv1.OutcomeOK}, nil
			})
			if err := db.PutMessage(core.Message{
				ID: "t1", ChatJID: progressJID, Sender: "human", Verb: "mention",
				Content: "request", Timestamp: time.Now(),
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := srv.loop.processGroupMessages(progressJID); err != nil {
				t.Fatal(err)
			}
			wantMark, wantStatus, wantFailure := "✗ working", "error", silentTurnNotice
			if mode == "timeout" {
				wantFailure = timedOutNotice
			}
			if mode == "delivered" || mode == "social" {
				wantMark, wantStatus, wantFailure = "✓ working", "success", ""
			}
			result, err := db.store().GetTurnResult("demo", "t1")
			if err != nil || result.Status != wantStatus || db.TurnFailure("demo", "t1") != wantFailure {
				t.Fatalf("persisted completion=%+v failure=%q err=%v", result, db.TurnFailure("demo", "t1"), err)
			}
			rows := statusRows(t, db, progressJID)
			if len(rows) != 1 || rows[0].Content != wantMark || len(dl.edits) != 2 || dl.edits[1].content != wantMark {
				t.Fatalf("run return lost terminal outcome: want=%q rows=%+v edits=%+v", wantMark, rows, dl.edits)
			}
		})
	}
}

type failedCompletionRead struct {
	objstore.Store
	err error
}

func (s failedCompletionRead) Query(ctx context.Context, q objstore.Query) ([]objstore.Item, error) {
	if q.Entity == store.TurnResults {
		return nil, s.err
	}
	return s.Store.Query(ctx, q)
}

// A failed result lookup leaves completion undecided and the inbound queued.
func TestCompletionReadFailureDoesNotFinishTurn(t *testing.T) {
	db, srv, _ := newTestRoutd(t)
	wantErr := errors.New("completion read unavailable")
	db.obj = failedCompletionRead{Store: db.Obj(), err: wantErr}
	dl := &recDeliverer{}
	srv.deliver = dl
	srv.loop.deliver = dl
	if err := db.PutGroup(core.Group{Folder: "demo"}); err != nil {
		t.Fatal(err)
	}
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "demo"}})
	srv.loop.runner = runnerFn(func(_ context.Context, req runedv1.RunRequest) (runedv1.RunOutcome, error) {
		if err := srv.mcpSubmitStatus(req.TurnID, "working"); err != nil {
			return runedv1.RunOutcome{}, err
		}
		return runedv1.RunOutcome{RunID: "completion-run", Outcome: runedv1.OutcomeOK}, nil
	})
	if err := db.PutMessage(core.Message{
		ID: "t1", ChatJID: progressJID, Sender: "human", Verb: "mention",
		Content: "request", Timestamp: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.loop.processGroupMessages(progressJID); !errors.Is(err, wantErr) {
		t.Fatalf("completion read error=%v, want %v", err, wantErr)
	}
	turn, _ := db.GetTurnContext("t1")
	rows := statusRows(t, db, progressJID)
	if turn.State != store.TurnRunning || db.GetAgentCursor(progressJID) != "" ||
		len(dl.edits) != 0 || len(rows) != 1 || rows[0].Content != "⏳ working" {
		t.Fatalf("failed lookup completed the turn: turn=%+v rows=%+v edits=%+v", turn, rows, dl.edits)
	}
}

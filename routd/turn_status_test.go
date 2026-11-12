package routd

import (
	"context"
	"errors"
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

func TestTurnStatusLifecycle(t *testing.T) {
	for _, tc := range []struct {
		jid      string
		progress bool
	}{
		{progressJID, false},
		{progressJID, true},
		{"github:kronael/arizuko/pr/12", false},
		{"gdrive:file/c/thread", false},
	} {
		jid, progress := tc.jid, tc.progress
		t.Run(jid+"/"+map[bool]string{false: "first-failure", true: "progress-to-failure"}[progress], func(t *testing.T) {
			db, err := OpenMem()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			if err := db.PutGroup(core.Group{Folder: "demo"}); err != nil {
				t.Fatal(err)
			}
			dl := &recDeliverer{}
			srv := NewServer(db, nil, dl, nil, 0, "")
			attempts := 0
			runner := runnerFn(func(_ context.Context, req runedv1.RunRequest) (runedv1.RunOutcome, error) {
				attempts++
				if progress && attempts == 1 {
					if err := srv.mcpSubmitStatus(req.TurnID, "working"); err != nil {
						t.Fatal(err)
					}
				}
				if attempts == 3 {
					if _, err := db.RecordTurnResult("demo", req.TurnID, "", "success", "", store.TurnUsage{}); err != nil {
						t.Fatal(err)
					}
					return runedv1.RunOutcome{Outcome: runedv1.OutcomeOK}, nil
				}
				return runedv1.RunOutcome{Outcome: runedv1.OutcomeError, Error: "vm record: /private/token: no space left on device"}, nil
			})
			loop := NewLoop(db, runner, LoopConfig{Deliver: dl, MaxTurnRetry: 3})
			loop.StopQueue()
			now := time.Date(2026, 11, 9, 3, 0, 0, 0, time.UTC)
			loop.q.now = func() time.Time { return now }
			doSetRoutes(t, db, []core.Route{{Match: "platform=" + jidScheme(jid), Target: "demo"}})
			if err := db.PutMessage(core.Message{ID: "turn", ChatJID: jid, Sender: "u", Content: "hi", Verb: "mention", Timestamp: now}); err != nil {
				t.Fatal(err)
			}
			if _, err := loop.processGroupMessages(jid); err != nil {
				t.Fatal(err)
			}
			row, ok := db.LastStatusRow(jid, "turn")
			if !ok || len(dl.sends) != 1 || row.Content != "Server storage is full. Retry 1/3 at 03:00:30 UTC." {
				t.Fatalf("first failure row=%+v sends=%+v", row, dl.sends)
			}
			if _, err := loop.processGroupMessages(jid); err != nil {
				t.Fatal(err)
			}
			parked, _ := db.GetTurnContext("turn")
			if db.GetAgentCursor(jid) != parked.InputLast {
				t.Fatal("status advanced the input cursor")
			}
			pid := row.PlatformID
			beforeLate := len(dl.edits)
			if _, err := srv.submitStatus(ipc.TurnStatus{TurnID: "turn", Text: "late old attempt"}); err != nil {
				t.Fatal(err)
			}
			afterLate, _ := db.LastStatusRow(jid, "turn")
			if afterLate.Content != row.Content || len(dl.edits) != beforeLate {
				t.Fatal("late progress replaced waiting status")
			}
			for range 2 {
				tc, _ := db.GetTurnContext("turn")
				now = tc.NextAttemptAt
				if _, err := loop.replay("turn"); err != nil {
					t.Fatal(err)
				}
			}
			if len(dl.sends) != 1 || len(dl.deletes) != 1 || dl.deletes[0].platformID != pid {
				t.Fatalf("lifecycle sends=%+v deletes=%+v", dl.sends, dl.deletes)
			}
			running := 0
			for _, edit := range dl.edits {
				if edit.platformID != pid || strings.Contains(edit.content, "/private") {
					t.Fatalf("edit identity or raw error: %+v", edit)
				}
				if strings.Contains(edit.content, "is running") {
					running++
				}
			}
			if running != 2 {
				t.Fatalf("retry admission edits=%+v", dl.edits)
			}
			before := len(dl.edits)
			if _, err := srv.submitStatus(ipc.TurnStatus{TurnID: "turn", Text: "late"}); err != nil {
				t.Fatal(err)
			}
			if len(dl.edits) != before {
				t.Fatal("late progress overwrote completion")
			}
		})
	}
}

func TestTurnStatusFinalRetained(t *testing.T) {
	dl := &recDeliverer{}
	db, _ := progressWithRow(t, dl)
	tc, _ := db.GetTurnContext("t1")
	loop := NewLoop(db, &recRunner{}, LoopConfig{Deliver: dl})
	loop.StopQueue()
	if err := loop.publishStatus(tc, turnStatus{phase: "final", cause: "Server storage is full", retry: 3}); err != nil {
		t.Fatal(err)
	}
	finishTurn(db, dl, "t1", endFailed)
	finishTurn(db, dl, "t1", endDone)
	if _, err := db.PutTurnContext("other", "demo", "", progressJID, "u", ""); err != nil {
		t.Fatal(err)
	}
	finishTurn(db, dl, "other", endDone)
	row, _ := db.LastStatusRow(progressJID, "t1")
	if row.StatusPhase != "final" || row.Content != "Server storage is full. Stopped after 3 retries." || len(dl.deletes) != 0 {
		t.Fatalf("final deleted: %+v deletes=%+v", row, dl.deletes)
	}
}

func TestTurnStatusNoRawError(t *testing.T) {
	for _, sample := range []struct{ raw, want string }{
		{"vm record: /srv/secret: no space left on device", "Server storage is full"},
		{"Guest exited with code 1: user quoted no space left on device /secret", "Agent stopped unexpectedly"},
		{"Guest timed out after 1h: token=secret", "Time limit reached"},
		{"arbitrary token=secret /srv/socket", "Agent could not complete the request"},
	} {
		got := turnFailureCause(runedv1.RunOutcome{Error: sample.raw}, nil)
		if got != sample.want {
			t.Fatalf("cause %q want %q", got, sample.want)
		}
	}
}

func TestTurnStatusForwardedCron(t *testing.T) {
	dl := &recDeliverer{}
	db, srv := newProgressTurn(t, dl, progressJID)
	if _, err := db.PutTurnContext("cron", "demo", "thread", "demo", "timed-task", progressJID); err != nil {
		t.Fatal(err)
	}
	tc, _ := db.GetTurnContext("cron")
	if err := applyStatus(db, dl, tc, tc.ChatJID, turnStatus{phase: "waiting", cause: "Model unavailable", retry: 1, max: 3, at: time.Date(2026, 11, 9, 3, 15, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
	if len(dl.sends) != 1 || dl.sends[0].jid != progressJID || dl.sends[0].threadID != "thread" {
		t.Fatalf("cron destination=%+v", dl.sends)
	}
	if _, ok := db.LastStatusRow(progressJID, tc.TurnID); !ok {
		t.Fatal("cron status stored in owner partition")
	}
	if _, err := srv.submitStatus(ipc.TurnStatus{TurnID: "cron", Text: "late"}); err != nil {
		t.Fatal(err)
	}
}

func TestTurnStatusRestartOperations(t *testing.T) {
	dl := &recDeliverer{}
	db, _ := progressWithRow(t, dl)
	tc, _ := db.GetTurnContext("t1")
	dl.editErr = errSend
	if err := applyStatus(db, dl, tc, progressJID, turnStatus{phase: "waiting", cause: "Model unavailable", retry: 1, max: 3, at: time.Now()}); err != nil {
		t.Fatal(err)
	}
	dl.editErr = nil
	restarted := NewLoop(db, &recRunner{}, LoopConfig{Deliver: dl})
	restarted.StopQueue()
	restarted.maybeRetryOutbound(time.Now().Add(time.Hour))
	row, _ := db.LastStatusRow(progressJID, "t1")
	if len(dl.edits) != 2 || dl.edits[1].platformID != row.PlatformID {
		t.Fatalf("restart lost edit: %+v", dl.edits)
	}
	dl.deleteErr = errSend
	finishTurn(db, dl, "t1", endDone)
	dl.deleteErr = nil
	restarted = NewLoop(db, &recRunner{}, LoopConfig{Deliver: dl})
	restarted.StopQueue()
	restarted.maybeRetryOutbound(time.Now().Add(2 * time.Hour))
	if len(dl.sends) != 1 || len(dl.deletes) != 2 {
		t.Fatalf("restart operations sends=%+v deletes=%+v", dl.sends, dl.deletes)
	}
}

func TestTurnStatusCompletionBeforeSend(t *testing.T) {
	dl := &recDeliverer{failSend: true}
	db, srv := newProgressTurn(t, dl, "")
	if err := srv.mcpSubmitStatus("t1", "working"); err != nil {
		t.Fatal(err)
	}
	finishTurn(db, dl, "t1", endDone)
	dl.failSend = false
	loop := NewLoop(db, &recRunner{}, LoopConfig{Deliver: dl})
	loop.StopQueue()
	loop.maybeRetryOutbound(time.Now().Add(time.Hour))
	if len(dl.sends) != 1 {
		t.Fatalf("completed status sent: %+v", dl.sends)
	}
}

func TestTurnStatusEmail(t *testing.T) {
	dl := &recDeliverer{noEdit: true, noDelete: true}
	db, _ := newProgressTurn(t, dl, "")
	if _, err := db.PutTurnContext("email-turn", "demo", "thread", "email:thread/1", "u", ""); err != nil {
		t.Fatal(err)
	}
	tc, _ := db.GetTurnContext("email-turn")
	for _, status := range []turnStatus{{phase: "waiting", cause: "Model unavailable", retry: 1, max: 3, at: time.Now()}, {phase: "running", retry: 1, max: 3}, {phase: "waiting", cause: "Model unavailable", retry: 2, max: 3, at: time.Now()}, {phase: "cleared"}, {phase: "cleared"}} {
		if err := applyStatus(db, dl, tc, tc.ChatJID, status); err != nil {
			t.Fatal(err)
		}
	}
	if len(dl.sends) != 2 || dl.sends[1].text != "The request completed." || len(dl.edits) != 0 || len(dl.deletes) != 0 {
		t.Fatalf("email sends=%+v edits=%+v deletes=%+v", dl.sends, dl.edits, dl.deletes)
	}
}

func TestTurnStatusRecoversFinalIntent(t *testing.T) {
	db, _ := newProgressTurn(t, &recDeliverer{}, "")
	tc, _ := db.GetTurnContext("t1")
	tc.FailureCause = "Server storage is full"
	if ok, err := db.store().FinishControlledTurn(t.Context(), tc, nil, nil); err != nil || !ok {
		t.Fatalf("finish %v %v", ok, err)
	}
	dl := &recDeliverer{}
	loop := NewLoop(db, &recRunner{}, LoopConfig{Deliver: dl})
	loop.StopQueue()
	loop.recoverStatuses()
	loop.recoverStatuses()
	if len(dl.sends) != 1 || dl.sends[0].text != "Server storage is full." {
		t.Fatalf("recovery sends=%+v", dl.sends)
	}
}

func TestTurnStatusFanout(t *testing.T) {
	dl := &recDeliverer{}
	db, _ := newProgressTurn(t, dl, "")
	for _, sample := range []struct{ id, jid, topic string }{{"t1", progressJID, "primary-thread"}, {"t2", "telegram:2", ""}} {
		if _, err := db.PutTurnContext(sample.id, "demo", sample.topic, sample.jid, "u", ""); err != nil {
			t.Fatal(err)
		}
		if err := db.SetTurnRunID(sample.id, "shared-run"); err != nil {
			t.Fatal(err)
		}
	}
	loop := NewLoop(db, &recRunner{}, LoopConfig{Deliver: dl})
	loop.StopQueue()
	tc, _ := db.GetTurnContext("t1")
	if err := loop.publishStatus(tc, turnStatus{phase: "final", cause: "Model unavailable"}); err != nil {
		t.Fatal(err)
	}
	if len(dl.sends) != 2 || dl.sends[0].idem == dl.sends[1].idem {
		t.Fatalf("fanout identity=%+v", dl.sends)
	}
	for _, send := range dl.sends {
		if send.jid == progressJID && send.threadID != "primary-thread" {
			t.Fatalf("lost primary thread: %+v", send)
		}
		if send.jid != progressJID && send.threadID != "" {
			t.Fatalf("foreign thread: %+v", send)
		}
	}
}

type refuseStatusClear struct{ objstore.Store }

func (s refuseStatusClear) GuardedBatch(ctx context.Context, checks []objstore.Check, writes []objstore.Write) (bool, error) {
	for _, w := range writes {
		if w.Entity == store.Messages && w.Attrs["status_phase"] == "cleared" {
			return false, errors.New("clear persistence unavailable")
		}
	}
	return s.Store.GuardedBatch(ctx, checks, writes)
}

func TestTurnStatusClearIntentSurvivesFailure(t *testing.T) {
	dl := &recDeliverer{}
	db, _ := progressWithRow(t, dl)
	base := db.Obj()
	db.obj = refuseStatusClear{base}
	finishTurn(db, dl, "t1", endDone)
	db.obj = base
	loop := NewLoop(db, &recRunner{}, LoopConfig{Deliver: dl})
	loop.StopQueue()
	loop.recoverStatuses()
	if len(dl.deletes) != 1 {
		t.Fatalf("cleanup intent lost after persistence failure: %+v", dl.deletes)
	}
}

func TestTurnStatusRetryKeepsFanout(t *testing.T) {
	dl := &recDeliverer{}
	db, _ := newProgressTurn(t, dl, "")
	for _, sample := range []struct{ id, jid string }{{"t1", progressJID}, {"t2", "telegram:2"}} {
		if _, err := db.PutTurnContext(sample.id, "demo", "", sample.jid, "u", ""); err != nil {
			t.Fatal(err)
		}
		if err := db.SetTurnRunID(sample.id, "old-run"); err != nil {
			t.Fatal(err)
		}
	}
	loop := NewLoop(db, &recRunner{}, LoopConfig{Deliver: dl})
	loop.StopQueue()
	tc, _ := db.GetTurnContext("t1")
	if err := loop.publishStatus(tc, turnStatus{phase: "waiting", cause: "Model unavailable", retry: 1, max: 3, at: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := db.store().WriteEntities(t.Context(), nil, objstore.Write{Op: objstore.Update, Entity: store.TurnContexts, Key: objstore.Key{Partition: store.OwnerRoutd, Sort: "t1"}, Attrs: map[string]string{"run_id": "new-run", "retry_count": "1"}}); err != nil {
		t.Fatal(err)
	}
	tc, _ = db.GetTurnContext("t1")
	if err := loop.publishStatus(tc, turnStatus{phase: "running", retry: 1, max: 3}); err != nil {
		t.Fatal(err)
	}
	finishTurn(db, dl, "t1", endDone)
	if len(dl.sends) != 2 || len(dl.edits) != 2 || len(dl.deletes) != 2 {
		t.Fatalf("retry lost fanout: sends=%+v edits=%+v deletes=%+v", dl.sends, dl.edits, dl.deletes)
	}
}

func TestTurnStatusFailureAfterEarlySuccess(t *testing.T) {
	dl := &recDeliverer{}
	db, _ := progressWithRow(t, dl)
	first := dl.sends[0]
	finishTurn(db, dl, "t1", endDone)
	tc, _ := db.GetTurnContext("t1")
	loop := NewLoop(db, &recRunner{}, LoopConfig{Deliver: dl})
	loop.StopQueue()
	if err := loop.publishStatus(tc, turnStatus{phase: "final", cause: "Agent stopped unexpectedly"}); err != nil {
		t.Fatal(err)
	}
	if len(dl.sends) != 2 || dl.sends[1].idem == first.idem || len(dl.edits) != 0 {
		t.Fatalf("edited deleted status: sends=%+v edits=%+v", dl.sends, dl.edits)
	}
}

func TestTurnStatusEmailCancelsUnsentWaiting(t *testing.T) {
	dl := &recDeliverer{failSend: true}
	db, _ := newProgressTurn(t, dl, "")
	if _, err := db.PutTurnContext("mail", "demo", "", "email:thread/1", "u", ""); err != nil {
		t.Fatal(err)
	}
	tc, _ := db.GetTurnContext("mail")
	if err := applyStatus(db, dl, tc, tc.ChatJID, turnStatus{phase: "waiting", cause: "Model unavailable", retry: 1, max: 3, at: time.Now()}); err != nil {
		t.Fatal(err)
	}
	dl.failSend = false
	if err := applyStatus(db, dl, tc, tc.ChatJID, turnStatus{phase: "cleared"}); err != nil {
		t.Fatal(err)
	}
	loop := NewLoop(db, &recRunner{}, LoopConfig{Deliver: dl})
	loop.StopQueue()
	loop.maybeRetryOutbound(time.Now().Add(time.Hour))
	if len(dl.sends) != 2 || dl.sends[1].text != "The request completed." {
		t.Fatalf("email sent stale waiting after recovery: %+v", dl.sends)
	}
}

func TestTurnStatusFinalIntentWinsCompletion(t *testing.T) {
	dl := &recDeliverer{}
	db, _ := progressWithRow(t, dl)
	tc, _ := db.GetTurnContext("t1")
	tc.FailureCause = "Agent stopped unexpectedly"
	if ok, err := db.store().FinishControlledTurn(t.Context(), tc, nil, nil); err != nil || !ok {
		t.Fatalf("final intent %v %v", ok, err)
	}
	finishTurn(db, dl, "t1", endDone)
	loop := NewLoop(db, &recRunner{}, LoopConfig{Deliver: dl})
	loop.StopQueue()
	loop.recoverStatuses()
	row, _ := db.LastStatusRow(progressJID, "t1")
	if row.StatusPhase != "final" || row.Content != "Agent stopped unexpectedly." || len(dl.deletes) != 0 {
		t.Fatalf("completion lost final: row=%+v deletes=%+v", row, dl.deletes)
	}
}

func TestTurnStatusDuplicateResultKeepsSuccess(t *testing.T) {
	dl := &recDeliverer{}
	db, srv := progressWithRow(t, dl)
	srv.replyForTest(t, "t1", progressJID, "answer")
	if _, err := srv.recordTurnResult("t1", apiv1.TurnResult{TurnID: "t1", Status: "success"}); err != nil {
		t.Fatal(err)
	}
	before := len(dl.sends)
	if _, err := srv.recordTurnResult("t1", apiv1.TurnResult{TurnID: "t1", Status: "error", Error: "private diagnostic"}); err != nil {
		t.Fatal(err)
	}
	row, _ := db.LastStatusRow(progressJID, "t1")
	if row.StatusPhase != "cleared" || len(dl.sends) != before || len(dl.edits) != 0 {
		t.Fatalf("duplicate result changed success: %+v sends=%+v edits=%+v", row, dl.sends, dl.edits)
	}
}

func TestTurnStatusRetiredIntentDoesNotRetry(t *testing.T) {
	dl := &recDeliverer{}
	db, _ := progressWithRow(t, dl)
	if err := db.store().WriteEntities(t.Context(), nil, objstore.Write{Op: objstore.Update, Entity: store.TurnContexts, Key: objstore.Key{Partition: store.OwnerRoutd, Sort: "t1"}, Attrs: map[string]string{"status_owed": "waiting", "failure_cause": "Model unavailable"}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.store().DeleteGroup("demo"); err != nil {
		t.Fatal(err)
	}
	loop := NewLoop(db, &recRunner{}, LoopConfig{Deliver: dl})
	loop.StopQueue()
	loop.recoverStatuses()
	row, _ := db.LastStatusRow(progressJID, "t1")
	if row.StatusPhase != "cleared" || len(dl.edits) != 0 || len(dl.deletes) != 1 {
		t.Fatalf("retired turn advertised retry: %+v edits=%+v", row, dl.edits)
	}
}

func TestTurnStatusStopPostsOnce(t *testing.T) {
	for _, sample := range []struct {
		name, returnTo string
		killed         bool
	}{{"active", "", true}, {"pending", "", false}, {"forwarded", "telegram:2", true}} {
		t.Run(sample.name, func(t *testing.T) {
			dl := &recDeliverer{}
			db, _ := newProgressTurn(t, dl, sample.returnTo)
			runner := &stopRunner{resp: runedv1.StopRunResponse{Killed: sample.killed}}
			loop := NewLoop(db, runner, LoopConfig{Deliver: dl})
			loop.StopQueue()
			loop.cmdStop(progressJID, "demo")
			var messages []sentMsg
			for _, sent := range dl.sends {
				if sent.jid == progressJID {
					messages = append(messages, sent)
				}
			}
			if len(messages) != 1 || messages[0].text != renderTurnStatus(turnStatus{phase: "stopped"}) {
				t.Fatalf("stop did not post once to command chat: %+v", dl.sends)
			}
		})
	}
}

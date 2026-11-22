package routd

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/ipc"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/store"
)

// Checkpoints and both ceiling paths create one bound successor behind pending work and preserve native sessions.
func TestCheckpointContinuation(t *testing.T) {
	for _, mode := range []string{"checkpoint", "guest_ceiling", "transport_ceiling", "busy", "normal"} {
		for _, harness := range []string{"claude", "codex", "pi", "isolated"} {
			t.Run(mode+"/"+harness, func(t *testing.T) {
				db, err := OpenMem()
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { db.Close() })
				if err := db.PutGroup(core.Group{Folder: "demo"}); err != nil {
					t.Fatal(err)
				}
				doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "demo"}})
				const jid = "slack:T/C/U"
				const topic = "pr/485"
				sender := "slack:user"
				if harness == "isolated" {
					sender = core.IsolatedSenderPrefix + "task"
					if err := db.PutSession("demo", topic, "chat-session"); err != nil {
						t.Fatal(err)
					}
				}
				var srv *Server
				var requests []runedv1.RunRequest
				deliver := &recDeliverer{}
				native := harness + "-native-session"
				runner := runnerFn(func(_ context.Context, req runedv1.RunRequest) (runedv1.RunOutcome, error) {
					requests = append(requests, req)
					if len(requests) > 1 {
						if mode == "busy" && len(requests) == 2 {
							return runedv1.RunOutcome{Busy: true}, nil
						}
						if !strings.Contains(req.MessageBatch, "Continue the work in this same session") {
							t.Fatal(req.MessageBatch)
						}
						srv.replyForTest(t, req.TurnID, jid, "done")
						if _, err := srv.recordTurnResult(req.TurnID, apiv1.TurnResult{Status: "success", SessionID: native}); err != nil {
							t.Fatal(err)
						}
						return runedv1.RunOutcome{Outcome: runedv1.OutcomeOK}, nil
					}
					if _, err := srv.submitStatus(ipc.TurnStatus{TurnID: req.TurnID, SessionID: native}); err != nil {
						t.Fatal(err)
					}
					if harness == "isolated" {
						current, _ := db.GetTurnContext(req.TurnID)
						if current.SessionID != native || db.SessionID("demo", topic) != "chat-session" {
							t.Fatal("isolated session changed chat lineage")
						}
					} else if got := db.SessionID("demo", topic); got != native {
						t.Fatalf("live session = %q", got)
					}
					if mode == "transport_ceiling" {
						return runedv1.RunOutcome{}, context.DeadlineExceeded
					}
					if mode == "guest_ceiling" {
						return runedv1.RunOutcome{Outcome: runedv1.OutcomeError, Terminal: true, TimedOut: true}, nil
					}
					srv.replyForTest(t, req.TurnID, jid, "summary")
					for range 2 {
						if _, err := srv.recordTurnResult(req.TurnID, apiv1.TurnResult{Status: "success", SessionID: native, Checkpoint: mode == "checkpoint" || mode == "busy"}); err != nil {
							t.Fatal(err)
						}
					}
					return runedv1.RunOutcome{Outcome: runedv1.OutcomeOK}, nil
				})
				loop := NewLoop(db, runner, LoopConfig{MaxAgents: 1, SessionIdle: time.Nanosecond, Deliver: deliver})
				loop.StopQueue()
				srv = NewServer(db, nil, deliver, nil, 0, "")
				srv.loop = loop
				if err := db.PutMessage(core.Message{ID: "work", ChatJID: jid, Topic: topic, Sender: sender, Content: "finish the PR", Timestamp: time.Now().Add(-time.Minute), Hops: 1, LinkContext: "original link instructions"}); err != nil {
					t.Fatal(err)
				}
				loop.q.pending["waiting-chat"] = turnReservation{folder: "demo", turnID: "waiting"}
				loop.q.waitingGroups = []string{"waiting-chat"}
				if _, err := loop.processGroupMessages(jid); err != nil {
					t.Fatal(err)
				}
				id := checkpointTurnID("work")
				next, ok := db.GetTurnContext(id)
				if mode == "normal" {
					if ok {
						t.Fatal("normal completion enqueued a continuation")
					}
					return
				}
				if !ok || next.Folder != "demo" || next.Topic != topic || next.ChatJID != jid || next.Trigger != sender || next.RetryCount != 0 || next.TriggerMsgID != "work" {
					t.Fatalf("continuation = %+v, exists=%t", next, ok)
				}
				if len(loop.q.waitingGroups) != 2 || loop.q.waitingGroups[0] != "waiting-chat" || loop.q.waitingGroups[1] != deferredWorkPrefix+id {
					t.Fatalf("queue = %q", loop.q.waitingGroups)
				}
				current, _ := db.GetTurnContext("work")
				if _, err := loop.q.complete(loop, current, runedv1.RunOutcome{TimedOut: true}, nil, turnWork{}); err != nil {
					t.Fatal(err)
				}
				if len(loop.q.waitingGroups) != 2 {
					t.Fatal("duplicate continuation")
				}
				if err := db.PutMessage(core.Message{ID: "unread", ChatJID: jid, Topic: topic, Sender: "slack:user", Content: "new input", Timestamp: time.Now()}); err != nil {
					t.Fatal(err)
				}
				if _, err := loop.replay(id); err != nil {
					t.Fatal(err)
				}
				wantRuns := 2
				if mode == "busy" {
					pending, _ := db.GetTurnContext(id)
					if pending.State != store.TurnWaitingCapacity || pending.CapacityFailures != 1 {
						t.Fatalf("busy continuation=%+v", pending)
					}
					loop.q.now = func() time.Time { return pending.NextAttemptAt }
					if _, err := loop.replay(id); err != nil {
						t.Fatal(err)
					}
					wantRuns = 3
				}
				if len(requests) != wantRuns || requests[len(requests)-1].SessionID != native {
					t.Fatalf("resumed requests = %+v", requests)
				}
				unread, _ := db.MessageByID(jid, "unread")
				if db.GetAgentCursor(jid) >= store.MessageCursor(unread) {
					t.Fatal("continuation consumed unread input")
				}
				if control, exists, err := db.store().ReadTurnControl(context.Background()); err != nil || exists && control.Phase != "closed" {
					t.Fatalf("checkpoint opened failure circuit: %+v, %v", control, err)
				}
			})
		}
	}
}

// The eighth continuation stops once; only a new non-bot message in the same topic resets the guard.
func TestCheckpointGuard(t *testing.T) {
	dl := &recDeliverer{}
	db, srv := newProgressTurn(t, dl, "")
	loop := NewLoop(db, nil, LoopConfig{Deliver: dl})
	loop.StopQueue()
	srv.loop = loop
	base := time.Now().Add(-time.Minute)
	if err := db.PutMessage(core.Message{ID: "start", ChatJID: progressJID, Sender: "slack:user",
		Content: "work", Timestamp: base.Add(-time.Second)}); err != nil {
		t.Fatal(err)
	}
	for i := range 8 {
		if err := db.PutMessage(core.Message{ID: checkpointTurnID(fmt.Sprint(i)), ChatJID: progressJID,
			Sender: store.SystemSender, BotMsg: true, Timestamp: base.Add(time.Duration(i) * time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	for _, message := range []core.Message{
		{ID: "bot-summary", ChatJID: progressJID, Sender: "demo", BotMsg: true, Timestamp: base.Add(10 * time.Second)},
		{ID: "other-topic", ChatJID: progressJID, Topic: "other", Sender: "slack:user", Timestamp: base.Add(11 * time.Second)},
	} {
		if err := db.PutMessage(message); err != nil {
			t.Fatal(err)
		}
	}
	tc, _ := db.GetTurnContext("t1")
	start, _ := db.MessageByID(progressJID, "start")
	tc.InputLast = store.MessageCursor(start)
	if count, err := loop.continuationCount(tc); err != nil || count != 8 {
		messages, _ := db.MessagesBefore(progressJID, "", 100)
		t.Fatalf("continuation count=%d, err=%v, messages=%+v", count, err, messages)
	}
	for range 2 {
		if err := loop.continueTurn(tc); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := db.GetTurnContext(checkpointTurnID("t1")); ok {
		t.Fatal("guard enqueued ninth continuation")
	}
	notices := 0
	for _, message := range dl.sentMsgs() {
		if strings.Contains(message.text, "Work stopped after eight continuations") {
			notices++
		}
	}
	if notices != 1 {
		t.Fatalf("stop notices=%d", notices)
	}
	if err := db.PutMessage(core.Message{ID: "resume", ChatJID: progressJID, Sender: "slack:user", Timestamp: time.Now()}); err != nil {
		t.Fatal(err)
	}
	tc.InputLast = db.GetAgentCursor(progressJID)
	if tc.InputLast == "" {
		message, _ := db.MessageByID(progressJID, "resume")
		tc.InputLast = store.MessageCursor(message)
	}
	if err := loop.continueTurn(tc); err != nil {
		t.Fatal(err)
	}
	if _, ok := db.GetTurnContext(checkpointTurnID("t1")); !ok {
		t.Fatal("new message did not reset guard")
	}
}

// A delivered stop prevents a stale checkpoint callback from creating a successor.
func TestCheckpointAfterStop(t *testing.T) {
	dl := &recDeliverer{}
	db, _ := newProgressTurn(t, dl, "")
	loop := NewLoop(db, &recoveryRunner{}, LoopConfig{Deliver: dl})
	loop.StopQueue()
	tc, _ := db.GetTurnContext("t1")
	if _, err := loop.q.cancel(loop, tc.Folder); err != nil {
		t.Fatal(err)
	}
	if err := loop.continueTurn(tc); err != nil {
		t.Fatal(err)
	}
	if _, exists := db.GetTurnContext(checkpointTurnID(tc.TurnID)); exists {
		t.Fatal("stopped work continued")
	}
}

// Restart recovery schedules an acknowledged checkpoint once while retaining its bound topic and caller.
func TestCheckpointRecovery(t *testing.T) {
	db, _, _, tc, _ := deferredFixture(t)
	if ok, err := db.store().ClaimDeferredTurn(t.Context(), tc, "attempt", tc.NextAttemptAt); err != nil || !ok {
		t.Fatalf("claim=%t, %v", ok, err)
	}
	loop := NewLoop(db, &recoveryRunner{}, LoopConfig{})
	loop.StopQueue()
	srv := NewServer(db, nil, nil, nil, 0, "")
	srv.loop = loop
	if _, err := srv.recordTurnResult(tc.TurnID, apiv1.TurnResult{Status: "success", Checkpoint: true, SessionID: "native"}); err != nil {
		t.Fatal(err)
	}
	current, _ := db.GetTurnContext(tc.TurnID)
	if current.State != store.TurnRunning {
		t.Fatalf("checkpoint closed before successor save: %+v", current)
	}
	for range 2 {
		if err := loop.q.recover(loop, true); err != nil {
			t.Fatal(err)
		}
	}
	next, ok := db.GetTurnContext(checkpointTurnID(tc.TurnID))
	if !ok || next.Topic != tc.Topic || next.Trigger != tc.Trigger || next.ReturnTo != tc.ReturnTo {
		t.Fatalf("recovered continuation=%+v", next)
	}
	if len(loop.q.waitingGroups) != 1 {
		t.Fatalf("queue=%v", loop.q.waitingGroups)
	}
}

// A continuation in a root topic retains the real source message as its reply anchor.
func TestCheckpointReplyAnchor(t *testing.T) {
	dl := &recDeliverer{}
	db, _ := newProgressTurn(t, dl, "")
	message := core.Message{ID: "real-source", ChatJID: progressJID, Sender: "slack:user", Timestamp: time.Now()}
	if err := db.PutMessage(message); err != nil {
		t.Fatal(err)
	}
	tc, _ := db.GetTurnContext("t1")
	tc.InputLast, tc.TriggerMsgID = store.MessageCursor(message), message.ID
	if err := db.SetTurnTriggerMsg(tc.TurnID, message.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.SetChatIsGroup(progressJID, true); err != nil {
		t.Fatal(err)
	}
	if err := db.SetStickyTopic(progressJID, "another-topic"); err != nil {
		t.Fatal(err)
	}
	runs := 0
	loop := NewLoop(db, runnerFn(func(_ context.Context, req runedv1.RunRequest) (runedv1.RunOutcome, error) {
		runs++
		current, _ := db.GetTurnContext(req.TurnID)
		place, err := placeTurnDelivery(db, current, progressJID, "", true)
		if err != nil || place.threadRoot != message.ID {
			t.Fatalf("reply anchor=%+v, %v", place, err)
		}
		return runedv1.RunOutcome{Outcome: runedv1.OutcomeSilent}, nil
	}), LoopConfig{})
	loop.StopQueue()
	if err := loop.continueTurn(tc); err != nil {
		t.Fatal(err)
	}
	input, ok := db.MessageByID(progressJID, checkpointTurnID(tc.TurnID))
	if !ok || input.Topic != "" {
		t.Fatalf("continuation inherited the sticky topic: %+v", input)
	}
	if _, err := loop.replay(checkpointTurnID(tc.TurnID)); err != nil {
		t.Fatal(err)
	}
	if runs != 1 {
		t.Fatalf("runs=%d", runs)
	}
}

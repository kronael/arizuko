package routd

import (
	"context"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

const nemoChat = "telegram:group/5229089286"

// replyRunner answers inside the run, the way an agent's reply tool does.
type replyRunner struct {
	srv *Server
	t   *testing.T
	req runedv1.RunRequest
}

func (r *replyRunner) Run(_ context.Context, req runedv1.RunRequest) (runedv1.RunOutcome, error) {
	r.req = req
	r.srv.replyForTest(r.t, req.TurnID, "a/b", "weekly report")
	return runedv1.RunOutcome{RunID: "r", Outcome: runedv1.OutcomeOK}, nil
}

// BUGS L596: timed's fire for owner a/b into a chat whose only rule is
// #observe. The row addresses the owner folder and carries the task chat as its
// return address, so the turn runs in a/b and answers in the chat.
func TestScheduledFireRunsInOwnerAndAnswersInTaskChat(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	dl := &recDeliverer{}
	rr := &replyRunner{srv: NewServer(db, nil, dl, nil, 0, ""), t: t}
	loop := NewLoop(db, rr, LoopConfig{})
	t.Cleanup(loop.StopQueue)
	_ = db.PutGroup(core.Group{Folder: "a/b"})
	doSetRoutes(t, db, []core.Route{{Match: "platform=telegram", Target: "a/b?observe"}})

	m := apiv1.Message{ID: "sched-t@1", ChatJID: "a/b", ForwardedFrom: nemoChat,
		Sender: core.ScheduledSender, Content: "Run /nemo skill"}
	if err := db.PutMessage(buildMessageRow(m, time.Now().UTC(), "message")); err != nil {
		t.Fatal(err)
	}

	if _, err := loop.processGroupMessages("a/b"); err != nil {
		t.Fatalf("process: %v", err)
	}
	if rr.req.Folder != "a/b" {
		t.Fatalf("turn folder = %q, want a/b", rr.req.Folder)
	}
	if rr.req.Channel != "telegram" || rr.req.ChatJID != nemoChat {
		t.Errorf("run chat = %q (%q), want the task chat %q", rr.req.ChatJID, rr.req.Channel, nemoChat)
	}
	if len(dl.sends) != 1 || dl.sends[0].jid != nemoChat {
		t.Fatalf("delivered to %+v, want the task chat %s", dl.sends, nemoChat)
	}
}

// The same prompt posted into the observe-only chat is stored as context and
// starts no turn: the routing the scheduled fire no longer goes through.
func TestScheduledPromptInObserveChatStartsNoTurn(t *testing.T) {
	db, loop, runner := newCaptureLoop(t)
	_ = db.PutGroup(core.Group{Folder: "a/b"})
	doSetRoutes(t, db, []core.Route{{Match: "platform=telegram", Target: "a/b?observe"}})
	m := apiv1.Message{ID: "sched-t@1", ChatJID: nemoChat, Sender: core.ScheduledSender, Content: "Run /nemo skill"}
	if err := db.PutMessage(buildMessageRow(m, time.Now().UTC(), "message")); err != nil {
		t.Fatal(err)
	}
	if _, err := loop.processGroupMessages(nemoChat); err != nil {
		t.Fatalf("process: %v", err)
	}
	if runner.req.TurnID != "" {
		t.Fatalf("a turn ran: %+v", runner.req)
	}
}

// A reply delivered to the return chat is the turn's reply: the nudge and the
// failed-run retry must look there, not in the chat the run addressed.
func TestTurnDeliveredLooksInTheReturnChat(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	srv := NewServer(db, nil, &recDeliverer{}, nil, 0, "")
	if _, err := db.PutTurnContext("t-1", "a/b", "", "a/b", core.ScheduledSender, nemoChat); err != nil {
		t.Fatal(err)
	}
	if err := db.PutMessage(core.Message{ID: "out-1", ChatJID: nemoChat, Sender: "a/b", Content: "done",
		Timestamp: time.Now().UTC(), BotMsg: true, FromMe: true, TurnID: "t-1"}); err != nil {
		t.Fatal(err)
	}
	tc, _ := db.GetTurnContext("t-1")
	delivered, err := srv.turnDelivered(tc)
	if err != nil || !delivered {
		t.Fatalf("turnDelivered = %v, %v; the reply sits in %s", delivered, err, nemoChat)
	}
}

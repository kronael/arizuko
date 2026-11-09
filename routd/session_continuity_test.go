package routd

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

func TestKilledTurnResumesItsOwnSession(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_ = db.PutGroup(core.Group{Folder: "demo"})

	var sent []string
	runner := &recoveryRunner{run: runnerFn(func(_ context.Context, req runedv1.RunRequest) (runedv1.RunOutcome, error) {
		sent = append(sent, req.SessionID)
		if len(sent) == 1 {
			return runedv1.RunOutcome{}, errors.New("read: connection reset by peer")
		}
		return runedv1.RunOutcome{Outcome: runedv1.OutcomeOK}, nil
	})}
	loop := NewLoop(db, runner, LoopConfig{})
	loop.StopQueue()
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "demo"}})
	_ = db.PutMessage(core.Message{ID: "m1", ChatJID: "slack:T/C/U", Sender: "u1",
		Content: "hi", Timestamp: time.Now().UTC(), Verb: "message"})

	if _, err := loop.processGroupMessages("slack:T/C/U"); err == nil {
		t.Fatal("the killed run must surface as an error so the batch re-feeds")
	}
	tc, ok := db.GetTurnContext("m1")
	if !ok || tc.State != "pending_retry" || tc.RetryCount != 1 {
		t.Fatalf("interrupted turn not deferred: %+v", tc)
	}
	loop.q.now = func() time.Time { return tc.NextAttemptAt }
	if _, err := loop.replay("m1"); err != nil {
		t.Fatalf("re-fed dispatch: %v", err)
	}
	if len(sent) != 2 {
		t.Fatalf("runs = %d, want 2", len(sent))
	}
	if sent[0] == "" {
		t.Fatal("the killed attempt ran with no session id; nothing it wrote can be resumed")
	}
	if sent[1] != sent[0] {
		t.Fatalf("re-fed turn resumes %q, the killed attempt ran %q", sent[1], sent[0])
	}
	if got := db.SessionID("demo", ""); got != sent[0] {
		t.Fatalf("persisted session = %q, want %q", got, sent[0])
	}
}

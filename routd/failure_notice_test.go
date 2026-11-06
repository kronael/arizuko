package routd

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

func TestFailureNoticePerChat(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.PutGroup(core.Group{Folder: "demo"}); err != nil {
		t.Fatal(err)
	}
	dl := &recDeliverer{}
	cause := ""
	runner := runnerFn(func(_ context.Context, req runedv1.RunRequest) (runedv1.RunOutcome, error) {
		if cause == "" {
			if _, err := db.RecordTurnResult(string(req.Folder), req.TurnID, "session", "success", ""); err != nil {
				t.Fatal(err)
			}
			if _, err := db.PutTurnContext("steered-success", "demo", "", "slack:T/B/U", "u", ""); err != nil {
				t.Fatal(err)
			}
			if err := db.SetTurnRunID("steered-success", "success-run"); err != nil {
				t.Fatal(err)
			}
			return runedv1.RunOutcome{Outcome: runedv1.OutcomeOK, RunID: "success-run"}, nil
		}
		return runedv1.RunOutcome{Outcome: runedv1.OutcomeError, Error: cause, Terminal: true}, nil
	})
	loop := NewLoop(db, runner, LoopConfig{Deliver: dl, MaxTurnRetry: -1})
	loop.StopQueue()
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "demo"}})
	steps := []struct {
		jid, cause string
		want       []string
	}{
		{"slack:T/A/U", "Monthly spend limit reached", []string{"slack:T/A/U"}},
		{"slack:T/A/U", "Organization spend limit reached", []string{"slack:T/A/U"}},
		{"slack:T/A/U", "container failed", []string{"slack:T/A/U"}},
		{"slack:T/B/U", "usage limit reached", []string{"slack:T/A/U", "slack:T/B/U"}},
		{"slack:T/A/U", "", []string{"slack:T/A/U", "slack:T/B/U"}},
		{"slack:T/A/U", "usage limit reached", []string{"slack:T/A/U", "slack:T/B/U", "slack:T/A/U"}},
		{"slack:T/B/U", "spend limit reached", []string{"slack:T/A/U", "slack:T/B/U", "slack:T/A/U", "slack:T/B/U"}},
	}
	for i, step := range steps {
		if i == 2 {
			loop = NewLoop(db, runner, LoopConfig{Deliver: dl, MaxTurnRetry: -1})
			loop.StopQueue()
		}
		cause = step.cause
		if err := db.PutMessage(core.Message{ID: fmt.Sprintf("m%d", i), ChatJID: step.jid, Sender: "u", Content: "hi", Timestamp: time.Now().UTC().Add(time.Duration(i) * time.Second), Verb: "message"}); err != nil {
			t.Fatal(err)
		}
		if _, err := loop.processGroupMessages(step.jid); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		var got []string
		for _, send := range dl.sentMsgs() {
			got = append(got, send.jid)
		}
		if !slices.Equal(got, step.want) {
			t.Fatalf("step %d: recipients=%v, want %v", i, got, step.want)
		}
	}
}

func TestFailureNoticeDoesNotRetryFailedDelivery(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.PutGroup(core.Group{Folder: "demo"}); err != nil {
		t.Fatal(err)
	}
	recorded := &recDeliverer{}
	dl := &noticeFailingDeliverer{recorded}
	runner := runnerFn(func(context.Context, runedv1.RunRequest) (runedv1.RunOutcome, error) {
		return runedv1.RunOutcome{Outcome: runedv1.OutcomeError, Error: "spend limit reached", Terminal: true}, nil
	})
	loop := NewLoop(db, runner, LoopConfig{Deliver: dl, MaxTurnRetry: -1})
	loop.StopQueue()
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "demo"}})
	for i := range 2 {
		if err := db.PutMessage(core.Message{ID: fmt.Sprintf("failed%d", i), ChatJID: "slack:T/A/U", Sender: "u", Content: "hi", Timestamp: time.Now().UTC().Add(time.Duration(i) * time.Second), Verb: "message"}); err != nil {
			t.Fatal(err)
		}
		if _, err := loop.processGroupMessages("slack:T/A/U"); err != nil {
			t.Fatal(err)
		}
	}
	if len(recorded.sends) != 1 || recorded.sends[0].jid != "slack:T/A/U" {
		t.Fatalf("delivery attempts=%+v, want one to slack:T/A/U", recorded.sends)
	}
}

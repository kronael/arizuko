package routd

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/store"
)

func TestTurnControlExecutionRetry(t *testing.T) {
	for _, sender := range []string{"user", core.ScheduledSender} {
		t.Run(sender, func(t *testing.T) {
			db, loop, _ := recLoop(t)
			if err := db.PutGroup(core.Group{Folder: "demo"}); err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
			loop.q.now = func() time.Time { return now }
			launches := 0
			loop.runner = runnerFn(func(_ context.Context, req runedv1.RunRequest) (runedv1.RunOutcome, error) {
				if req.TurnID == "retry" {
					launches++
				}
				return runedv1.RunOutcome{Outcome: runedv1.OutcomeError}, nil
			})
			if err := db.PutMessage(core.Message{ID: "retry", ChatJID: "web:demo", Sender: sender,
				Timestamp: now, Content: "question"}); err != nil {
				t.Fatal(err)
			}
			if _, err := loop.processGroupMessages("web:demo"); err != nil {
				t.Fatal(err)
			}
			delays := []time.Duration{30 * time.Second, 2 * time.Minute, 5 * time.Minute}
			if sender == core.ScheduledSender {
				delays = []time.Duration{15 * time.Minute, 45 * time.Minute, 180 * time.Minute}
			}
			for i, delay := range delays {
				tc, _ := db.GetTurnContext("retry")
				if tc.State != store.TurnPendingRetry || tc.RetryCount != i+1 || !tc.NextAttemptAt.Equal(now.Add(delay)) {
					t.Fatalf("retry %d: %+v, want delay %v", i+1, tc, delay)
				}
				if i == 0 {
					if err := db.PutMessage(core.Message{ID: "fresh", ChatJID: "web:demo", Sender: "other",
						Timestamp: now.Add(time.Second), Content: "fresh input"}); err != nil {
						t.Fatal(err)
					}
					if _, err := loop.processGroupMessages("web:demo"); err != nil {
						t.Fatal(err)
					}
					bound, _ := db.GetTurnContext("retry")
					if bound.RetryCount != tc.RetryCount || !bound.NextAttemptAt.Equal(tc.NextAttemptAt) {
						t.Fatalf("fresh input reset retry: %+v", bound)
					}
				}
				if _, err := loop.replay("retry"); err != nil || launches != i+1 {
					t.Fatalf("early replay: launches=%d err=%v", launches, err)
				}
				now = tc.NextAttemptAt
				if _, err := loop.replay("retry"); err != nil || launches != i+2 {
					t.Fatalf("due replay: launches=%d err=%v", launches, err)
				}
			}
			tc, _ := db.GetTurnContext("retry")
			if tc.State != store.TurnDone || tc.RetryCount != 3 {
				t.Fatalf("exhaustion: %+v", tc)
			}
		})
	}
	for _, exclusion := range []string{"terminal", "reply", "error", "success", "stop", "deadline", "silent", "busy"} {
		t.Run(exclusion, func(t *testing.T) {
			db, loop, _ := recLoop(t)
			if err := db.PutGroup(core.Group{Folder: "demo"}); err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			loop.runner = runnerFn(func(_ context.Context, req runedv1.RunRequest) (runedv1.RunOutcome, error) {
				out := runedv1.RunOutcome{Outcome: runedv1.OutcomeError}
				switch exclusion {
				case "terminal":
					out.Terminal = true
				case "reply":
					if err := db.PutMessage(core.Message{ID: "reply", ChatJID: req.ChatJID, Timestamp: now.Add(time.Second),
						Sender: "demo", Content: "answer", TurnID: req.TurnID, BotMsg: true}); err != nil {
						t.Fatal(err)
					}
				case "error", "success":
					if _, err := db.store().RecordTurnResult("demo", req.TurnID, "", exclusion, ""); err != nil {
						t.Fatal(err)
					}
				case "stop":
					return out, context.Canceled
				case "deadline":
					return out, context.DeadlineExceeded
				case "silent":
					out.Outcome = runedv1.OutcomeSilent
				case "busy":
					out.Busy = true
				}
				return out, nil
			})
			if err := db.PutMessage(core.Message{ID: "excluded", ChatJID: "web:demo", Sender: "user",
				Timestamp: now, Content: "question"}); err != nil {
				t.Fatal(err)
			}
			if _, err := loop.processGroupMessages("web:demo"); err != nil {
				t.Fatal(err)
			}
			tc, _ := db.GetTurnContext("excluded")
			if tc.RetryCount != 0 || tc.State == store.TurnPendingRetry {
				t.Fatalf("excluded outcome retried: %+v", tc)
			}
		})
	}
	for _, budget := range []struct{ configured, effective int }{{-1, 0}, {1, 1}, {0, 3}, {99, 3}} {
		t.Run(fmt.Sprint(budget.configured), func(t *testing.T) {
			loop := NewLoop(nil, nil, LoopConfig{MaxTurnRetry: budget.configured})
			loop.StopQueue()
			if loop.maxTurnRetry != budget.effective {
				t.Fatalf("effective retry budget=%d, want %d", loop.maxTurnRetry, budget.effective)
			}
		})
	}
}

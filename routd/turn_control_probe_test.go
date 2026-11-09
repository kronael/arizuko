package routd

import (
	"context"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/store"
)

func TestTurnControlProbeFence(t *testing.T) {
	for _, newerFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "one probe", true: "episode"}[newerFailure], func(t *testing.T) {
			db, loop, _, tc, now := deferredFixture(t)
			now = tc.NextAttemptAt
			loop.q.now = func() time.Time { return now }
			loop.runner = runnerFn(func(context.Context, runedv1.RunRequest) (runedv1.RunOutcome, error) {
				return runedv1.RunOutcome{Outcome: runedv1.OutcomeError}, nil
			})
			if _, err := loop.replay(tc.TurnID); err != nil {
				t.Fatal(err)
			}
			tc, _ = db.GetTurnContext(tc.TurnID)
			control, _, _ := db.store().ReadTurnControl(context.Background())
			if control.Phase != "open" || control.ProbeTurnID != tc.TurnID || !control.NextProbeAt.Equal(tc.NextAttemptAt) {
				t.Fatalf("failure owner: %+v", control)
			}
			if err := db.PutGroup(core.Group{Folder: "other"}); err != nil {
				t.Fatal(err)
			}
			_, _ = db.PutTurnContext("older", "other", "", "web:other", "user", "")
			older, _ := db.GetTurnContext("older")
			_, err := loop.q.complete(loop, older, runedv1.RunOutcome{Outcome: runedv1.OutcomeSilent}, nil, turnWork{})
			if err != nil {
				t.Fatal(err)
			}
			unchanged, _, _ := db.store().ReadTurnControl(context.Background())
			if unchanged != control {
				t.Fatalf("unrelated completion cleared outage: %+v", unchanged)
			}
			fresh := core.Message{ID: "fresh", ChatJID: "web:other", Sender: "user", Content: "question", Timestamp: now}
			if err := db.PutMessage(fresh); err != nil {
				t.Fatal(err)
			}
			launches := 0
			loop.runner = runnerFn(func(context.Context, runedv1.RunRequest) (runedv1.RunOutcome, error) {
				launches++
				return runedv1.RunOutcome{Outcome: runedv1.OutcomeSilent}, nil
			})
			launchFresh := func() {
				t.Helper()
				if _, _, _, _, err := loop.runTurn("other", "", fresh.ChatJID, fresh.ID, false, []core.Message{fresh}, nil); err != nil {
					t.Fatal(err)
				}
			}
			launchFresh()
			if launches != 0 {
				t.Fatal("global admission launched during cooldown")
			}
			now = tc.NextAttemptAt
			launchFresh()
			if launches != 0 {
				t.Fatal("fresh input replaced the failure owner")
			}
			loop.runner = runnerFn(func(context.Context, runedv1.RunRequest) (runedv1.RunOutcome, error) {
				launches++
				if _, err := db.store().RecordTurnResult(tc.Folder, tc.TurnID, "", "success", ""); err != nil {
					t.Fatal(err)
				}
				launchFresh()
				if launches != 1 {
					t.Fatal("more than one probe launched")
				}
				if newerFailure {
					_, err := loop.q.complete(loop, older, runedv1.RunOutcome{Outcome: runedv1.OutcomeError, Terminal: true}, nil, turnWork{})
					if err != nil {
						t.Fatal(err)
					}
				}
				return runedv1.RunOutcome{Outcome: runedv1.OutcomeSilent}, nil
			})
			if _, err := loop.replay(tc.TurnID); err != nil {
				t.Fatal(err)
			}
			current, _, _ := db.store().ReadTurnControl(context.Background())
			if newerFailure {
				if current.Phase != "open" || current.Episode == control.Episode || !current.NextProbeAt.Equal(control.NextProbeAt) {
					t.Fatalf("stale probe cleared newer episode or changed cooldown: %+v", current)
				}
			} else if current.Phase != "closed" {
				t.Fatalf("successful probe did not clear episode: %+v", current)
			}
		})
	}
	t.Run("hourly successors", func(t *testing.T) {
		db, loop, _, tc, _ := deferredFixture(t)
		now := tc.NextAttemptAt
		loop.q.now = func() time.Time { return now }
		loop.maxTurnRetry = 1
		launches := 0
		loop.runner = runnerFn(func(context.Context, runedv1.RunRequest) (runedv1.RunOutcome, error) {
			launches++
			return runedv1.RunOutcome{Outcome: runedv1.OutcomeError}, nil
		})
		for i := range 2 {
			if i > 0 {
				now = tc.NextAttemptAt
			}
			if _, err := loop.replay(tc.TurnID); err != nil {
				t.Fatal(err)
			}
			tc, _ = db.GetTurnContext(tc.TurnID)
		}
		control, _, _ := db.store().ReadTurnControl(context.Background())
		if tc.State != store.TurnDone || tc.RetryCount != 1 || control.Phase != "open" ||
			control.ProbeTurnID != "" || !control.NextProbeAt.Equal(now.Add(time.Hour)) {
			t.Fatalf("exhaustion: turn=%+v control=%+v", tc, control)
		}
		fresh := core.Message{ID: "successor", ChatJID: tc.ChatJID, Sender: "user", Content: "next", Timestamp: now.Add(time.Second)}
		if err := db.PutMessage(fresh); err != nil {
			t.Fatal(err)
		}
		if _, _, _, _, err := loop.runTurn(tc.Folder, "", tc.ChatJID, fresh.ID, false, []core.Message{fresh}, nil); err != nil || launches != 2 {
			t.Fatalf("early successor: launches=%d err=%v", launches, err)
		}
		now = control.NextProbeAt
		if _, _, _, _, err := loop.runTurn(tc.Folder, "", tc.ChatJID, fresh.ID, false, []core.Message{fresh}, nil); err != nil || launches != 3 {
			t.Fatalf("due successor: launches=%d err=%v", launches, err)
		}
		control, _, _ = db.store().ReadTurnControl(context.Background())
		successor, _ := db.GetTurnContext(fresh.ID)
		if control.NextProbeAt.Before(now.Add(time.Hour)) || successor.NextAttemptAt.Before(now.Add(time.Hour)) {
			t.Fatalf("successor shortened cooldown: turn=%+v control=%+v", successor, control)
		}
	})
}

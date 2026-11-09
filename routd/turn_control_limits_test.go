package routd

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/kronael/arizuko/core"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

func TestTurnControlLimits(t *testing.T) {
	for _, n := range []int{1, 2} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				q := newTurnControl(n)
				started := make(chan string, 4)
				release := make(chan struct{})
				defer close(release)
				q.SetProcessMessagesFn(func(jid string) (bool, error) {
					if !q.reserve(jid, jid, jid != "chat", jid) {
						return false, nil
					}
					started <- jid
					<-release
					return true, nil
				})
				q.EnqueueMessageCheck("cron1")
				if got := <-started; got != "cron1" {
					t.Fatal(got)
				}
				q.EnqueueMessageCheck("cron2")
				synctest.Wait()
				q.EnqueueMessageCheck("chat")
				synctest.Wait()
				if q.ActiveCount() != n {
					t.Fatalf("total limit: active=%d want=%d", q.ActiveCount(), n)
				}
				if n == 2 {
					if got := <-started; got != "chat" {
						t.Fatalf("cron limit blocked chat or admitted two cron: %s", got)
					}
				}
				select {
				case got := <-started:
					t.Fatalf("admitted extra execution: %s", got)
				default:
				}
				q.Shutdown()
			})
		})
	}
}

func TestTurnControlOccurrenceIsolation(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.PutGroup(core.Group{Folder: "cron"}); err != nil {
		t.Fatal(err)
	}
	var ids []string
	l := NewLoop(db, runnerFn(func(_ context.Context, req runedv1.RunRequest) (runedv1.RunOutcome, error) {
		ids = append(ids, req.TurnID)
		return runedv1.RunOutcome{Outcome: runedv1.OutcomeSilent}, nil
	}), LoopConfig{})
	l.StopQueue()
	for i := range 2 {
		if err := db.PutMessage(core.Message{ID: fmt.Sprint(i), ChatJID: "web:cron", Sender: "timed",
			Content: "scheduled", Timestamp: time.Now().Add(time.Duration(i) * time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := l.processGroupMessages("web:cron"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ids, []string{"0", "1"}) {
		t.Fatalf("scheduled occurrences combined: %v", ids)
	}
}

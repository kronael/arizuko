package routd

import (
	"slices"
	"testing"
	"testing/synctest"
)

// BUGS L920: one folder runs one turn per topic. Two PR chats routed to one
// folder with different topics run at once; a third chat on a running topic
// waits for it; the global cap still bounds the total.
func TestTurnControlAdmitsTopicsOfOneFolder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := newTurnControl(2)
		target := map[string][2]string{
			"pr1": {"repo", "pr-1"}, "pr2": {"repo", "pr-2"},
			"pr1-again": {"repo", "pr-1"}, "other": {"other", ""},
		}
		started := make(chan string, 4)
		release := make(chan struct{})
		q.SetProcessMessagesFn(func(jid string) (bool, error) {
			if !q.reserve(jid, target[jid][0], target[jid][1], false, jid) {
				return false, nil
			}
			started <- jid
			<-release
			return true, nil
		})
		q.EnqueueMessageCheck("pr1")
		q.EnqueueMessageCheck("pr2")
		synctest.Wait()
		if q.ActiveCount() != 2 {
			t.Fatalf("two topics of one folder: active=%d want 2", q.ActiveCount())
		}
		got := []string{<-started, <-started}
		slices.Sort(got)
		if !slices.Equal(got, []string{"pr1", "pr2"}) {
			t.Fatalf("started %v, want pr1 and pr2", got)
		}
		q.EnqueueMessageCheck("pr1-again")
		q.EnqueueMessageCheck("other")
		synctest.Wait()
		select {
		case jid := <-started:
			t.Fatalf("admitted %s beside two live turns (same topic or over the cap)", jid)
		default:
		}
		if q.ActiveCount() != 2 {
			t.Fatalf("global cap: active=%d want 2", q.ActiveCount())
		}
		q.Shutdown()
		close(release)
		synctest.Wait()
	})
}

// The same topic is never admitted twice, whatever the cap; a recovery that
// holds a topic excludes only that topic.
func TestTurnControlRefusesSecondTurnOfOneTopic(t *testing.T) {
	q := newTurnControl(5)
	if !q.reserve("chat-a", "repo", "pr-1", false, "t1") {
		t.Fatal("first turn of the topic refused")
	}
	if q.reserve("chat-b", "repo", "pr-1", false, "t2") {
		t.Fatal("second turn of one (folder, topic) admitted")
	}
	if !q.reserve("chat-c", "repo", "pr-2", false, "t3") {
		t.Fatal("a different topic of the busy folder refused")
	}
	ran, err := q.WithIdleTopic("repo", "pr-1", func() error { return nil })
	if err != nil || ran {
		t.Fatalf("recovery entered a running topic: ran=%v err=%v", ran, err)
	}
	ran, err = q.WithIdleTopic("repo", "pr-3", func() error {
		if q.canReserve(turnReservation{folder: "repo", topic: "pr-3"}) {
			t.Error("admission entered the topic a recovery holds")
		}
		if !q.canReserve(turnReservation{folder: "repo", topic: "pr-4"}) {
			t.Error("a recovery of one topic excluded another topic")
		}
		return nil
	})
	if err != nil || !ran {
		t.Fatalf("recovery of an idle topic refused: ran=%v err=%v", ran, err)
	}
	q.Shutdown()
}

package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

func TestOutboundDueRowsCannotHideBehindBackoff(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	now := time.Now()
	for i := range 201 {
		m := core.Message{ID: fmt.Sprint(i), ChatJID: "chat", Sender: "bot", Content: "owed", BotMsg: true, Timestamp: now.Add(-2*time.Hour + time.Duration(i)*time.Nanosecond)}
		if i == 200 {
			m.Timestamp = now.Add(-time.Minute)
		}
		if err := s.PutOutbound(t.Context(), m, core.MessageStatusPending); err != nil {
			t.Fatal(err)
		}
		if i < 200 {
			if _, _, err := s.RecordDeliveryFailure(t.Context(), m, 10); err != nil {
				t.Fatal(err)
			}
		}
	}
	c := &stateReadCounter{Store: s.obj}
	reader := NewOver(c)
	due, err := reader.PendingOutbound(t.Context(), now, 200)
	if err != nil || len(due) != 1 || due[0].ID != "200" || c.rows != 3 {
		t.Fatalf("due count=%d err=%v rows=%d", len(due), err, c.rows)
	}
	if err := s.MarkDelivered(t.Context(), due[0].Message, "pid"); err != nil {
		t.Fatal(err)
	}
	c.rows = 0
	if due, err := reader.PendingOutbound(t.Context(), now, 200); err != nil || len(due) != 0 || c.rows != 0 {
		t.Fatalf("acknowledged work: %v %v rows=%d", due, err, c.rows)
	}
}
func TestOutboundFailureSchedulesFromAttempt(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	now := time.Now()
	m := core.Message{ID: "old", ChatJID: "chat", Sender: "bot", Content: "owed", BotMsg: true, Timestamp: now.Add(-24 * time.Hour)}
	if err := s.PutOutbound(t.Context(), m, core.MessageStatusPending); err != nil {
		t.Fatal(err)
	}
	if n, dead, err := s.RecordDeliveryFailure(t.Context(), m, 10); err != nil || n != 1 || dead {
		t.Fatalf("failure=%d %v %v", n, dead, err)
	}
	if due, err := s.PendingOutbound(t.Context(), now.Add(59*time.Second), 10); err != nil || len(due) != 0 {
		t.Fatalf("early retry: %v %v", due, err)
	}
	due, err := s.PendingOutbound(t.Context(), now.Add(61*time.Second), 10)
	if err != nil || len(due) != 1 || due[0].NextAttemptAt.Before(now.Add(time.Minute)) {
		t.Fatalf("deadline lost: %v %v", due, err)
	}
}

type deliveryRace struct {
	objstore.Store
	other      *Store
	m          core.Message
	ack, fired bool
}

func (r *deliveryRace) GuardedBatch(ctx context.Context, checks []objstore.Check, writes []objstore.Write) (bool, error) {
	if !r.fired {
		r.fired = true
		if r.ack {
			if err := r.other.MarkDelivered(ctx, r.m, "accepted"); err != nil {
				return false, err
			}
		} else {
			if _, _, err := r.other.RecordDeliveryFailure(ctx, r.m, 10); err != nil {
				return false, err
			}
		}
	}
	return r.Store.GuardedBatch(ctx, checks, writes)
}
func TestOutboundFailureKeepsConcurrentTransitions(t *testing.T) {
	for _, ack := range []bool{false, true} {
		t.Run(fmt.Sprint(ack), func(t *testing.T) {
			s := openMem(t)
			defer s.Close()
			m := core.Message{ID: "race", ChatJID: "chat", Sender: "bot", Content: "owed", BotMsg: true, Timestamp: time.Now().Add(-time.Hour)}
			if err := s.PutOutbound(t.Context(), m, core.MessageStatusPending); err != nil {
				t.Fatal(err)
			}
			race := NewOver(&deliveryRace{Store: s.obj, other: s, m: m, ack: ack})
			maxAttempts := 10
			if ack {
				maxAttempts = 1
			}
			n, dead, err := race.RecordDeliveryFailure(t.Context(), m, maxAttempts)
			if err != nil || dead {
				t.Fatalf("concurrent transition lost: n=%d dead=%v err=%v", n, dead, err)
			}
			if !ack && n != 2 {
				t.Fatalf("failure count=%d, want 2", n)
			}
			if ack {
				if due, err := s.PendingOutbound(t.Context(), time.Now().Add(time.Hour), 10); err != nil || len(due) != 0 {
					t.Fatalf("delivery resurrected: %v %v", due, err)
				}
				if failed, err := s.DeadLetters(t.Context(), "", 10); err != nil || len(failed) != 0 {
					t.Fatalf("acknowledged delivery failed: %v %v", failed, err)
				}
			}
		})
	}
}

func TestStatusAcknowledgementClearsConcurrentFailure(t *testing.T) {
	s := openTurnEntities(t)
	m := core.Message{ID: "status", ChatJID: "chat", Verb: "status", BotMsg: true, Timestamp: time.Now()}
	if err := s.QueueStatus(t.Context(), m, "waiting", "waiting"); err != nil {
		t.Fatal(err)
	}
	m, _ = s.MessageByID(m.ChatJID, m.ID)
	p, ok, err := s.PendingDelivery(t.Context(), m)
	if err != nil || !ok {
		t.Fatalf("pending: %v %v", ok, err)
	}
	if err := s.FailStatus(t.Context(), p, false, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkDelivered(t.Context(), m, "accepted"); err != nil {
		t.Fatal(err)
	}
	if failed, err := s.DeadLetters(t.Context(), "", 10); err != nil || len(failed) != 0 {
		t.Fatalf("acknowledged status failed: %v %v", failed, err)
	}
}

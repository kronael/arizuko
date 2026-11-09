package store

import (
	"context"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

func TestStatusDeliveryRevision(t *testing.T) {
	s := openTurnEntities(t)
	ctx := t.Context()
	msg := core.Message{ID: "status", ChatJID: "web:demo", TurnID: "turn", Verb: "status", BotMsg: true, Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	queue := func(content, phase string) {
		t.Helper()
		if current, ok := s.MessageByID(msg.ChatJID, msg.ID); ok {
			msg = current
		}
		if err := s.QueueStatus(ctx, msg, content, phase); err != nil {
			t.Fatal(err)
		}
		msg, _ = s.MessageByID(msg.ChatJID, msg.ID)
	}
	pending := func() PendingMessage {
		t.Helper()
		rows, err := s.PendingOutbound(ctx, time.Now().Add(time.Hour), 10)
		if err != nil || len(rows) != 1 {
			t.Fatalf("pending=%+v err=%v", rows, err)
		}
		return rows[0]
	}
	queue("first failure", "waiting")
	first := pending()
	queue("second failure", "waiting")
	if got := pending(); got.ID != first.ID || got.Operation != "send" || got.Content != "second failure" {
		t.Fatalf("unsent revision did not coalesce: %+v", got)
	}
	if err := s.MarkDelivered(ctx, first.Message, "platform"); err != nil {
		t.Fatal(err)
	}
	edit := pending()
	if err := s.FailStatus(ctx, edit, false, 3); err != nil {
		t.Fatal(err)
	}
	if got := pending(); got.Attempts != 1 || got.Operation != "edit" {
		t.Fatalf("transient edit lost: %+v", got)
	}
	if err := s.FailStatus(ctx, pending(), false, 4); err != nil {
		t.Fatal(err)
	}
	if err := s.FailStatus(ctx, edit, false, 4); err != nil {
		t.Fatal(err)
	}
	if got := pending(); got.Attempts != 2 {
		t.Fatalf("stale failure reset attempts: %+v", got)
	}
	if edit.Operation != "edit" || edit.PlatformID != "platform" {
		t.Fatalf("stale send cleared newer revision: %+v", edit)
	}
	queue("finished", "cleared")
	if err := s.MarkDelivered(ctx, edit.Message, ""); err != nil {
		t.Fatal(err)
	}
	deletion := pending()
	if deletion.Operation != "delete" || deletion.PlatformID != "platform" {
		t.Fatalf("stale edit cleared delete: %+v", deletion)
	}
	if err := s.FailStatus(ctx, edit, true, 3); err != nil {
		t.Fatal(err)
	}
	if got := pending(); got.Operation != "delete" {
		t.Fatalf("stale failure replaced delete: %+v", got)
	}
	if err := s.FailStatus(ctx, deletion, true, 3); err != nil {
		t.Fatal(err)
	}
	dead, err := s.DeadLetters(ctx, "", 10)
	if err != nil || len(dead) != 1 || dead[0].Operation != "delete" {
		t.Fatalf("dead letters=%+v err=%v", dead, err)
	}
	if ok, err := s.ReplayDeadLetter(ctx, deletion.Message); err != nil || !ok {
		t.Fatalf("replay=%v %v", ok, err)
	}
	if got := pending(); got.Operation != "delete" {
		t.Fatalf("replay lost operation: %+v", got)
	}
	if err := s.MarkDelivered(ctx, deletion.Message, ""); err != nil {
		t.Fatal(err)
	}
	rows, err := s.PendingOutbound(ctx, time.Now().Add(time.Hour), 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("ack pending=%+v err=%v", rows, err)
	}
}

func TestStatusDeliveryReadRevision(t *testing.T) {
	s := openTurnEntities(t)
	msg := core.Message{ID: "status-read", ChatJID: "web:demo", Verb: "status", BotMsg: true, Timestamp: time.Now()}
	if err := s.QueueStatus(t.Context(), msg, "old", "waiting"); err != nil {
		t.Fatal(err)
	}
	msg, _ = s.MessageByID(msg.ChatJID, msg.ID)
	key, err := messageKeyOf(msg)
	if err != nil {
		t.Fatal(err)
	}
	old, ok, err := s.itemAt(t.Context(), MessageDelivery, deliveryKey(core.MessageStatusPending, key))
	if err != nil || !ok {
		t.Fatalf("pointer %v %v", ok, err)
	}
	if err := s.QueueStatus(t.Context(), msg, "new", "waiting"); err != nil {
		t.Fatal(err)
	}
	rows, err := s.pendingFrom(t.Context(), []objstore.Item{old})
	if err != nil || len(rows) != 0 {
		t.Fatalf("mixed delivery revisions: %+v %v", rows, err)
	}
}

type statusAckInterleave struct {
	objstore.Store
	msg   core.Message
	armed bool
}

func (s *statusAckInterleave) GuardedBatch(ctx context.Context, checks []objstore.Check, writes []objstore.Write) (bool, error) {
	if s.armed && len(writes) == 2 && writes[1].Entity == MessageDelivery && writes[1].Op == objstore.Delete {
		s.armed = false
		if err := NewOver(s.Store).QueueStatus(ctx, s.msg, "new", "waiting"); err != nil {
			return false, err
		}
	}
	return s.Store.GuardedBatch(ctx, checks, writes)
}

func TestStatusDeliveryAckCAS(t *testing.T) {
	s := openTurnEntities(t)
	msg := core.Message{ID: "ack-cas", ChatJID: "web:demo", Verb: "status", BotMsg: true, Timestamp: time.Now()}
	if err := s.QueueStatus(t.Context(), msg, "old", "waiting"); err != nil {
		t.Fatal(err)
	}
	msg, _ = s.MessageByID(msg.ChatJID, msg.ID)
	injected := NewOver(&statusAckInterleave{Store: s.Obj(), msg: msg, armed: true})
	if err := injected.MarkDelivered(t.Context(), msg, "platform"); err != nil {
		t.Fatal(err)
	}
	pending, err := s.PendingOutbound(t.Context(), time.Now().Add(time.Hour), 10)
	if err != nil || len(pending) != 1 || pending[0].Content != "new" || pending[0].Operation != "edit" {
		t.Fatalf("ack raced past revision: %+v %v", pending, err)
	}
}

type statusQueueInterleave struct {
	objstore.Store
	msg   core.Message
	armed bool
}

func (s *statusQueueInterleave) GuardedBatch(ctx context.Context, checks []objstore.Check, writes []objstore.Write) (bool, error) {
	if s.armed {
		for _, w := range writes {
			if w.Entity == Messages && w.Attrs["status_revision"] != "" {
				s.armed = false
				if err := NewOver(s.Store).MarkDelivered(ctx, s.msg, "platform"); err != nil {
					return false, err
				}
				break
			}
		}
	}
	return s.Store.GuardedBatch(ctx, checks, writes)
}
func TestStatusDeliveryQueueTracksAcknowledgedID(t *testing.T) {
	s := openTurnEntities(t)
	msg := core.Message{ID: "queue-cas", ChatJID: "web:demo", Verb: "status", BotMsg: true, Timestamp: time.Now()}
	if err := s.QueueStatus(t.Context(), msg, "old", "waiting"); err != nil {
		t.Fatal(err)
	}
	msg, _ = s.MessageByID(msg.ChatJID, msg.ID)
	injected := NewOver(&statusQueueInterleave{Store: s.Obj(), msg: msg, armed: true})
	if err := injected.QueueStatus(t.Context(), msg, "new", "waiting"); err == nil {
		t.Fatal("queued send after its platform acknowledgement")
	}
	msg, _ = s.MessageByID(msg.ChatJID, msg.ID)
	if err := s.QueueStatus(t.Context(), msg, "new", "waiting"); err != nil {
		t.Fatal(err)
	}
	rows, err := s.PendingOutbound(t.Context(), time.Now().Add(time.Hour), 10)
	if err != nil || len(rows) != 1 || rows[0].Operation != "edit" || rows[0].PlatformID != "platform" {
		t.Fatalf("acknowledged send did not become edit: %+v %v", rows, err)
	}
}

package store

import (
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
)

func TestStatusDeliveryRevision(t *testing.T) {
	s := openTurnEntities(t)
	ctx := t.Context()
	msg := core.Message{ID: "status", ChatJID: "web:demo", TurnID: "turn", Verb: "status", BotMsg: true, Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	queue := func(content, phase string) {
		t.Helper()
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

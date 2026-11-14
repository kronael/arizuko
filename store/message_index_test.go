package store

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

func TestMessageWorkReadsOnlyItsKeys(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	now := time.Now()
	for i := range 40 {
		m := core.Message{ID: fmt.Sprint(i), ChatJID: "chat", Timestamp: now.Add(time.Duration(i) * time.Second), Sender: "user"}
		if i == 0 {
			m.Source = "adapter"
		}
		if i == 1 {
			m.BotMsg, m.Verb, m.TurnID, m.StatusPhase = true, "status", "turn", "final"
		}
		if i == 2 {
			m.BotMsg, m.TurnID = true, "turn"
		}
		if err := s.PutMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	c := &stateReadCounter{Store: s.obj}
	r := NewOver(c)
	checks := map[string]func() bool{
		"identity": func() bool { m, ok := r.MessageByID("chat", "1"); return ok && m.ID == "1" },
		"status":   func() bool { m, ok := r.LastStatusRow("chat", "turn"); return ok && m.ID == "1" },
		"progress": func() bool { ms, err := r.ActiveProgress("chat", "", "turn"); return err == nil && len(ms) == 1 },
		"source":   func() bool { return r.LatestSource(t.Context(), "chat") == "adapter" },
		"reply":    func() bool { return r.TurnHasBotReply("chat", "turn") },
		"frames":   func() bool { ms, err := r.TurnFrames("chat", "turn", "", 10); return err == nil && len(ms) == 1 },
		"poll": func() bool {
			ms, err := r.BotRepliesSince("chat", now.Add(time.Second), 10)
			return err == nil && len(ms) == 1
		},
	}
	for name, check := range checks {
		t.Run(name, func(t *testing.T) {
			c.rows = 0
			if !check() || c.rows > 2 {
				t.Fatalf("%s read %d rows or returned wrong work", name, c.rows)
			}
		})
	}
}

func TestDeliveryUsesClampedMessageCoordinate(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	m := core.Message{ID: "z", ChatJID: "chat", Timestamp: time.Now().Add(-time.Hour), BotMsg: true}
	if err := s.PutMessage(m); err != nil {
		t.Fatal(err)
	}
	m.ID = "a"
	if err := s.PutOutbound(t.Context(), m, core.MessageStatusPending); err != nil {
		t.Fatal(err)
	}
	if n, dead, err := s.RecordDeliveryFailure(t.Context(), m, 10); err != nil || dead || n != 1 {
		t.Fatalf("clamped delivery lost: %d %v %v", n, dead, err)
	}
	if err := s.MarkDelivered(t.Context(), m, "accepted"); err != nil {
		t.Fatal(err)
	}
	if due, err := s.PendingOutbound(t.Context(), time.Now().Add(time.Hour), 10); err != nil || len(due) != 0 {
		t.Fatalf("clamped delivery not acknowledged: %v %v", due, err)
	}
	got, ok := s.MessageByID("chat", "accepted")
	if !ok || got.ID != m.ID {
		t.Fatalf("platform identity lost: %v", got)
	}
}

func TestMessageIndexMigrationPreservesLegacyRows(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	m := core.Message{ID: "legacy", ChatJID: "chat", Timestamp: time.Now(), BotMsg: true, PlatformID: "platform"}
	key, err := messageKeyOf(m)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := s.obj.GuardedBatch(t.Context(), nil, []objstore.Write{
		{Op: objstore.Put, Entity: Chats, Key: objstore.Key{Partition: OwnerRoutd, Sort: m.ChatJID}},
		{Op: objstore.Put, Entity: Messages, Key: key, Attrs: messageAttrs(m)},
	})
	if err != nil || !ok {
		t.Fatalf("seed legacy: %v %v", ok, err)
	}
	if err := s.MigrateStateIndexes(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{m.ID, m.PlatformID} {
		if got, ok := s.MessageByID(m.ChatJID, id); !ok || got.ID != m.ID {
			t.Fatalf("legacy identity %s lost: %v", id, got)
		}
	}
	c := &stateReadCounter{Store: s.obj}
	if err := NewOver(c).MigrateStateIndexes(t.Context()); err != nil || c.rows != 9 {
		t.Fatalf("repeat migration: %v rows=%d", err, c.rows)
	}
}

func TestMessageIndexKeepsKeyBudgetAndCursor(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	now := time.Now()
	for _, id := range []string{"a", "a1", strings.Repeat("x", 512)} {
		m := core.Message{ID: id, ChatJID: "chat", Timestamp: now, BotMsg: true, TurnID: "turn"}
		if ok, err := s.PutOutboundOnce(t.Context(), m); err != nil || !ok {
			t.Fatalf("append id length %d: %v %v", len(id), ok, err)
		}
	}
	rows, err := s.TurnFrames("chat", "turn", "a", 10)
	if err != nil || len(rows) != 2 || rows[0].ID != "a1" {
		t.Fatalf("frames after prefix ID: %v %v", rows, err)
	}
}

func TestMessageObservationRespectsIndexedBatchLimit(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	var messages []core.Message
	for i := range 49 {
		m, err := s.AppendMessage(core.Message{ID: fmt.Sprint(i), ChatJID: "chat", Timestamp: time.Now(), Sender: "user"})
		if err != nil {
			t.Fatal(err)
		}
		messages = append(messages, m)
	}
	if err := s.MarkMessagesObserved(t.Context(), "folder", messages); err != nil {
		t.Fatal(err)
	}
}

func TestMessageIndexKeepsPrefixCursor(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	now := time.Now()
	for _, id := range []string{"a", "a1"} {
		if _, err := s.PutOutboundOnce(t.Context(), core.Message{ID: id, ChatJID: "chat", Timestamp: now, BotMsg: true, TurnID: "turn"}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.TurnFrames("chat", "turn", "a", 10)
	if err != nil || len(rows) != 1 || rows[0].ID != "a1" {
		t.Fatalf("frames after prefix ID: %v %v", rows, err)
	}
}

func TestMessageIndexAcceptsFullWidthKeys(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	m := core.Message{ID: strings.Repeat("x", 993), ChatJID: strings.Repeat("c", 1000), Timestamp: time.Now(), BotMsg: true}
	if ok, err := s.PutOutboundOnce(t.Context(), m); err != nil || !ok {
		t.Fatalf("full-width message: %v %v", ok, err)
	}
	if got, ok := s.MessageByID(m.ChatJID, m.ID); !ok || got.ID != m.ID {
		t.Fatal("full-width identity missing")
	}
	if err := s.MigrateStateIndexes(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkDelivered(t.Context(), m, "delivered"); err != nil {
		t.Fatal(err)
	}
}

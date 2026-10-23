package store

import (
	"context"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

func TestAppendMessageLateRedelivery(t *testing.T) {
	// A redelivered late message keeps one row at its first stored instant.
	s := openMem(t)
	base := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	anchor := base.Add(time.Minute)
	if err := s.PutMessage(core.Message{
		ID: "anchor", ChatJID: "web:late", Timestamp: anchor,
	}); err != nil {
		t.Fatal(err)
	}
	original := core.Message{
		ID: "late", ChatJID: "web:late", Timestamp: base,
	}
	first, err := s.AppendMessage(original)
	if err != nil {
		t.Fatal(err)
	}
	if want := anchor.Add(time.Nanosecond); !first.Timestamp.Equal(want) {
		t.Fatalf("first stored instant = %s, want %s", first.Timestamp, want)
	}
	if err := s.PutMessage(core.Message{
		ID: "later", ChatJID: original.ChatJID,
		Timestamp: anchor.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.AppendMessage(original)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Timestamp.Equal(first.Timestamp) {
		t.Errorf("redelivery instant = %s, want %s", got.Timestamp, first.Timestamp)
	}
	rows, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: Messages, Partition: original.ChatJID, Project: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, row := range rows {
		if _, id, ok := MsgCoord(row.Key.Sort); ok && id == original.ID {
			count++
		}
	}
	if count != 1 {
		t.Errorf("rows for %s in %s = %d, want 1",
			original.ID, original.ChatJID, count)
	}
}

// BotRepliesSince returns only the agent's outbound rows after a cutoff — the
// inverse of MessagesSince. Used by `arizuko send` operator-direct mode to show
// the agent's reply to a CLI-injected message.
func TestBotRepliesSince(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	base := time.Now().Truncate(time.Second)

	mustPut := func(m core.Message) {
		if err := s.PutMessage(m); err != nil {
			t.Fatalf("put %s: %v", m.ID, err)
		}
	}
	// The rows are appended in the order they happened, because an append takes
	// the chat's next position and an out-of-order fixture would be re-stamped
	// onto the tail (store.appendInstant).
	// bot reply BEFORE the cutoff — excluded
	mustPut(core.Message{ID: "old", ChatJID: "web:krons", Sender: "bot", Content: "stale", Timestamp: base.Add(-time.Hour), BotMsg: true})
	// operator inbound (not a bot row) — must be excluded
	mustPut(core.Message{ID: "in", ChatJID: "web:krons", Sender: "operator", Content: "draft tweets", Timestamp: base, Verb: "mention"})
	// agent replies (bot rows) after the cutoff — included, oldest first
	mustPut(core.Message{ID: "b1", ChatJID: "web:krons", Sender: "bot", Content: "tweet 1", Timestamp: base.Add(2 * time.Second), BotMsg: true})
	mustPut(core.Message{ID: "b2", ChatJID: "web:krons", Sender: "bot", Content: "tweet 2", Timestamp: base.Add(3 * time.Second), BotMsg: true})
	// bot reply on a different jid — excluded
	mustPut(core.Message{ID: "other", ChatJID: "web:happy", Sender: "bot", Content: "elsewhere", Timestamp: base.Add(2 * time.Second), BotMsg: true})

	got, err := s.BotRepliesSince("web:krons", base, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("replies = %d, want 2 (%+v)", len(got), got)
	}
	if got[0].Content != "tweet 1" || got[1].Content != "tweet 2" {
		t.Errorf("order/content = %q, %q", got[0].Content, got[1].Content)
	}
}

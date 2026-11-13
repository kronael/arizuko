package store

import (
	"context"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

func putInput(t *testing.T, s *Store, jid, id string, at time.Time, bot bool) core.Message {
	t.Helper()
	m := core.Message{ID: id, ChatJID: jid, Sender: "u", Content: "q", Timestamp: at, BotMsg: bot}
	if err := s.PutMessage(m); err != nil {
		t.Fatal(err)
	}
	return m
}

func hasInputBefore(t *testing.T, s *Store, jid string, before core.Message) bool {
	t.Helper()
	cursors, err := s.InputCursorsBefore(context.Background(), MessageCursor(before))
	if err != nil {
		t.Fatal(err)
	}
	_, ok := cursors[jid]
	return ok
}

// A chat answers for its oldest waiting row, whatever arrived after it, and a
// row appended once the cursor has passed every waiting one starts the span
// again.
func TestInputSpanTracksTheOldestWaitingRow(t *testing.T) {
	s := openMem(t)
	now := time.Now().UTC()
	const jid = "web:a"
	putInput(t, s, jid, "first", now, false)
	putInput(t, s, jid, "reply", now.Add(time.Second), true)
	later := putInput(t, s, jid, "later", now.Add(3*time.Second), false)
	mid := core.Message{ID: "mid", ChatJID: "web:b", Timestamp: now.Add(2 * time.Second)}
	if !hasInputBefore(t, s, jid, mid) {
		t.Fatal("the waiting row before the bound was not answered")
	}
	if hasInputBefore(t, s, jid, core.Message{ID: "early", Timestamp: now.Add(-time.Second)}) {
		t.Fatal("a chat whose waiting rows all follow the bound was answered")
	}
	if err := s.SetAgentCursor(jid, MessageCursor(later)); err != nil {
		t.Fatal(err)
	}
	if hasInputBefore(t, s, jid, mid) {
		t.Fatal("a chat with every row consumed was answered")
	}
	putInput(t, s, jid, "next", now.Add(5*time.Second), false)
	if hasInputBefore(t, s, jid, core.Message{ID: "x", Timestamp: now.Add(4 * time.Second)}) {
		t.Fatal("the span did not restart past the cursor")
	}
}

// The migration writes the span of rows that predate it and runs once.
func TestMigrateChatInputSpan(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	now := time.Now().UTC()
	putInput(t, s, "web:waiting", "w1", now, false)
	putInput(t, s, "web:waiting", "w2", now.Add(time.Second), false)
	done := putInput(t, s, "web:done", "d1", now, false)
	if err := s.SetAgentCursor("web:done", MessageCursor(done)); err != nil {
		t.Fatal(err)
	}
	putInput(t, s, "web:bot", "b1", now, true)
	strip := func() {
		for _, jid := range []string{"web:waiting", "web:done", "web:bot"} {
			key, err := chatKey(jid)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.WriteEntities(ctx, nil, objstore.Write{Op: objstore.Update, Entity: Chats,
				Key: key, Remove: []string{attrChatInputFirst, attrChatInputLast}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	strip()
	bound := core.Message{ID: "z", Timestamp: now.Add(time.Second)}
	if err := s.MigrateChatInputSpan(ctx); err != nil {
		t.Fatal(err)
	}
	cursors, err := s.InputCursorsBefore(ctx, MessageCursor(bound))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cursors["web:waiting"]; !ok || len(cursors) != 1 {
		t.Fatalf("migrated chats holding input = %v, want web:waiting", cursors)
	}
	strip()
	if err := s.MigrateChatInputSpan(ctx); err != nil {
		t.Fatal(err)
	}
	if hasInputBefore(t, s, "web:waiting", bound) {
		t.Fatal("the migration ran a second time")
	}
}

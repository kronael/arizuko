package store

import (
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
)

// ---------------------------------------------------------------------------
// folderAncestors (pure)
// ---------------------------------------------------------------------------

func TestFolderAncestors(t *testing.T) {
	cases := []struct {
		folder string
		want   []string
	}{
		{"", []string{"root"}},
		{"root", []string{"root"}},
		{"atlas", []string{"atlas", "root"}},
		{"atlas/eng", []string{"atlas/eng", "atlas", "root"}},
		{"atlas/eng/sre", []string{"atlas/eng/sre", "atlas/eng", "atlas", "root"}},
	}
	for _, c := range cases {
		got := folderAncestors(c.folder)
		if len(got) != len(c.want) {
			t.Errorf("folderAncestors(%q) = %v, want %v", c.folder, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("folderAncestors(%q)[%d] = %q, want %q", c.folder, i, got[i], c.want[i])
			}
		}
	}
}

// ---------------------------------------------------------------------------
// MessageByID
// ---------------------------------------------------------------------------

func TestMessageByID_Found(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	now := time.Now()
	s.PutMessage(core.Message{
		ID: "m1", ChatJID: "tg:1", Sender: "alice",
		Content: "hi there", Timestamp: now, Topic: "t1",
	})
	msg, ok := s.MessageByID("m1")
	if !ok {
		t.Fatal("MessageByID: expected found")
	}
	if msg.Content != "hi there" || msg.Topic != "t1" {
		t.Errorf("MessageByID = %+v", msg)
	}
}

func TestMessageByID_Missing(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	_, ok := s.MessageByID("nope")
	if ok {
		t.Fatal("expected not found")
	}
}

func TestMessageByID_EmptyID(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	_, ok := s.MessageByID("")
	if ok {
		t.Fatal("empty id should return not found")
	}
}

// ---------------------------------------------------------------------------
// IsBotMessageByID
// ---------------------------------------------------------------------------

func TestIsBotMessageByID_BotMsg(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	s.PutMessage(core.Message{
		ID: "bot-1", ChatJID: "tg:1", Sender: "main",
		Content: "reply", Timestamp: time.Now(),
		FromMe: true, BotMsg: true,
	})
	if !s.IsBotMessageByID("bot-1") {
		t.Error("expected true for bot message")
	}
}

func TestIsBotMessageByID_UserMsg(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	s.PutMessage(core.Message{
		ID: "user-1", ChatJID: "tg:1", Sender: "alice",
		Content: "hi", Timestamp: time.Now(),
	})
	if s.IsBotMessageByID("user-1") {
		t.Error("expected false for user message")
	}
}

func TestIsBotMessageByID_ByPlatformID(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	s.PutMessage(core.Message{
		ID: "out-1", ChatJID: "tg:1", Sender: "main",
		Content: "reply", Timestamp: time.Now(),
		FromMe: true, BotMsg: true,
	})
	s.MarkMessageDelivered("out-1", "plt-99")
	if !s.IsBotMessageByID("plt-99") {
		t.Error("expected true when looking up by platform_id")
	}
}

func TestIsBotMessageByID_EmptyID(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	if s.IsBotMessageByID("") {
		t.Error("empty id should return false")
	}
}

// ---------------------------------------------------------------------------
// MarkMessagesObserved / ObservedSince
// ---------------------------------------------------------------------------

func TestMarkMessagesObserved_AndObservedSince(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	now := time.Now()
	msgs := []core.Message{
		{ID: "obs-1", ChatJID: "tg:1", Sender: "alice", Content: "first",
			Timestamp: now.Add(-3 * time.Minute), RoutedTo: "atlas"},
		{ID: "obs-2", ChatJID: "tg:1", Sender: "alice", Content: "second",
			Timestamp: now.Add(-2 * time.Minute), RoutedTo: "atlas"},
		{ID: "obs-3", ChatJID: "tg:1", Sender: "alice", Content: "third",
			Timestamp: now.Add(-1 * time.Minute), RoutedTo: "atlas"},
	}
	for _, m := range msgs {
		s.PutMessage(m)
	}

	if err := s.MarkMessagesObserved("atlas", []string{"obs-1", "obs-2"}); err != nil {
		t.Fatalf("MarkMessagesObserved: %v", err)
	}

	got := s.ObservedSince("atlas", "", 100, 100000)
	if len(got) != 2 {
		t.Fatalf("ObservedSince: expected 2, got %d: %v", len(got), got)
	}
	// Results are ascending
	if got[0].ID != "obs-1" || got[1].ID != "obs-2" {
		t.Errorf("order wrong: %v %v", got[0].ID, got[1].ID)
	}
}

func TestMarkMessagesObserved_Empty(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	if err := s.MarkMessagesObserved("atlas", nil); err != nil {
		t.Errorf("empty ids should be no-op, got: %v", err)
	}
}

func TestObservedSince_WithCursor(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	now := time.Now()
	for i, id := range []string{"c1", "c2", "c3"} {
		s.PutMessage(core.Message{
			ID: id, ChatJID: "tg:1", Sender: "u", Content: "x",
			Timestamp: now.Add(time.Duration(i) * time.Minute), RoutedTo: "atlas",
		})
	}
	s.MarkMessagesObserved("atlas", []string{"c1", "c2", "c3"})

	// cursor = timestamp of c1 → only c2, c3 should come back
	cursor := now.UTC().Format(time.RFC3339Nano)
	got := s.ObservedSince("atlas", cursor, 100, 100000)
	for _, m := range got {
		if m.ID == "c1" {
			t.Error("c1 should be filtered by cursor")
		}
	}
}

func TestObservedSince_ZeroLimitsReturnNil(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	got := s.ObservedSince("atlas", "", 0, 100)
	if got != nil {
		t.Errorf("zero maxMsgs: expected nil, got %v", got)
	}
	got = s.ObservedSince("atlas", "", 100, 0)
	if got != nil {
		t.Errorf("zero maxChars: expected nil, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// MessagesBefore
// ---------------------------------------------------------------------------

func TestMessagesBefore_OrderAscending(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	now := time.Now()
	for i, id := range []string{"b1", "b2", "b3"} {
		s.PutMessage(core.Message{
			ID: id, ChatJID: "tg:1", Sender: "alice",
			Content: "msg", Timestamp: now.Add(time.Duration(i) * time.Minute),
		})
	}

	got, err := s.MessagesBefore("tg:1", now.Add(10*time.Minute), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3, got %d", len(got))
	}
	// MessagesBefore returns ascending order
	if got[0].ID != "b1" || got[2].ID != "b3" {
		t.Errorf("order: %v %v %v", got[0].ID, got[1].ID, got[2].ID)
	}
}

func TestMessagesBefore_BeforeCutsOff(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	now := time.Now()
	s.PutMessage(core.Message{
		ID: "old", ChatJID: "tg:1", Sender: "alice",
		Content: "old", Timestamp: now.Add(-2 * time.Minute),
	})
	s.PutMessage(core.Message{
		ID: "recent", ChatJID: "tg:1", Sender: "alice",
		Content: "recent", Timestamp: now,
	})

	got, err := s.MessagesBefore("tg:1", now.Add(-time.Minute), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "old" {
		t.Errorf("expected only 'old', got %+v", got)
	}
}

// ---------------------------------------------------------------------------
// LatestSource
// ---------------------------------------------------------------------------

func TestLatestSource(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	now := time.Now()
	s.PutMessage(core.Message{
		ID: "s1", ChatJID: "tg:1", Sender: "alice",
		Content: "hi", Timestamp: now, Source: "telegram",
	})
	s.PutMessage(core.Message{
		ID: "s2", ChatJID: "tg:1", Sender: "alice",
		Content: "bye", Timestamp: now.Add(time.Second), Source: "telegram",
	})

	got := s.LatestSource("tg:1")
	if got != "telegram" {
		t.Errorf("LatestSource = %q, want telegram", got)
	}
}

func TestLatestSource_MissingJID(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	if got := s.LatestSource("nope"); got != "" {
		t.Errorf("missing jid: got %q, want empty", got)
	}
}

// ---------------------------------------------------------------------------
// Topics / MessagesSinceTopic
// ---------------------------------------------------------------------------

func TestTopics_GroupByTopic(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	now := time.Now()
	rows := []core.Message{
		{ID: "w1", ChatJID: "web:main", Sender: "alice", Content: "first", Timestamp: now, Topic: "t1"},
		{ID: "w2", ChatJID: "web:main", Sender: "alice", Content: "second", Timestamp: now.Add(time.Minute), Topic: "t1"},
		{ID: "w3", ChatJID: "web:main", Sender: "alice", Content: "other", Timestamp: now.Add(2 * time.Minute), Topic: "t2"},
	}
	for _, m := range rows {
		s.PutMessage(m)
	}

	topics, err := s.Topics("main")
	if err != nil {
		t.Fatal(err)
	}
	if len(topics) != 2 {
		t.Fatalf("expected 2 topics, got %d", len(topics))
	}
	// t1 should have 2 messages
	var t1cnt int
	for _, tp := range topics {
		if tp.ID == "t1" {
			t1cnt = tp.MessageCount
		}
	}
	if t1cnt != 2 {
		t.Errorf("t1 message count = %d, want 2", t1cnt)
	}
}

func TestMessagesSinceTopic(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	now := time.Now()
	for i, id := range []string{"mst1", "mst2", "mst3"} {
		s.PutMessage(core.Message{
			ID: id, ChatJID: "web:main", Sender: "alice", Content: "x",
			Timestamp: now.Add(time.Duration(i) * time.Minute), Topic: "tp1",
		})
	}

	// After first message
	got, err := s.MessagesSinceTopic("main", "tp1", now.Add(30*time.Second), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2, got %d", len(got))
	}
	if got[0].ID != "mst2" {
		t.Errorf("first = %q, want mst2", got[0].ID)
	}
}

// ---------------------------------------------------------------------------
// RoutedToByMessageID
// ---------------------------------------------------------------------------

func TestRoutedToByMessageID(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	now := time.Now()
	s.PutMessage(core.Message{
		ID: "rt1", ChatJID: "tg:1", Sender: "alice",
		Content: "hi", Timestamp: now, RoutedTo: "atlas",
	})

	if got := s.RoutedToByMessageID("rt1"); got != "atlas" {
		t.Errorf("RoutedToByMessageID = %q, want atlas", got)
	}
	if got := s.RoutedToByMessageID("missing"); got != "" {
		t.Errorf("missing id: got %q, want empty", got)
	}
}

// ---------------------------------------------------------------------------
// HasPendingMessages
// ---------------------------------------------------------------------------

func TestHasPendingMessages(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	jid := "tg:pending"
	if s.HasPendingMessages(jid, "Bot") {
		t.Fatal("empty chat should have no pending messages")
	}

	now := time.Now()
	s.PutMessage(core.Message{
		ID: "pm1", ChatJID: jid, Sender: "alice",
		Content: "hello", Timestamp: now,
	})
	if !s.HasPendingMessages(jid, "Bot") {
		t.Fatal("should have pending messages after PutMessage")
	}

	// Bot messages don't count
	s.SetAgentCursor(jid, now.Add(time.Second))
	if s.HasPendingMessages(jid, "Bot") {
		t.Fatal("advancing cursor should clear pending")
	}
}

// ---------------------------------------------------------------------------
// SetLastReply — empty replyID preserves existing value
// ---------------------------------------------------------------------------

func TestSetLastReply_EmptyReplyIDPreservesExisting(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	if err := s.SetLastReply("jid1", "", "rid-1", "folderA"); err != nil {
		t.Fatal(err)
	}
	// Second call with empty replyID must not overwrite rid-1
	if err := s.SetLastReply("jid1", "", "", "folderA"); err != nil {
		t.Fatal(err)
	}
	if got := s.GetLastReplyID("jid1", ""); got != "rid-1" {
		t.Errorf("last_reply_id = %q, want rid-1 (preserved)", got)
	}
}

// ---------------------------------------------------------------------------
// MarkMessagesErrored / DeleteErroredMessages
// ---------------------------------------------------------------------------

func TestMarkMessagesErrored_AndDelete(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	now := time.Now()
	for _, id := range []string{"e1", "e2", "e3"} {
		s.PutMessage(core.Message{
			ID: id, ChatJID: "tg:1", Sender: "alice",
			Content: "x", Timestamp: now,
		})
	}

	if err := s.MarkMessagesErrored([]string{"e1", "e2"}); err != nil {
		t.Fatalf("MarkMessagesErrored: %v", err)
	}

	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE errored=1`).Scan(&n)
	if n != 2 {
		t.Errorf("errored count = %d, want 2", n)
	}

	if err := s.DeleteErroredMessages("tg:1"); err != nil {
		t.Fatalf("DeleteErroredMessages: %v", err)
	}
	s.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE errored=1`).Scan(&n)
	if n != 0 {
		t.Errorf("errored remaining = %d, want 0", n)
	}
	// e3 untouched
	s.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE id='e3'`).Scan(&n)
	if n != 1 {
		t.Error("e3 should not have been deleted")
	}
}

func TestMarkMessagesErrored_EmptyList(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	if err := s.MarkMessagesErrored(nil); err != nil {
		t.Errorf("empty list should be no-op: %v", err)
	}
}

package store

import (
	"context"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
)

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

func TestMessageByID_Found(t *testing.T) {
	s := openMem(t)
	defer s.Close()

	now := time.Now()
	s.PutMessage(core.Message{
		ID: "m1", ChatJID: "tg:1", Sender: "alice",
		Content: "hi there", Timestamp: now, Topic: "t1",
	})
	msg, ok := s.MessageByID("tg:1", "m1")
	if !ok {
		t.Fatal("MessageByID: expected found")
	}
	if msg.Content != "hi there" || msg.Topic != "t1" {
		t.Errorf("MessageByID = %+v", msg)
	}
}

func TestMessageByID_Missing(t *testing.T) {
	s := openMem(t)
	defer s.Close()

	_, ok := s.MessageByID("tg:1", "nope")
	if ok {
		t.Fatal("expected not found")
	}
}

func TestMessageByID_EmptyID(t *testing.T) {
	s := openMem(t)
	defer s.Close()

	_, ok := s.MessageByID("tg:1", "")
	if ok {
		t.Fatal("empty id should return not found")
	}
	if _, ok := s.MessageByID("", "m1"); ok {
		t.Fatal("a lookup with no chat names no partition, so it finds nothing")
	}
}

func TestMarkMessagesObserved_AndObservedSince(t *testing.T) {
	s := openMem(t)
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

	ctx := context.Background()
	if err := s.MarkMessagesObserved(ctx, "atlas", msgs[:2]); err != nil {
		t.Fatalf("MarkMessagesObserved: %v", err)
	}

	got, err := s.ObservedSince(ctx, "atlas", "", 100, 100000, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("ObservedSince: expected 2, got %d: %v", len(got), got)
	}
	// Results are ascending
	if got[0].ID != "obs-1" || got[1].ID != "obs-2" {
		t.Errorf("order wrong: %v %v", got[0].ID, got[1].ID)
	}
}

func TestMarkMessagesObserved_Empty(t *testing.T) {
	s := openMem(t)
	defer s.Close()

	if err := s.MarkMessagesObserved(context.Background(), "atlas", nil); err != nil {
		t.Errorf("empty ids should be no-op, got: %v", err)
	}
}

func TestObservedSince_WithCursor(t *testing.T) {
	s := openMem(t)
	defer s.Close()

	ctx := context.Background()
	now := time.Now()
	var msgs []core.Message
	for i, id := range []string{"c1", "c2", "c3"} {
		m := core.Message{
			ID: id, ChatJID: "tg:1", Sender: "u", Content: "x",
			Timestamp: now.Add(time.Duration(i) * time.Minute), RoutedTo: "atlas",
		}
		msgs = append(msgs, m)
		s.PutMessage(m)
	}
	if err := s.MarkMessagesObserved(ctx, "atlas", msgs); err != nil {
		t.Fatal(err)
	}

	// cursor = timestamp of c1 → only c2, c3 should come back
	cursor := core.TS(now)
	got, err := s.ObservedSince(ctx, "atlas", cursor, 100, 100000, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected c2 and c3, got %d", len(got))
	}
	for _, m := range got {
		if m.ID == "c1" {
			t.Error("c1 should be filtered by cursor")
		}
	}
}

func TestObservedSince_ZeroLimitsReturnNil(t *testing.T) {
	s := openMem(t)
	defer s.Close()

	ctx := context.Background()
	got, err := s.ObservedSince(ctx, "atlas", "", 0, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("zero maxMsgs: expected nil, got %v", got)
	}
	got, err = s.ObservedSince(ctx, "atlas", "", 100, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("zero maxChars: expected nil, got %v", got)
	}
}

func TestMessagesBefore_OrderAscending(t *testing.T) {
	s := openMem(t)
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
	s := openMem(t)
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

func TestTopics_GroupByTopic(t *testing.T) {
	s := openMem(t)
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
	s := openMem(t)
	defer s.Close()

	now := time.Now()
	for i, id := range []string{"mst1", "mst2", "mst3"} {
		s.PutMessage(core.Message{
			ID: id, ChatJID: "web:main", Sender: "alice", Content: "x",
			Timestamp: now.Add(time.Duration(i) * time.Minute), Topic: "tp1",
		})
	}

	// After first message — keyed by the chat JID verbatim (BUGS L30).
	got, err := s.MessagesSinceTopic("web:main", "tp1", now.Add(30*time.Second), 10)
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

func TestRoutedToByMessageID(t *testing.T) {
	s := openMem(t)
	defer s.Close()

	now := time.Now()
	s.PutMessage(core.Message{
		ID: "rt1", ChatJID: "tg:1", Sender: "alice",
		Content: "hi", Timestamp: now, RoutedTo: "atlas",
	})

	if got := s.RoutedToByMessageID("tg:1", "rt1"); got != "atlas" {
		t.Errorf("RoutedToByMessageID = %q, want atlas", got)
	}
	if got := s.RoutedToByMessageID("tg:1", "missing"); got != "" {
		t.Errorf("missing id: got %q, want empty", got)
	}
}

func TestSetLastReply_EmptyReplyIDPreservesExisting(t *testing.T) {
	s := openMem(t)
	defer s.Close()

	if err := s.SetLastReply("jid1", "", "rid-1", "folderA"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLastReply("jid1", "", "", "folderA"); err != nil {
		t.Fatal(err)
	}
	if got := s.LastReplyID("jid1", ""); got != "rid-1" {
		t.Errorf("last_reply_id = %q, want rid-1 (preserved)", got)
	}
}

func TestMarkMessagesErrored_AndDelete(t *testing.T) {
	s := openMem(t)
	defer s.Close()

	ctx := context.Background()
	now := time.Now()
	var msgs []core.Message
	for i, id := range []string{"e1", "e2", "e3"} {
		m := core.Message{
			ID: id, ChatJID: "tg:1", Sender: "alice",
			Content: "x", Timestamp: now.Add(time.Duration(i) * time.Second),
		}
		msgs = append(msgs, m)
		s.PutMessage(m)
	}

	if err := s.MarkMessagesErrored(ctx, msgs[:2]); err != nil {
		t.Fatalf("MarkMessagesErrored: %v", err)
	}

	items, err := s.erroredItems(ctx, "tg:1")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Errorf("errored count = %d, want 2", len(items))
	}

	gone, err := s.DeleteErroredMessages(ctx, "tg:1")
	if err != nil {
		t.Fatalf("DeleteErroredMessages: %v", err)
	}
	// The positions it answers with are what a derived index is told to drop, so
	// a delete that reported none would leave the search index holding them.
	if len(gone) != 2 {
		t.Errorf("deleted positions = %d, want the 2 rows it dropped", len(gone))
	}
	items, err = s.erroredItems(ctx, "tg:1")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Errorf("errored remaining = %d, want 0", len(items))
	}
	if _, ok := s.MessageByID("tg:1", "e3"); !ok {
		t.Error("e3 should not have been deleted")
	}
}

func TestMarkMessagesErrored_EmptyList(t *testing.T) {
	s := openMem(t)
	defer s.Close()

	if err := s.MarkMessagesErrored(context.Background(), nil); err != nil {
		t.Errorf("empty list should be no-op: %v", err)
	}
}

package store

import (
	"context"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
)

func TestMessageQueries_WithRoutedTo(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	msgs := []core.Message{
		{
			ID:        "msg-1",
			ChatJID:   "tg:123",
			Sender:    "user1",
			Name:      "User One",
			Content:   "first message",
			Timestamp: time.Now().Add(-2 * time.Minute),
			RoutedTo:  "group-a",
		},
		{
			ID:        "msg-2",
			ChatJID:   "tg:123",
			Sender:    "bot",
			Name:      "Bot",
			Content:   "bot reply",
			Timestamp: time.Now().Add(-1 * time.Minute),
			BotMsg:    true,
			RoutedTo:  "group-b",
		},
		{
			ID:        "msg-3",
			ChatJID:   "tg:123",
			Sender:    "user1",
			Name:      "User One",
			Content:   "third message",
			Timestamp: time.Now(),
			RoutedTo:  "",
		},
	}

	for _, m := range msgs {
		if err := s.PutMessage(m); err != nil {
			t.Fatal(err)
		}
	}

	retrieved, err := s.MessagesSince("tg:123", time.Now().Add(-3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}

	// The chat's whole window comes back, the agent's own row included: the
	// read is the chat and the time, and nothing else.
	if len(retrieved) != 3 {
		t.Fatalf("got %d messages, want 3", len(retrieved))
	}

	if retrieved[0].RoutedTo != "group-a" {
		t.Errorf("msg[0].RoutedTo = %q, want %q", retrieved[0].RoutedTo, "group-a")
	}

	if retrieved[1].RoutedTo != "group-b" || !retrieved[1].BotMsg {
		t.Errorf("msg[1] = %+v, want the bot row routed to group-b", retrieved[1])
	}

	if retrieved[2].RoutedTo != "" {
		t.Errorf("msg[2].RoutedTo = %q, want empty", retrieved[2].RoutedTo)
	}
}

func TestMessageQueries_WithTopic(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	msgs := []core.Message{
		{
			ID:        "msg-1",
			ChatJID:   "tg:123",
			Sender:    "user1",
			Name:      "User One",
			Content:   "in thread 1",
			Timestamp: time.Now().Add(-2 * time.Minute),
			Topic:     "thread-1",
		},
		{
			ID:        "msg-2",
			ChatJID:   "tg:123",
			Sender:    "user2",
			Name:      "User Two",
			Content:   "in thread 2",
			Timestamp: time.Now().Add(-1 * time.Minute),
			Topic:     "thread-2",
		},
		{
			ID:        "msg-3",
			ChatJID:   "tg:123",
			Sender:    "user1",
			Name:      "User One",
			Content:   "no thread",
			Timestamp: time.Now(),
			Topic:     "",
		},
	}

	for _, m := range msgs {
		if err := s.PutMessage(m); err != nil {
			t.Fatal(err)
		}
	}

	retrieved, err := s.MessagesSince("tg:123", time.Now().Add(-3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}

	if len(retrieved) != 3 {
		t.Fatalf("got %d messages, want 3", len(retrieved))
	}

	if retrieved[0].Topic != "thread-1" {
		t.Errorf("msg[0].Topic = %q, want %q", retrieved[0].Topic, "thread-1")
	}

	if retrieved[1].Topic != "thread-2" {
		t.Errorf("msg[1].Topic = %q, want %q", retrieved[1].Topic, "thread-2")
	}

	if retrieved[2].Topic != "" {
		t.Errorf("msg[2].Topic = %q, want empty", retrieved[2].Topic)
	}
}

func TestTopicByMessageID(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	msg := core.Message{
		ID:        "msg-1",
		ChatJID:   "tg:123",
		Sender:    "user1",
		Content:   "test",
		Timestamp: time.Now(),
		Topic:     "forum-topic-42",
	}

	if err := s.PutMessage(msg); err != nil {
		t.Fatal(err)
	}

	topic := s.TopicByMessageID("msg-1", "tg:123")
	if topic != "forum-topic-42" {
		t.Errorf("TopicByMessageID = %q, want %q", topic, "forum-topic-42")
	}
}

func TestTopicByMessageID_NoTopic(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	msg := core.Message{
		ID:        "msg-1",
		ChatJID:   "tg:123",
		Sender:    "user1",
		Content:   "test",
		Timestamp: time.Now(),
		Topic:     "",
	}

	if err := s.PutMessage(msg); err != nil {
		t.Fatal(err)
	}

	topic := s.TopicByMessageID("msg-1", "tg:123")
	if topic != "" {
		t.Errorf("TopicByMessageID = %q, want empty", topic)
	}
}

func TestTopicByMessageID_NotFound(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	topic := s.TopicByMessageID("non-existent", "tg:123")
	if topic != "" {
		t.Errorf("TopicByMessageID = %q, want empty for non-existent", topic)
	}
}

func TestRoutedToByMessageID_Found(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	msg := core.Message{
		ID:        "msg-1",
		ChatJID:   "tg:123",
		Sender:    "bot",
		Content:   "test",
		Timestamp: time.Now(),
		BotMsg:    true,
		RoutedTo:  "target-folder",
	}

	if err := s.PutMessage(msg); err != nil {
		t.Fatal(err)
	}

	routedTo := s.RoutedToByMessageID("tg:123", "msg-1")
	if routedTo != "target-folder" {
		t.Errorf("RoutedToByMessageID = %q, want %q", routedTo, "target-folder")
	}
}

func TestRoutedToByMessageID_Empty(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	msg := core.Message{
		ID:        "msg-1",
		ChatJID:   "tg:123",
		Sender:    "user1",
		Content:   "test",
		Timestamp: time.Now(),
		RoutedTo:  "",
	}

	if err := s.PutMessage(msg); err != nil {
		t.Fatal(err)
	}

	routedTo := s.RoutedToByMessageID("tg:123", "msg-1")
	if routedTo != "" {
		t.Errorf("RoutedToByMessageID = %q, want empty", routedTo)
	}
}

func TestRoutedToByMessageID_NotFound(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	routedTo := s.RoutedToByMessageID("tg:123", "non-existent")
	if routedTo != "" {
		t.Errorf("RoutedToByMessageID = %q, want empty for non-existent", routedTo)
	}
	// The chat bounds the lookup: a message in another chat is another
	// partition and resolving one there is the cross-folder read the bound
	// exists to make impossible.
	if err := s.PutMessage(core.Message{
		ID: "elsewhere", ChatJID: "tg:456", Sender: "user1", Content: "test",
		Timestamp: time.Now(), RoutedTo: "other-folder",
	}); err != nil {
		t.Fatal(err)
	}
	if got := s.RoutedToByMessageID("tg:123", "elsewhere"); got != "" {
		t.Errorf("RoutedToByMessageID(tg:123, elsewhere) = %q, want empty", got)
	}
}

func TestNewMessagesForChat_PreservesRoutedToAndTopic(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	msgs := []core.Message{
		{
			ID:        "msg-1",
			ChatJID:   "tg:123",
			Sender:    "user1",
			Content:   "first",
			Timestamp: time.Now().Add(-2 * time.Minute),
			Topic:     "thread-1",
			RoutedTo:  "group-a",
		},
		{
			ID:        "msg-2",
			ChatJID:   "tg:456",
			Sender:    "user2",
			Content:   "second",
			Timestamp: time.Now().Add(-1 * time.Minute),
			Topic:     "thread-2",
			RoutedTo:  "group-b",
		},
	}

	for _, m := range msgs {
		if err := s.PutMessage(m); err != nil {
			t.Fatal(err)
		}
	}

	// The feed is per chat now, so the poll loop walks the partition list and
	// reads each chat from its own watermark.
	ctx := context.Background()
	jids, err := s.ChatJIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var retrieved []core.Message
	for _, jid := range jids {
		got, err := s.NewMessagesForChat(ctx, jid, "", 10)
		if err != nil {
			t.Fatal(err)
		}
		retrieved = append(retrieved, got...)
	}

	if len(retrieved) != 2 {
		t.Fatalf("got %d messages, want 2", len(retrieved))
	}

	if retrieved[0].Topic != "thread-1" {
		t.Errorf("msg[0].Topic = %q, want %q", retrieved[0].Topic, "thread-1")
	}
	if retrieved[0].RoutedTo != "group-a" {
		t.Errorf("msg[0].RoutedTo = %q, want %q", retrieved[0].RoutedTo, "group-a")
	}

	if retrieved[1].Topic != "thread-2" {
		t.Errorf("msg[1].Topic = %q, want %q", retrieved[1].Topic, "thread-2")
	}
	if retrieved[1].RoutedTo != "group-b" {
		t.Errorf("msg[1].RoutedTo = %q, want %q", retrieved[1].RoutedTo, "group-b")
	}
}

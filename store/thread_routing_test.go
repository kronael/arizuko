package store

import (
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

	// Insert messages with routed_to
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

	// Query should preserve routed_to
	retrieved, err := s.MessagesSince("tg:123", time.Now().Add(-3*time.Minute), "bot")
	if err != nil {
		t.Fatal(err)
	}

	// Should get non-bot messages
	if len(retrieved) != 2 {
		t.Fatalf("got %d messages, want 2", len(retrieved))
	}

	if retrieved[0].RoutedTo != "group-a" {
		t.Errorf("msg[0].RoutedTo = %q, want %q", retrieved[0].RoutedTo, "group-a")
	}

	if retrieved[1].RoutedTo != "" {
		t.Errorf("msg[1].RoutedTo = %q, want empty", retrieved[1].RoutedTo)
	}
}

func TestMessageQueries_WithTopic(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Insert messages with topics
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

	// Query should preserve topic
	retrieved, err := s.MessagesSince("tg:123", time.Now().Add(-3*time.Minute), "bot")
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

	routedTo := s.RoutedToByMessageID("msg-1")
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

	routedTo := s.RoutedToByMessageID("msg-1")
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

	routedTo := s.RoutedToByMessageID("non-existent")
	if routedTo != "" {
		t.Errorf("RoutedToByMessageID = %q, want empty for non-existent", routedTo)
	}
}

func TestNewMessages_PreservesRoutedToAndTopic(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Insert messages with both routed_to and topic
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

	// Query using NewMessages (multi-JID query)
	jids := []string{"tg:123", "tg:456"}
	retrieved, _, err := s.NewMessages(jids, time.Now().Add(-3*time.Minute), "bot")
	if err != nil {
		t.Fatal(err)
	}

	if len(retrieved) != 2 {
		t.Fatalf("got %d messages, want 2", len(retrieved))
	}

	// Check first message
	if retrieved[0].Topic != "thread-1" {
		t.Errorf("msg[0].Topic = %q, want %q", retrieved[0].Topic, "thread-1")
	}
	if retrieved[0].RoutedTo != "group-a" {
		t.Errorf("msg[0].RoutedTo = %q, want %q", retrieved[0].RoutedTo, "group-a")
	}

	// Check second message
	if retrieved[1].Topic != "thread-2" {
		t.Errorf("msg[1].Topic = %q, want %q", retrieved[1].Topic, "thread-2")
	}
	if retrieved[1].RoutedTo != "group-b" {
		t.Errorf("msg[1].RoutedTo = %q, want %q", retrieved[1].RoutedTo, "group-b")
	}
}

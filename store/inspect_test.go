package store

import (
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
)

func TestErroredChats_ScopedByFolder(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	now := time.Now()
	// two chats, one routed to world/a, one to world/b
	msgs := []core.Message{
		{ID: "a1", ChatJID: "tg:1", Sender: "u", Content: "x", Timestamp: now, RoutedTo: "world/a"},
		{ID: "a2", ChatJID: "tg:1", Sender: "u", Content: "y", Timestamp: now.Add(time.Second), RoutedTo: "world/a"},
		{ID: "b1", ChatJID: "tg:2", Sender: "u", Content: "z", Timestamp: now, RoutedTo: "world/b"},
	}
	for _, m := range msgs {
		if err := s.PutMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.MarkMessagesErrored([]string{"a1", "a2", "b1"}); err != nil {
		t.Fatal(err)
	}

	// root sees both chats
	all := s.ErroredChats("", true)
	if len(all) != 2 {
		t.Errorf("root: want 2 chats, got %d", len(all))
	}

	// world/a sees only its own chat
	scoped := s.ErroredChats("world/a", false)
	if len(scoped) != 1 || scoped[0].ChatJID != "tg:1" {
		t.Errorf("scoped world/a: want [tg:1], got %+v", scoped)
	}
	if scoped[0].Count != 2 {
		t.Errorf("want count=2, got %d", scoped[0].Count)
	}

	// world/b sees only its chat
	if b := s.ErroredChats("world/b", false); len(b) != 1 || b[0].ChatJID != "tg:2" {
		t.Errorf("scoped world/b: want [tg:2], got %+v", b)
	}
}

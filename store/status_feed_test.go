package store

import (
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
)

func TestStatusExcludedFromDispatchFeed(t *testing.T) {
	s := openTurnEntities(t)
	now := time.Date(2026, 11, 9, 3, 0, 0, 0, time.UTC)
	for _, msg := range []core.Message{
		{ID: "input", ChatJID: "web:demo", Content: "question", Timestamp: now},
		{ID: "status", ChatJID: "web:demo", Verb: "status", BotMsg: true, Content: "waiting", Timestamp: now.Add(time.Second)},
	} {
		if err := s.PutMessage(msg); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.NewMessagesForChat(t.Context(), "web:demo", "", 10)
	if err != nil || len(rows) != 1 || rows[0].ID != "input" {
		t.Fatalf("status entered dispatch feed: %+v %v", rows, err)
	}
}

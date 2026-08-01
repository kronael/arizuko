package store

import (
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
)

// A turn id is guessable and is not proof of ownership: TurnFrames must bind
// the turn to its chat. The HTTP face proved this separately (webd/turn.go
// authorizeTurn), the MCP face did not, so the same query served both a
// contained and an uncontained caller. Containment belongs in the query.
func TestTurnFrames_DoesNotCrossChats(t *testing.T) {
	s := openMem(t)

	put := func(id, jid, turnID string) {
		t.Helper()
		if err := s.PutMessage(core.Message{
			ID: id, ChatJID: jid, Sender: "bot", Name: "bot",
			Content: "frame " + id, Timestamp: time.Now(), TurnID: turnID,
			BotMsg: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	put("m-a", "web:tenant-a", "turn-a")
	put("m-b", "web:tenant-b", "turn-b")

	// Own turn: visible.
	own, err := s.TurnFrames("web:tenant-a", "turn-a", "", 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(own) != 1 {
		t.Fatalf("own turn returned %d frames, want 1", len(own))
	}

	// Another tenant's turn id, with our own JID: nothing.
	foreign, err := s.TurnFrames("web:tenant-a", "turn-b", "", 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(foreign) != 0 {
		t.Errorf("foreign turn returned %d frames, want 0 — turn id alone must not grant a read", len(foreign))
	}

	// An empty JID must match nothing rather than everything.
	none, err := s.TurnFrames("", "turn-a", "", 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Errorf("empty chat_jid returned %d frames, want 0", len(none))
	}

	// The afterID paging branch carries the same predicate.
	page, err := s.TurnFrames("web:tenant-a", "turn-b", "m-b", 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 0 {
		t.Errorf("paged foreign turn returned %d frames, want 0", len(page))
	}
}

package store

import (
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
)

// BotRepliesSince returns only the agent's outbound rows after a cutoff — the
// inverse of MessagesSince. Used by `arizuko send` operator-direct mode to show
// the agent's reply to a CLI-injected message.
func TestBotRepliesSince(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()
	base := time.Now().Truncate(time.Second)

	mustPut := func(m core.Message) {
		if err := s.PutMessage(m); err != nil {
			t.Fatalf("put %s: %v", m.ID, err)
		}
	}
	// operator inbound (not a bot row) — must be excluded
	mustPut(core.Message{ID: "in", ChatJID: "web:krons", Sender: "operator", Content: "draft tweets", Timestamp: base, Verb: "mention"})
	// agent replies (bot rows) after the cutoff — included, oldest first
	mustPut(core.Message{ID: "b1", ChatJID: "web:krons", Sender: "bot", Content: "tweet 1", Timestamp: base.Add(2 * time.Second), BotMsg: true})
	mustPut(core.Message{ID: "b2", ChatJID: "web:krons", Sender: "bot", Content: "tweet 2", Timestamp: base.Add(3 * time.Second), BotMsg: true})
	// bot reply BEFORE the cutoff — excluded
	mustPut(core.Message{ID: "old", ChatJID: "web:krons", Sender: "bot", Content: "stale", Timestamp: base.Add(-time.Hour), BotMsg: true})
	// bot reply on a different jid — excluded
	mustPut(core.Message{ID: "other", ChatJID: "web:happy", Sender: "bot", Content: "elsewhere", Timestamp: base.Add(2 * time.Second), BotMsg: true})

	got, err := s.BotRepliesSince("web:krons", base)
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

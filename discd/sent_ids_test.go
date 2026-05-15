package main

import (
	"strconv"
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestSentIDs_AddHasEvict(t *testing.T) {
	s := newSentIDs(3)
	if s.has("a") {
		t.Fatal("empty set should not have a")
	}
	s.add("a")
	s.add("b")
	s.add("c")
	for _, id := range []string{"a", "b", "c"} {
		if !s.has(id) {
			t.Fatalf("has(%s) = false, want true", id)
		}
	}
	// Eviction: adding a fourth drops the oldest.
	s.add("d")
	if s.has("a") {
		t.Fatal("a should have been evicted")
	}
	for _, id := range []string{"b", "c", "d"} {
		if !s.has(id) {
			t.Fatalf("has(%s) = false after eviction, want true", id)
		}
	}
	// Empty ID is a no-op.
	s.add("")
	if s.has("") {
		t.Fatal("empty ID should not be stored")
	}
	// Re-adding a present ID is a no-op (no double-count, no early eviction).
	s.add("d")
	if !s.has("b") {
		t.Fatal("b should not have been evicted on duplicate add")
	}
}

func TestSentIDs_CapDefault(t *testing.T) {
	s := newSentIDs(0)
	for i := 0; i < 300; i++ {
		s.add(strconv.Itoa(i))
	}
	if !s.has("299") {
		t.Fatal("newest should be present")
	}
	if s.has("0") {
		t.Fatal("oldest should be evicted under default cap")
	}
}

// isMentioned checks @mention, full ReferencedMessage author, and the
// botMsgs cache (the user-mode fallback). Exercise each path.
func TestIsMentioned(t *testing.T) {
	const botID = "bot-1"
	b := &bot{
		session: &discordgo.Session{
			State: &discordgo.State{Ready: discordgo.Ready{User: &discordgo.User{ID: botID}}},
		},
		botMsgs: newSentIDs(8),
	}

	t.Run("explicit @mention", func(t *testing.T) {
		m := &discordgo.MessageCreate{Message: &discordgo.Message{
			Mentions: []*discordgo.User{{ID: botID}},
		}}
		if !b.isMentioned(m) {
			t.Fatal("explicit @mention should be detected")
		}
	})

	t.Run("reply via full ReferencedMessage", func(t *testing.T) {
		m := &discordgo.MessageCreate{Message: &discordgo.Message{
			ReferencedMessage: &discordgo.Message{Author: &discordgo.User{ID: botID}},
		}}
		if !b.isMentioned(m) {
			t.Fatal("reply with ReferencedMessage.Author=bot should be detected")
		}
	})

	t.Run("reply via MessageReference only (user-mode case)", func(t *testing.T) {
		b.botMsgs.add("msg-xyz")
		m := &discordgo.MessageCreate{Message: &discordgo.Message{
			MessageReference: &discordgo.MessageReference{MessageID: "msg-xyz"},
		}}
		if !b.isMentioned(m) {
			t.Fatal("MessageReference.MessageID matching botMsgs should be detected")
		}
	})

	t.Run("MessageReference unknown to botMsgs", func(t *testing.T) {
		m := &discordgo.MessageCreate{Message: &discordgo.Message{
			MessageReference: &discordgo.MessageReference{MessageID: "msg-other"},
		}}
		if b.isMentioned(m) {
			t.Fatal("unknown referenced ID should not count as mention")
		}
	})

	t.Run("non-reply, no mention", func(t *testing.T) {
		m := &discordgo.MessageCreate{Message: &discordgo.Message{Content: "hi"}}
		if b.isMentioned(m) {
			t.Fatal("plain message should not count as mention")
		}
	})
}

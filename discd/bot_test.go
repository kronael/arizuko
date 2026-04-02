package main

import (
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestReplaceMentions_StandardMention(t *testing.T) {
	user := &discordgo.User{ID: "123456"}
	got := replaceMentions("hello <@123456> how are you", "Bot", user)
	want := "hello @Bot how are you"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestReplaceMentions_NickMention(t *testing.T) {
	user := &discordgo.User{ID: "123456"}
	got := replaceMentions("hey <@!123456> check this", "Bot", user)
	want := "hey @Bot check this"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestReplaceMentions_BothFormats(t *testing.T) {
	user := &discordgo.User{ID: "99"}
	got := replaceMentions("<@99> and <@!99> twice", "Ari", user)
	want := "@Ari and @Ari twice"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestReplaceMentions_NoMatch(t *testing.T) {
	user := &discordgo.User{ID: "123456"}
	input := "hello world no mentions"
	got := replaceMentions(input, "Bot", user)
	if got != input {
		t.Errorf("got %q, want unchanged %q", got, input)
	}
}

func TestReplaceMentions_DifferentUser(t *testing.T) {
	user := &discordgo.User{ID: "123456"}
	input := "hello <@999999> other user"
	got := replaceMentions(input, "Bot", user)
	if got != input {
		t.Errorf("got %q, want unchanged %q", got, input)
	}
}

func TestReplaceMentions_EmptyAssistantName(t *testing.T) {
	user := &discordgo.User{ID: "123456"}
	input := "hello <@123456>"
	got := replaceMentions(input, "", user)
	if got != input {
		t.Errorf("got %q, want unchanged %q (empty name)", got, input)
	}
}

func TestReplaceMentions_NilUser(t *testing.T) {
	input := "hello <@123456>"
	got := replaceMentions(input, "Bot", nil)
	if got != input {
		t.Errorf("got %q, want unchanged %q (nil user)", got, input)
	}
}

func TestReplaceMentions_MultipleMentions(t *testing.T) {
	user := &discordgo.User{ID: "42"}
	got := replaceMentions("<@42> said to <@42>: hi", "Agent", user)
	want := "@Agent said to @Agent: hi"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

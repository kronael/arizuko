package main

import (
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestReplaceMentions(t *testing.T) {
	u := &discordgo.User{ID: "123456"}
	cases := []struct {
		name, in, assistant, want string
		user                      *discordgo.User
	}{
		{"standard", "hello <@123456> how are you", "Bot", "hello @Bot how are you", u},
		{"nick", "hey <@!123456> check this", "Bot", "hey @Bot check this", u},
		{"both formats", "<@99> and <@!99> twice", "Ari", "@Ari and @Ari twice", &discordgo.User{ID: "99"}},
		{"no match", "hello world no mentions", "Bot", "hello world no mentions", u},
		{"different user", "hello <@999999> other user", "Bot", "hello <@999999> other user", u},
		{"empty assistant name", "hello <@123456>", "", "hello <@123456>", u},
		{"nil user", "hello <@123456>", "Bot", "hello <@123456>", nil},
		{"multiple", "<@42> said to <@42>: hi", "Agent", "@Agent said to @Agent: hi", &discordgo.User{ID: "42"}},
	}
	for _, c := range cases {
		got := replaceMentions(c.in, c.assistant, c.user, nil)
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// Every in-content user mention resolves to @Username via the message's
// Mentions list; the bot's own mention keeps the @AssistantName rewrite, and
// unresolved tokens stay verbatim.
func TestReplaceMentions_OtherUsers(t *testing.T) {
	self := &discordgo.User{ID: "1", Username: "botself"}
	mentions := []*discordgo.User{
		self,
		{ID: "2", Username: "alice"},
		{ID: "3", Username: "bob"},
	}
	cases := []struct{ name, in, want string }{
		{"other user", "thanks <@2>!", "thanks @alice!"},
		{"nick form", "ping <@!3> now", "ping @bob now"},
		{"bot keeps assistant name", "<@1> and <@2>", "@Ari and @alice"},
		{"unresolved stays", "who is <@999>?", "who is <@999>?"},
		{"mixed", "<@2> tell <@!3> and <@1>", "@alice tell @bob and @Ari"},
	}
	for _, c := range cases {
		if got := replaceMentions(c.in, "Ari", self, mentions); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

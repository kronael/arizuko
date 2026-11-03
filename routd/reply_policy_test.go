package routd

import (
	"testing"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
)

// A reply to a message that addressed the bot addresses the bot too; a
// reaction does so only on the bot's own message.
func TestReplyToAMessageThatAddressedTheBot(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	jid := "discord:G/C"
	srv := NewServer(db, nil, nil, nil, time.Hour, "")
	for _, p := range []core.Message{
		{ID: "asked", ChatJID: jid, Sender: "u1", Verb: "mention", Timestamp: time.Now()},
		{ID: "chat", ChatJID: jid, Sender: "u1", Verb: "message", Timestamp: time.Now()},
		{ID: "answer", ChatJID: jid, BotMsg: true, Timestamp: time.Now()},
	} {
		if err := db.PutMessage(p); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct{ id, verb, replyTo, want string }{
		{"reply-to-mention", "message", "asked", "mention"},
		{"reaction-on-mention", "like", "asked", "like"},
		{"reply-to-chat", "message", "chat", "message"},
		{"reply-to-bot", "message", "answer", "mention"},
		{"reaction-on-bot", "like", "answer", "mention"},
	} {
		rec := doJSON(t, srv.Handler(), "POST", "/v1/messages", "", chanlib.InboundMsg{ID: c.id, ChatJID: jid, Sender: "u2", Content: "x", ReplyTo: c.replyTo, Verb: c.verb, IsGroup: true})
		if rec.Code != 200 {
			t.Fatalf("%s: ingress %d: %s", c.id, rec.Code, rec.Body.String())
		}
		if got, ok := db.MessageByID(jid, c.id); !ok || got.Verb != c.want {
			t.Errorf("%s: verb=%q want %q", c.id, got.Verb, c.want)
		}
	}
}

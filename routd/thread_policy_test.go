package routd

import (
	"testing"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
)

func TestThreadMentionRequiresBotRoot(t *testing.T) {
	for _, platform := range []string{"slack", "discord"} {
		for _, root := range []string{"bot", "human", "missing"} {
			t.Run(platform+"/"+root, func(t *testing.T) {
				db, err := OpenMem()
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { db.Close() })
				jid := platform + ":T/C"
				srv := NewServer(db, nil, nil, nil, time.Hour, "")
				if root != "missing" {
					if err := db.PutMessage(core.Message{ID: "root-local", PlatformID: "thread", ChatJID: jid, BotMsg: root == "bot", Timestamp: time.Now()}); err != nil {
						t.Fatal(err)
					}
				}
				if err := db.PutMessage(core.Message{ID: "bot-reply", PlatformID: "answer", ChatJID: jid, Topic: "thread", TurnID: "thread", BotMsg: true, Timestamp: time.Now()}); err != nil {
					t.Fatal(err)
				}
				ambientReply := ""
				if platform == "slack" {
					ambientReply = "thread"
				}
				ambientWant := "message"
				if root == "bot" {
					ambientWant = "mention"
				}
				for _, test := range []struct{ id, verb, replyTo, want string }{
					{"ambient", "message", ambientReply, ambientWant},
					{"explicit", "mention", ambientReply, "mention"},
					{"direct", "message", "answer", "mention"},
					{"reaction", "like", "answer", "mention"},
					{"untrusted", "untrusted", "answer", "untrusted"},
				} {
					rec := doJSON(t, srv.Handler(), "POST", "/v1/messages", "", chanlib.InboundMsg{ID: test.id, ChatJID: jid, Sender: "user", Content: "text", Topic: "thread", ReplyTo: test.replyTo, Verb: test.verb, IsGroup: true})
					if rec.Code != 200 {
						t.Fatalf("ingress status %d: %s", rec.Code, rec.Body.String())
					}
					seen, ok := db.MessageByID(jid, test.id)
					if !ok || seen.Verb != test.want {
						t.Fatalf("%s verb=%q want %q", test.id, seen.Verb, test.want)
					}
				}
				if err := db.store().BumpEngagement(jid, "thread", "eng", time.Time{}); err != nil {
					t.Fatal(err)
				}
				rec := doJSON(t, srv.Handler(), "POST", "/v1/messages", "", chanlib.InboundMsg{ID: "stopped", ChatJID: jid, Sender: "user", Content: "ambient", Topic: "thread", ReplyTo: ambientReply, Verb: "message", IsGroup: true})
				if rec.Code != 200 {
					t.Fatal(rec.Body.String())
				}
				if seen, ok := db.MessageByID(jid, "stopped"); !ok || seen.Verb != "message" {
					t.Fatalf("stopped thread promoted ambient: %+v", seen)
				}
				if platform == "discord" && root == "bot" {
					rec := doJSON(t, srv.Handler(), "POST", "/v1/messages", "", chanlib.InboundMsg{ID: "direct-root", ChatJID: jid, Sender: "user", Content: "addressed", Topic: "thread", ReplyTo: "thread", Verb: "message", IsGroup: true})
					if rec.Code != 200 {
						t.Fatal(rec.Body.String())
					}
					if seen, ok := db.MessageByID(jid, "direct-root"); !ok || seen.Verb != "mention" {
						t.Fatalf("explicit Discord root reply lost: %+v", seen)
					}
				}
			})
		}
	}
}

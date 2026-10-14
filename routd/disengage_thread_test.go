package routd

import (
	"testing"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
)

func TestDisengageStopsImplicitThreadMentions(t *testing.T) {
	for _, clearTopic := range []string{"thread", ""} {
		t.Run(clearTopic, func(t *testing.T) {
			db, err := OpenMem()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			const jid = "slack:T/C"
			srv := NewServer(db, nil, nil, nil, time.Hour, "")
			if err := db.PutMessage(core.Message{ID: "bot", ChatJID: jid, Topic: "thread", BotMsg: true, Timestamp: time.Now()}); err != nil {
				t.Fatal(err)
			}
			if err := db.PutMessage(core.Message{ID: "thread", ChatJID: jid, Topic: "thread", BotMsg: true, Timestamp: time.Now()}); err != nil {
				t.Fatal(err)
			}
			if err := srv.buildStoreFns(turnMCP{}).SetEngagement(jid, clearTopic, "eng", time.Time{}); err != nil {
				t.Fatal(err)
			}
			for _, test := range []struct{ id, verb, replyTo, want string }{
				{"plain", "message", "", "message"},
				{"root-ref", "message", "thread", "message"},
				{"reaction", "like", "thread", "mention"},
				{"explicit", "mention", "", "mention"},
				{"direct", "message", "bot", "mention"},
			} {
				rec := doJSON(t, srv.Handler(), "POST", "/v1/messages", "", chanlib.InboundMsg{ID: test.id, ChatJID: jid, Sender: "user", Content: "text", Topic: "thread", Verb: test.verb, ReplyTo: test.replyTo, IsGroup: true})
				if rec.Code != 200 {
					t.Fatalf("ingress status: %d %s", rec.Code, rec.Body.String())
				}
				seen, ok := db.MessageByID(jid, test.id)
				if !ok || seen.Verb != test.want {
					t.Fatalf("%s verb=%q want %q", test.id, seen.Verb, test.want)
				}
			}
			if err := db.SetEngagement(jid, "thread", "eng", time.Hour); err != nil {
				t.Fatal(err)
			}
			rec := doJSON(t, srv.Handler(), "POST", "/v1/messages", "", chanlib.InboundMsg{ID: "fresh", ChatJID: jid, Sender: "user", Content: "follow-up", Topic: "thread", Verb: "message", IsGroup: true})
			if rec.Code != 200 {
				t.Fatal(rec.Body.String())
			}
			if seen, ok := db.MessageByID(jid, "fresh"); !ok || seen.Verb != "mention" {
				t.Fatalf("fresh engagement lost implicit follow-up: %+v", seen)
			}
		})
	}
}

func TestDisengagedThreadDoesNotInheritRootEngagement(t *testing.T) {
	for _, route := range []string{"", "observe"} {
		t.Run(route, func(t *testing.T) {
			db, loop := targetLoop(t)
			const jid = "slack:T/C"
			if err := db.PutGroup(core.Group{Folder: "eng"}); err != nil {
				t.Fatal(err)
			}
			if route == "observe" {
				doSetRoutes(t, db, []core.Route{{Match: "chat_jid=" + jid, Target: "eng#observe"}})
			}
			if err := db.SetEngagement(jid, "", "eng", time.Hour); err != nil {
				t.Fatal(err)
			}
			if err := db.SetEngagement(jid, "thread", "eng", -time.Second); err != nil {
				t.Fatal(err)
			}
			result := loop.resolve(jid, core.Message{ChatJID: jid, Topic: "thread", Verb: "message"})
			if result.ok {
				t.Fatal("explicitly stopped thread inherited active root engagement")
			}
			if route == "observe" && result.Observe != "eng" {
				t.Fatalf("observe route lost: %+v", result)
			}
			other := loop.resolve(jid, core.Message{ChatJID: jid, Topic: "other", Verb: "message"})
			if !other.ok || other.Folder != "eng" {
				t.Fatalf("thread clear leaked into other topic: %+v", other)
			}
		})
	}
}

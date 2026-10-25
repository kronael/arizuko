package routd

import (
	"testing"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

func TestGitHubIngressPromotesOnlyAReplyToTheBot(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	routes, err := store.GitHubSourceRoutes("repo/kronael/arizuko", "eng")
	if err != nil {
		t.Fatal(err)
	}
	doSetRoutes(t, db, routes)
	srv := NewServer(db, nil, nil, nil, time.Hour, "")
	loop := NewLoop(db, nil, LoopConfig{})
	defer loop.StopQueue()
	for _, kind := range []string{"pr", "issue"} {
		jid, topic := "github:kronael/arizuko/"+kind+"/5", kind+"/5"
		if err := db.PutMessage(core.Message{ID: "gh-rc42", ChatJID: jid, Topic: topic, BotMsg: true, Timestamp: time.Now()}); err != nil {
			t.Fatal(err)
		}
		if err := db.SetEngagement(jid, topic, "eng", time.Hour); err != nil {
			t.Fatal(err)
		}
		for _, scenario := range []struct {
			id, verb, replyTo string
			mention, fires    bool
		}{
			{"bystander", "message", "", false, true},
			{"direct", "message", "gh-rc42", true, true},
			{"ci", "check_run", "", false, false},
			{"review state", "pull_request_review", "", false, false},
		} {
			rec := doJSON(t, srv.Handler(), "POST", "/v1/messages", "", chanlib.InboundMsg{
				ID: scenario.id, ChatJID: jid, Topic: topic, Sender: "github:user/human", Content: "text",
				Verb: scenario.verb, ReplyTo: scenario.replyTo, IsGroup: true,
			})
			if rec.Code != 200 {
				t.Fatalf("ingress status %d: %s", rec.Code, rec.Body.String())
			}
			seen, ok := db.MessageByID(jid, scenario.id)
			if !ok || (seen.Verb == "mention") != scenario.mention {
				t.Fatalf("%s/%s incorrectly classified: %+v", kind, scenario.id, seen)
			}
			got := loop.resolve(jid, seen)
			if (got.Folder == "eng") != scenario.fires || (!scenario.fires && got.Observe != "eng") {
				t.Fatalf("%s/%s incorrectly routed: %+v", kind, scenario.id, got)
			}
		}
	}
}

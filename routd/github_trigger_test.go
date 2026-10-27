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

func TestGitHubIngressResponsibilityMentionUsesRoute(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	const jid, topic = "github:kronael/arizuko/pr/5", "pr/5"
	doSetRoutes(t, db, []core.Route{
		{Seq: 0, Match: "chat_jid=" + jid + " verb=mention", Target: "review"},
		{Seq: 1, Match: "chat_jid=" + jid, Target: "fallback"},
	})
	srv := NewServer(db, nil, nil, nil, time.Hour, "")
	loop := NewLoop(db, nil, LoopConfig{})
	defer loop.StopQueue()
	for i, stopped := range []bool{false, true} {
		if stopped {
			if err := db.store().BumpEngagement(jid, topic, "fallback", time.Time{}); err != nil {
				t.Fatal(err)
			}
		}
		id := []string{"first", "after-disengage"}[i]
		rec := doJSON(t, srv.Handler(), "POST", "/v1/messages", "", chanlib.InboundMsg{ID: id, ChatJID: jid, Topic: topic, Sender: "github:human", Content: "correction", Verb: "mention", IsGroup: true})
		if rec.Code != 200 {
			t.Fatalf("ingress %d: %s", rec.Code, rec.Body.String())
		}
		seen, ok := db.MessageByID(jid, id)
		if !ok {
			t.Fatal("missing message")
		}
		if got := loop.resolve(jid, seen); got.Folder != "review" {
			t.Fatalf("stopped=%v route=%+v", stopped, got)
		}
	}
}

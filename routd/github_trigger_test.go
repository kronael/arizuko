package routd

import (
	"testing"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

func TestGitHubObserveAndRouteMissCannotInheritEngagement(t *testing.T) {
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
	loop := NewLoop(db, nil, LoopConfig{})
	defer loop.StopQueue()
	for _, scenario := range []struct{ jid, verb, folder, observe string }{
		{"github:kronael/arizuko/pr/5", "message", "", "eng"},
		{"github:kronael/arizuko/pr/5", "pull_request", "", "eng"},
		{"github:kronael/arizuko/pr/5", "mention", "eng", ""},
		{"github:another/repository/pr/5", "message", "", ""},
		{"github:another/repository/pr/5", "mention", "", ""},
	} {
		for _, topic := range []string{"", "pr/5"} {
			if err := db.store().BumpEngagement(scenario.jid, topic, "foreign-owner", time.Now().Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
		}
		got := loop.resolve(scenario.jid, core.Message{ChatJID: scenario.jid, Topic: "pr/5", Verb: scenario.verb})
		if got.Folder != scenario.folder || got.Observe != scenario.observe {
			t.Fatalf("%s verb=%s inherited engagement: %+v", scenario.jid, scenario.verb, got)
		}
	}
}

func TestGitHubIngressRequiresAnActualPRRequest(t *testing.T) {
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
		for _, id := range []string{topic, "gh-rc42"} {
			if err := db.PutMessage(core.Message{ID: id, ChatJID: jid, Topic: topic, BotMsg: true, Timestamp: time.Now()}); err != nil {
				t.Fatal(err)
			}
		}
		if err := db.SetEngagement(jid, topic, "eng", time.Hour); err != nil {
			t.Fatal(err)
		}
		for _, scenario := range []struct {
			id, verb, replyTo string
			trigger           bool
		}{
			{"bystander", "message", "", false},
			{"direct", "message", "gh-rc42", kind == "pr"},
			{"edited", "issue_comment", "gh-rc42", false},
			{"review state", "pull_request_review", "gh-rc42", false},
		} {
			rec := doJSON(t, srv.Handler(), "POST", "/v1/messages", "", chanlib.InboundMsg{
				ID: scenario.id, ChatJID: jid, Topic: topic, Sender: "github:user/human", Content: "text",
				Verb: scenario.verb, ReplyTo: scenario.replyTo, IsGroup: true,
			})
			if rec.Code != 200 {
				t.Fatalf("ingress status %d: %s", rec.Code, rec.Body.String())
			}
			seen, ok := db.MessageByID(jid, scenario.id)
			if !ok || (seen.Verb == "mention") != scenario.trigger {
				t.Fatalf("%s/%s incorrectly classified: %+v", kind, scenario.id, seen)
			}
			got := loop.resolve(jid, seen)
			if (got.Folder == "eng") != scenario.trigger || (!scenario.trigger && got.Observe != "eng") {
				t.Fatalf("%s/%s incorrectly routed: %+v", kind, scenario.id, got)
			}
		}
	}
}

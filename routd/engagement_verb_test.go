package routd

import (
	"testing"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// Only a person's message or mention rides engagement, on every channel. Every
// other verb follows its route (specs/6/27-git-channel.md, ROUTING.md
// §"Engagement follows a person's message").
func newEngagementLoop(t *testing.T, routes []core.Route) (*DB, *Loop) {
	t.Helper()
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	doSetRoutes(t, db, routes)
	loop := NewLoop(db, nil, LoopConfig{})
	t.Cleanup(loop.StopQueue)
	return db, loop
}

func TestEngagementVerb_GitHubPRRidesOnlyAPersonsMessage(t *testing.T) {
	routes, err := store.GitHubSourceRoutes("repo/kronael/arizuko", "eng")
	if err != nil {
		t.Fatal(err)
	}
	db, loop := newEngagementLoop(t, routes)
	jid := "github:kronael/arizuko/pr/5"
	if err := db.SetEngagement(jid, "pr/5", "held", time.Hour); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ verb, folder, observe string }{
		{"message", "held", ""},
		{"", "held", ""},
		{"mention", "held", ""},
		{"check_run", "", "eng"},
		{"pull_request_review", "", "eng"},
		{"pull_request", "", "eng"},
	} {
		got := loop.resolve(jid, core.Message{ChatJID: jid, Topic: "pr/5", Verb: c.verb})
		if got.Folder != c.folder || got.Observe != c.observe {
			t.Errorf("verb=%q: got %+v want folder=%q observe=%q", c.verb, got, c.folder, c.observe)
		}
	}
}

func TestEngagementVerb_ChatReactionAndEditFollowTheirRoute(t *testing.T) {
	db, loop := newEngagementLoop(t, []core.Route{
		{Seq: 1, Match: "chat_jid=telegram:7 verb=mention", Target: "route"},
		{Seq: 2, Match: "chat_jid=telegram:7", Target: "route#observe"},
	})
	jid := "telegram:7"
	if err := db.SetEngagement(jid, "", "held", time.Hour); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ verb, folder, observe string }{
		{"message", "held", ""},
		{"like", "", "route"},
		{"dislike", "", "route"},
		{"edit", "", "route"},
		{"join", "", "route"},
	} {
		got := loop.resolve(jid, core.Message{ChatJID: jid, Verb: c.verb})
		if got.Folder != c.folder || got.Observe != c.observe {
			t.Errorf("verb=%q: got %+v want folder=%q observe=%q", c.verb, got, c.folder, c.observe)
		}
	}
}

func TestEngagementVerb_RouteMissHoldsAReaction(t *testing.T) {
	db, loop := newEngagementLoop(t, nil)
	jid := "telegram:8"
	if err := db.SetEngagement(jid, "", "held", time.Hour); err != nil {
		t.Fatal(err)
	}
	if got := loop.resolve(jid, core.Message{ChatJID: jid, Verb: "message"}); got.Folder != "held" || !got.ok {
		t.Errorf("message on a route miss: %+v", got)
	}
	if got := loop.resolve(jid, core.Message{ChatJID: jid, Verb: "like"}); got.ok || got.Observe != "" {
		t.Errorf("like on a route miss must be held: %+v", got)
	}
}

func TestEngagementVerb_AReplyToABotRowOnAPRIsStillAMention(t *testing.T) {
	routes, err := store.GitHubSourceRoutes("repo/kronael/arizuko", "eng")
	if err != nil {
		t.Fatal(err)
	}
	db, _ := newEngagementLoop(t, routes)
	srv := NewServer(db, nil, nil, nil, time.Hour, "")
	jid, topic := "github:kronael/arizuko/pr/5", "pr/5"
	if err := db.PutMessage(core.Message{ID: "gh-rc42", ChatJID: jid, Topic: topic, BotMsg: true, Timestamp: time.Now()}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		id, verb, replyTo string
		mention           bool
	}{
		{"reply", "message", "gh-rc42", true},
		{"ci", "check_run", "", false},
	} {
		rec := doJSON(t, srv.Handler(), "POST", "/v1/messages", "", chanlib.InboundMsg{
			ID: c.id, ChatJID: jid, Topic: topic, Sender: "github:user/human", Content: "text",
			Verb: c.verb, ReplyTo: c.replyTo, IsGroup: true,
		})
		if rec.Code != 200 {
			t.Fatalf("ingress status %d: %s", rec.Code, rec.Body.String())
		}
		seen, ok := db.MessageByID(jid, c.id)
		if !ok || (seen.Verb == "mention") != c.mention {
			t.Errorf("%s: classified %+v want mention=%v", c.id, seen, c.mention)
		}
	}
}

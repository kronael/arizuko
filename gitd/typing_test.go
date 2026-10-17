package main

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kronael/arizuko/chanlib"
)

// reactionAPI is a fake GitHub that records every request and answers the
// reaction endpoints: POST returns an incrementing reaction id, DELETE 204.
type reactionAPI struct {
	mu         sync.Mutex
	calls      []string
	bodies     []string
	postStatus int
	postReply  string
	deleted    chan struct{}
	nextID     int
}

func newReactionAPI(t *testing.T) (*reactionAPI, string) {
	t.Helper()
	api := &reactionAPI{postStatus: 201, deleted: make(chan struct{}, 8), nextID: 100}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	return api, srv.URL
}

func (a *reactionAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	a.mu.Lock()
	a.calls = append(a.calls, r.Method+" "+r.URL.Path)
	a.bodies = append(a.bodies, string(raw))
	status, reply := a.postStatus, a.postReply
	a.nextID++
	id := a.nextID
	a.mu.Unlock()
	if r.Method == "DELETE" {
		w.WriteHeader(204)
		a.deleted <- struct{}{}
		return
	}
	w.WriteHeader(status)
	if reply == "" {
		reply = fmt.Sprintf(`{"id":%d,"content":"eyes"}`, id)
	}
	_, _ = w.Write([]byte(reply))
}

func (a *reactionAPI) seen() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.calls...)
}

func typingBot(url string) *bot {
	return newBot(config{Token: "ghp_test", APIBase: url})
}

func comment(jid, id, verb string) chanlib.InboundMsg {
	return chanlib.InboundMsg{ChatJID: jid, ID: id, Verb: verb}
}

func wantCalls(t *testing.T, api *reactionAPI, want ...string) {
	t.Helper()
	got := api.seen()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("GitHub requests:\n got %q\nwant %q", got, want)
	}
}

const prJID = "github:kronael/arizuko/pr/12"

// An issue comment and an inline review comment are reacted to through
// different endpoints, and the delete names the reaction id the create
// returned.
func TestTyping_ReactsOnTheTriggerComment(t *testing.T) {
	for _, c := range []struct{ name, id, path string }{
		{"conversation comment", "gh-c4567890123", "/repos/kronael/arizuko/issues/comments/4567890123/reactions"},
		{"inline review comment", "gh-rc5678901234", "/repos/kronael/arizuko/pulls/comments/5678901234/reactions"},
	} {
		t.Run(c.name, func(t *testing.T) {
			api, url := newReactionAPI(t)
			b := typingBot(url)
			b.noteTrigger(comment(prJID, c.id, "mention"))
			b.Typing(prJID, true)
			b.Typing(prJID, false)
			wantCalls(t, api, "POST "+c.path, "DELETE "+c.path+"/101")
			if api.bodies[0] != `{"content":"eyes"}` {
				t.Errorf("reaction body = %s", api.bodies[0])
			}
		})
	}
}

// The reaction removed is the one set, even when a newer comment arrived while
// the agent worked.
func TestTyping_OffRemovesTheReactionSet(t *testing.T) {
	api, url := newReactionAPI(t)
	b := typingBot(url)
	b.noteTrigger(comment(prJID, "gh-c1", "message"))
	b.Typing(prJID, true)
	b.noteTrigger(comment(prJID, "gh-c2", "message"))
	b.Typing(prJID, false)
	wantCalls(t, api,
		"POST /repos/kronael/arizuko/issues/comments/1/reactions",
		"DELETE /repos/kronael/arizuko/issues/comments/1/reactions/101")
}

// A message that cannot start a turn must not retarget the indicator, and one
// that can but has no reactable comment (a review) must clear the old target
// rather than leave the 👀 on an older comment.
func TestTyping_TargetFollowsTheTrigger(t *testing.T) {
	api, url := newReactionAPI(t)
	b := typingBot(url)
	b.noteTrigger(comment(prJID, "gh-c1", "message"))
	b.noteTrigger(comment(prJID, "gh-c2", "issue_comment"))
	b.Typing(prJID, true)
	b.Typing(prJID, false)
	wantCalls(t, api,
		"POST /repos/kronael/arizuko/issues/comments/1/reactions",
		"DELETE /repos/kronael/arizuko/issues/comments/1/reactions/101")

	b.noteTrigger(comment(prJID, "gh-r77", "message"))
	b.Typing(prJID, true)
	b.Typing(prJID, false)
	if got := len(api.seen()); got != 2 {
		t.Errorf("a review trigger sent %d more requests, want none", got-2)
	}
}

func TestTyping_NoTokenSendsNothing(t *testing.T) {
	api, url := newReactionAPI(t)
	b := newBot(config{APIBase: url})
	b.noteTrigger(comment(prJID, "gh-c1", "message"))
	b.Typing(prJID, true)
	b.Typing(prJID, false)
	wantCalls(t, api)
}

// GitHub answers 200 when the reaction already stands; it is a success.
func TestTyping_AlreadyReactedIsSuccess(t *testing.T) {
	api, url := newReactionAPI(t)
	api.postStatus = 200
	b := typingBot(url)
	b.noteTrigger(comment(prJID, "gh-c1", "message"))
	b.Typing(prJID, true)
	b.Typing(prJID, false)
	if got := api.seen(); len(got) != 2 || !strings.HasPrefix(got[1], "DELETE ") {
		t.Errorf("requests = %q", got)
	}
}

// A refusal is logged at warn with GitHub's reason, never returned, and leaves
// no reaction to delete.
func TestTyping_RefusalIsLoggedNotRaised(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	api, url := newReactionAPI(t)
	api.postStatus = 403
	api.postReply = `{"message":"Resource not accessible by integration"}`
	b := typingBot(url)
	b.noteTrigger(comment(prJID, "gh-c1", "message"))
	b.Typing(prJID, true)
	b.Typing(prJID, false)

	wantCalls(t, api, "POST /repos/kronael/arizuko/issues/comments/1/reactions")
	for _, want := range []string{"level=WARN", "github typing reaction failed", "Resource not accessible by integration"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log lacks %q:\n%s", want, logs.String())
		}
	}
}

// With no Typing(off), the reaction goes at the same backstop the other
// adapters' indicators stop at.
func TestTyping_BackstopRemovesALostOff(t *testing.T) {
	prev := chanlib.DefaultTypingMaxTTL
	chanlib.DefaultTypingMaxTTL = 20 * time.Millisecond
	t.Cleanup(func() { chanlib.DefaultTypingMaxTTL = prev })

	api, url := newReactionAPI(t)
	b := typingBot(url)
	b.noteTrigger(comment(prJID, "gh-c1", "message"))
	b.Typing(prJID, true)
	select {
	case <-api.deleted:
	case <-time.After(5 * time.Second):
		t.Fatal("the backstop never removed the reaction")
	}
	wantCalls(t, api,
		"POST /repos/kronael/arizuko/issues/comments/1/reactions",
		"DELETE /repos/kronael/arizuko/issues/comments/1/reactions/101")
}

// A backstop armed for an earlier reaction must not remove a newer one.
func TestTyping_StaleBackstopLeavesTheNewerReaction(t *testing.T) {
	api, url := newReactionAPI(t)
	b := typingBot(url)
	b.noteTrigger(comment(prJID, "gh-c1", "message"))
	b.Typing(prJID, true)
	b.typing.mu.Lock()
	old := b.typing.live[prJID]
	b.typing.mu.Unlock()
	b.Typing(prJID, true)
	if err := b.removeEyes(prJID, old); err != nil {
		t.Fatal(err)
	}
	b.typing.mu.Lock()
	_, standing := b.typing.live[prJID]
	b.typing.mu.Unlock()
	if !standing {
		t.Errorf("the stale backstop removed the newer reaction; requests %q", api.seen())
	}
}

// A graceful shutdown takes the standing reactions down.
func TestStop_RemovesStandingReactions(t *testing.T) {
	api, url := newReactionAPI(t)
	b := typingBot(url)
	b.noteTrigger(comment(prJID, "gh-c1", "message"))
	b.Typing(prJID, true)
	b.stop()
	wantCalls(t, api,
		"POST /repos/kronael/arizuko/issues/comments/1/reactions",
		"DELETE /repos/kronael/arizuko/issues/comments/1/reactions/101")
}

// Ingest is what tells gitd which comment to react to: a delivered comment is
// reacted to, and a dropped (bot-authored) one is not recorded.
func TestWebhook_RecordsTheTriggerComment(t *testing.T) {
	api, url := newReactionAPI(t)
	e := newTestEnv(t, secretMap{"repo/kronael/arizuko": "s3cret"})
	e.srv.bot.cfg.Token, e.srv.bot.cfg.APIBase = "ghp_test", url

	for _, c := range []struct{ event, fixture, path string }{
		{"issue_comment", "issue_comment_created", "/repos/kronael/arizuko/issues/comments/4567890123/reactions"},
		{"pull_request_review_comment", "pull_request_review_comment_created", "/repos/kronael/arizuko/pulls/comments/5678901234/reactions"},
	} {
		w := e.deliver(t, "/github/repo/kronael/arizuko", "s3cret", c.event, "d-"+c.fixture, fixture(t, c.fixture))
		if w.Code != 200 {
			t.Fatalf("%s: status=%d body=%s", c.event, w.Code, w.Body.String())
		}
		msgs := e.routd.messages()
		jid := msgs[len(msgs)-1].ChatJID
		e.srv.bot.Typing(jid, true)
		e.srv.bot.Typing(jid, false)
		got := api.seen()
		if len(got) < 2 || got[len(got)-2] != "POST "+c.path {
			t.Errorf("%s: requests %q, want a POST to %s", c.event, got, c.path)
		}
	}
}

// A delivery gitd drops (a bot's own comment) never becomes a trigger.
func TestWebhook_DroppedCommentIsNotATrigger(t *testing.T) {
	api, url := newReactionAPI(t)
	e := newTestEnv(t, secretMap{"repo/kronael/arizuko": "s3cret"})
	e.srv.bot.cfg.Token, e.srv.bot.cfg.APIBase = "ghp_test", url
	w := e.deliver(t, "/github/repo/kronael/arizuko", "s3cret", "issue_comment", "d-bot", fixture(t, "issue_comment_bot"))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"dropped"`) {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	e.srv.bot.Typing(prJID, true)
	wantCalls(t, api)
}

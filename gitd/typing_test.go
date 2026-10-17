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
	"github.com/kronael/arizuko/chanlib/chanstore"
	"github.com/kronael/arizuko/objstore/dynstore/dyntest"
)

// reactionAPI is a fake GitHub that records every request and answers the
// reaction endpoints: POST returns an incrementing reaction id, DELETE 204.
type reactionAPI struct {
	mu         sync.Mutex
	calls      []string
	bodies     []string
	postStatus int
	// deleteStatus is what a DELETE answers; zero is 204.
	deleteStatus int
	postReply    string
	deleted      chan struct{}
	nextID       int
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
	status, reply, delStatus := a.postStatus, a.postReply, a.deleteStatus
	a.nextID++
	id := a.nextID
	a.mu.Unlock()
	if r.Method == "DELETE" {
		if delStatus == 0 {
			delStatus = 204
		}
		w.WriteHeader(delStatus)
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

// typingEnv is one gitd process as far as the 👀 is concerned: a bot with its
// mark, the client every inbound passes through, and the adapter store. Two
// envs over one store and one fake GitHub are a restart.
type typingEnv struct {
	b  *bot
	rc *chanlib.RouterClient
	st *chanstore.Store
}

func adapterStore(t *testing.T) *chanstore.Store {
	t.Helper()
	return chanstore.New(dyntest.Store(t, chanstore.Entities...))
}

func newTypingEnv(t *testing.T, st *chanstore.Store, cfg config) *typingEnv {
	t.Helper()
	routd := httptest.NewServer(&fakeRoutd{})
	t.Cleanup(routd.Close)
	e := &typingEnv{b: newBot(cfg), rc: chanlib.NewRouterClient(routd.URL), st: st}
	e.b.useMark(e.rc, st)
	t.Cleanup(e.b.stop)
	return e
}

// typingBot is one process over a fresh store.
func typingBot(t *testing.T, url string) *typingEnv {
	t.Helper()
	return newTypingEnv(t, adapterStore(t), config{Token: "ghp_test", APIBase: url})
}

// comment is an inbound as ingest hands it to routd.
func (e *typingEnv) comment(t *testing.T, jid, id, verb string) {
	t.Helper()
	if err := e.rc.SendMessage(chanlib.InboundMsg{ChatJID: jid, ID: id, Verb: verb}); err != nil {
		t.Fatalf("deliver %s: %v", id, err)
	}
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
			e := typingBot(t, url)
			e.comment(t, prJID, c.id, "mention")
			e.b.Typing(prJID, true)
			e.b.Typing(prJID, false)
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
	e := typingBot(t, url)
	e.comment(t, prJID, "gh-c1", "message")
	e.b.Typing(prJID, true)
	e.comment(t, prJID, "gh-c2", "message")
	e.b.Typing(prJID, false)
	wantCalls(t, api,
		"POST /repos/kronael/arizuko/issues/comments/1/reactions",
		"DELETE /repos/kronael/arizuko/issues/comments/1/reactions/101")
}

// A message that cannot start a turn must not retarget the indicator, and one
// that can but has no reactable comment (a review) must not leave the 👀 on an
// older comment.
func TestTyping_TargetFollowsTheTrigger(t *testing.T) {
	api, url := newReactionAPI(t)
	e := typingBot(t, url)
	e.comment(t, prJID, "gh-c1", "message")
	e.comment(t, prJID, "gh-c2", "issue_comment")
	e.b.Typing(prJID, true)
	e.b.Typing(prJID, false)
	wantCalls(t, api,
		"POST /repos/kronael/arizuko/issues/comments/1/reactions",
		"DELETE /repos/kronael/arizuko/issues/comments/1/reactions/101")

	e.comment(t, prJID, "gh-r77", "message")
	e.b.Typing(prJID, true)
	e.b.Typing(prJID, false)
	if got := len(api.seen()); got != 2 {
		t.Errorf("a review trigger sent %d more requests, want none", got-2)
	}
}

func TestTyping_NoTokenSendsNothing(t *testing.T) {
	api, url := newReactionAPI(t)
	e := newTypingEnv(t, adapterStore(t), config{APIBase: url})
	e.comment(t, prJID, "gh-c1", "message")
	e.b.Typing(prJID, true)
	e.b.Typing(prJID, false)
	wantCalls(t, api)
}

// GitHub answers 200 when the reaction already stands; it is a success.
func TestTyping_AlreadyReactedIsSuccess(t *testing.T) {
	api, url := newReactionAPI(t)
	api.postStatus = 200
	e := typingBot(t, url)
	e.comment(t, prJID, "gh-c1", "message")
	e.b.Typing(prJID, true)
	e.b.Typing(prJID, false)
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
	e := typingBot(t, url)
	e.comment(t, prJID, "gh-c1", "message")
	e.b.Typing(prJID, true)
	e.b.Typing(prJID, false)

	wantCalls(t, api, "POST /repos/kronael/arizuko/issues/comments/1/reactions")
	for _, want := range []string{"level=WARN", "ack mark: add failed", "Resource not accessible by integration"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log lacks %q:\n%s", want, logs.String())
		}
	}
}

// 404 on the delete is the reaction already gone, which is what was wanted.
func TestTyping_AlreadyGoneIsSuccess(t *testing.T) {
	api, url := newReactionAPI(t)
	e := typingBot(t, url)
	e.comment(t, prJID, "gh-c1", "message")
	e.b.Typing(prJID, true)
	api.deleteStatus = 404
	e.b.Typing(prJID, false)
	if marks, _ := e.st.Marks(t.Context()); len(marks) != 0 {
		t.Errorf("a 404 on the delete kept the record: %+v", marks)
	}
}

// With no Typing(off), the reaction goes at the same backstop the other
// adapters' indicators stop at.
func TestTyping_BackstopRemovesALostOff(t *testing.T) {
	shortBackstop(t)
	api, url := newReactionAPI(t)
	e := typingBot(t, url)
	e.comment(t, prJID, "gh-c1", "message")
	e.b.Typing(prJID, true)
	select {
	case <-api.deleted:
	case <-time.After(5 * time.Second):
		t.Fatal("the backstop never removed the reaction")
	}
	wantCalls(t, api,
		"POST /repos/kronael/arizuko/issues/comments/1/reactions",
		"DELETE /repos/kronael/arizuko/issues/comments/1/reactions/101")
}

func shortBackstop(t *testing.T) {
	t.Helper()
	prev := chanlib.DefaultTypingMaxTTL
	chanlib.DefaultTypingMaxTTL = 50 * time.Millisecond
	t.Cleanup(func() { chanlib.DefaultTypingMaxTTL = prev })
}

// A graceful shutdown leaves the reaction: the turn it marks may still be
// running, and routd's off removes it.
func TestStop_KeepsTheStandingReaction(t *testing.T) {
	api, url := newReactionAPI(t)
	e := typingBot(t, url)
	e.comment(t, prJID, "gh-c1", "message")
	e.b.Typing(prJID, true)
	e.b.stop()
	wantCalls(t, api, "POST /repos/kronael/arizuko/issues/comments/1/reactions")
}

// The case the maps could not survive: the process dies with a turn running,
// and the end of the turn reaches a process that never saw the start.
func TestTyping_OffAfterARestartRemovesTheReaction(t *testing.T) {
	api, url := newReactionAPI(t)
	st := adapterStore(t)
	first := newTypingEnv(t, st, config{Token: "ghp_test", APIBase: url})
	first.comment(t, prJID, "gh-c1", "message")
	first.b.Typing(prJID, true)

	second := newTypingEnv(t, st, config{Token: "ghp_test", APIBase: url})
	second.b.Typing(prJID, false)

	wantCalls(t, api,
		"POST /repos/kronael/arizuko/issues/comments/1/reactions",
		"DELETE /repos/kronael/arizuko/issues/comments/1/reactions/101")
}

// The residue the in-memory maps left: SIGKILL with a 👀 standing and nobody to
// remove it. The first process is never stopped; a second one boots after the
// backstop would have fired, sweeps, and the reaction comes off.
func TestSweep_ClearsAReactionAKilledProcessLeft(t *testing.T) {
	api, url := newReactionAPI(t)
	st := adapterStore(t)
	first := newTypingEnv(t, st, config{Token: "ghp_test", APIBase: url})
	first.comment(t, prJID, "gh-rc9", "mention")
	first.b.Typing(prJID, true)
	wantCalls(t, api, "POST /repos/kronael/arizuko/pulls/comments/9/reactions")

	shortBackstop(t)
	time.Sleep(100 * time.Millisecond)
	second := newTypingEnv(t, st, config{Token: "ghp_test", APIBase: url})
	second.b.mark.Sweep(t.Context())

	wantCalls(t, api,
		"POST /repos/kronael/arizuko/pulls/comments/9/reactions",
		"DELETE /repos/kronael/arizuko/pulls/comments/9/reactions/101")
	if marks, _ := st.Marks(t.Context()); len(marks) != 0 {
		t.Errorf("records left after the sweep: %+v", marks)
	}
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

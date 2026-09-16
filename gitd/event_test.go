package main

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/router"
	"github.com/kronael/arizuko/store"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

var fixedNow = time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)

func mapFixture(t *testing.T, kind, name, event, file string) chanlib.InboundMsg {
	t.Helper()
	msg, drop, err := mapEvent(kind, name, event, "d-"+file, fixture(t, file), fixedNow)
	if err != nil || drop != "" {
		t.Fatalf("%s: err=%v drop=%q", file, err, drop)
	}
	return msg
}

func TestMapEvent_Fixtures(t *testing.T) {
	cases := []struct {
		file, event                 string
		jid, verb, topic, id        string
		sender, replyTo, contentHas string
		ts                          int64
	}{
		{"ping", "ping", "github:kronael/arizuko", "ping", "", "gh-dd-ping",
			"github:kronael", "", "ping: Keep it logically awesome.", fixedNow.Unix()},
		{"push", "push", "github:kronael/arizuko", "push", "", "gh-dd-push",
			"github:kronael", "", "kronael pushed 2 commit(s) to refs/heads/main\n- b7c8d9e fix(routd): drop the stale topic cursor\n- 0f1e2d3 docs(changelog): v0.63.1",
			time.Date(2026, 9, 5, 9, 14, 44, 0, time.UTC).Unix()},
		{"issues_opened", "issues", "github:kronael/arizuko/issue/3", "message", "issue/3", "gh-dd-issues_opened",
			"github:kronael", "", "opened issue #3: gitd: GitHub events as messages\nhttps://github.com/kronael/arizuko/issues/3\n\nA repo's events", fixedNow.Unix()},
		{"pull_request_opened", "pull_request", "github:kronael/arizuko/pr/12", "message", "pr/12", "gh-dd-pull_request_opened",
			"github:rhias", "", "opened PR #12: feat(gitd): GitHub as an ingress channel (gitd → main)\nhttps://github.com/kronael/arizuko/pull/12\n\nWebhooks land",
			time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC).Unix()},
		{"pull_request_merged", "pull_request", "github:kronael/arizuko/pr/12", "pull_request", "pr/12", "gh-dd-pull_request_merged",
			"github:kronael", "", "kronael merged PR #12: feat(gitd): GitHub as an ingress channel",
			time.Date(2026, 9, 5, 11, 0, 0, 0, time.UTC).Unix()},
		{"issue_comment_created", "issue_comment", "github:kronael/arizuko/pr/12", "message", "pr/12", "gh-c4567890123",
			"github:kronael", "", "@arizuko can you review the signature check in verify.go?",
			time.Date(2026, 9, 5, 10, 5, 30, 0, time.UTC).Unix()},
		{"issue_comment_created_crackbox", "issue_comment", "github:kronael/crackbox/issue/7", "message", "issue/7", "gh-c4567890999",
			"github:rhias", "", "Reproduced on 6.1; the proxy PID file is stale.",
			time.Date(2026, 9, 5, 10, 6, 0, 0, time.UTC).Unix()},
		{"pull_request_review_comment_created", "pull_request_review_comment", "github:kronael/arizuko/pr/12", "message", "pr/12", "gh-c5678901234",
			"github:kronael", "gh-c5678901200", "on gitd/verify.go:\n\nConstant-time compare — good. Also reject an empty secret.",
			time.Date(2026, 9, 5, 10, 7, 15, 0, time.UTC).Unix()},
	}
	for _, c := range cases {
		msg := mapFixture(t, store.GitHubSourceApp, "arizuko-krons", c.event, c.file)
		if msg.ChatJID != c.jid || msg.Verb != c.verb || msg.Topic != c.topic || msg.ID != c.id ||
			msg.Sender != c.sender || msg.ReplyTo != c.replyTo || msg.Timestamp != c.ts {
			t.Errorf("%s: got jid=%q verb=%q topic=%q id=%q sender=%q reply_to=%q ts=%d\n want jid=%q verb=%q topic=%q id=%q sender=%q reply_to=%q ts=%d",
				c.file, msg.ChatJID, msg.Verb, msg.Topic, msg.ID, msg.Sender, msg.ReplyTo, msg.Timestamp,
				c.jid, c.verb, c.topic, c.id, c.sender, c.replyTo, c.ts)
		}
		if !strings.HasPrefix(msg.Content, c.contentHas) {
			t.Errorf("%s: content = %q, want prefix %q", c.file, msg.Content, c.contentHas)
		}
		if !msg.IsGroup || msg.SenderName != strings.TrimPrefix(c.sender, "github:") {
			t.Errorf("%s: is_group=%v sender_name=%q", c.file, msg.IsGroup, msg.SenderName)
		}
		if _, ok := chanlib.ParseGitHubJID(msg.ChatJID); !ok {
			t.Errorf("%s: %q does not parse as a GitHub JID", c.file, msg.ChatJID)
		}
	}
}

// One App secret, two repos: two JIDs, two topics, and the route table tells
// them apart without any per-repo registration (spec 6/27 decision 3).
func TestMapEvent_TwoReposOneSecret(t *testing.T) {
	a := mapFixture(t, store.GitHubSourceApp, "arizuko-krons", "issue_comment", "issue_comment_created")
	b := mapFixture(t, store.GitHubSourceApp, "arizuko-krons", "issue_comment", "issue_comment_created_crackbox")
	if a.ChatJID == b.ChatJID || a.ChatJID != "github:kronael/arizuko/pr/12" || b.ChatJID != "github:kronael/crackbox/issue/7" {
		t.Fatalf("jids: %q vs %q", a.ChatJID, b.ChatJID)
	}
	routes, err := store.GitHubSourceRoutes("app/arizuko-krons", "eng")
	if err != nil {
		t.Fatal(err)
	}
	if got := resolve(routes, a); got.Folder != "eng" || got.Mode != "" {
		t.Errorf("arizuko comment resolved to %+v", got)
	}
	if got := resolve(routes, b); got.Folder != "eng" || got.Mode != "" {
		t.Errorf("crackbox comment resolved to %+v", got)
	}
	narrower, err := store.GitHubSourceRoutes("repo/kronael/crackbox", "ops")
	if err != nil {
		t.Fatal(err)
	}
	routes = append(routes, narrower...)
	if got := resolve(routes, b); got.Folder != "ops" || got.Mode != "" {
		t.Errorf("crackbox comment with a repo registration resolved to %+v, want ops", got)
	}
	if got := resolve(routes, a); got.Folder != "eng" {
		t.Errorf("arizuko comment after crackbox registration resolved to %+v, want eng", got)
	}
}

// Observe vs trigger is the route's decision: the same registration observes
// a push and triggers on a comment (spec 6/27 decision 4).
func TestMapEvent_ObserveVersusTrigger(t *testing.T) {
	routes, err := store.GitHubSourceRoutes("repo/kronael/arizuko", "eng")
	if err != nil {
		t.Fatal(err)
	}
	push := mapFixture(t, store.GitHubSourceRepo, "kronael/arizuko", "push", "push")
	if got := resolve(routes, push); got.Folder != "eng" || got.Mode != "observe" {
		t.Errorf("push resolved to %+v, want eng#observe", got)
	}
	merged := mapFixture(t, store.GitHubSourceRepo, "kronael/arizuko", "pull_request", "pull_request_merged")
	if got := resolve(routes, merged); got.Folder != "eng" || got.Mode != "observe" {
		t.Errorf("merge resolved to %+v, want eng#observe", got)
	}
	opened := mapFixture(t, store.GitHubSourceRepo, "kronael/arizuko", "pull_request", "pull_request_opened")
	if got := resolve(routes, opened); got.Folder != "eng" || got.Mode != "" {
		t.Errorf("PR opened resolved to %+v, want eng trigger", got)
	}
	comment := mapFixture(t, store.GitHubSourceRepo, "kronael/arizuko", "issue_comment", "issue_comment_created")
	if got := resolve(routes, comment); got.Folder != "eng" || got.Mode != "" {
		t.Errorf("comment resolved to %+v, want eng trigger", got)
	}
	comment.Verb = "mention"
	if got := resolve(routes, comment); got.Folder != "eng" || got.Mode != "" {
		t.Errorf("mention-promoted comment resolved to %+v, want eng trigger", got)
	}
	ping := mapFixture(t, store.GitHubSourceRepo, "kronael/arizuko", "ping", "ping")
	if got := resolve(routes, ping); got.Folder != "eng" || got.Mode != "observe" {
		t.Errorf("ping resolved to %+v, want eng#observe", got)
	}
}

func resolve(routes []core.Route, msg chanlib.InboundMsg) core.RouteTarget {
	sorted := slices.Clone(routes)
	slices.SortStableFunc(sorted, func(a, b core.Route) int { return a.Seq - b.Seq })
	return router.ResolveRouteTarget(core.Message{ChatJID: msg.ChatJID, Sender: msg.Sender, Verb: msg.Verb}, sorted)
}

func TestMapEvent_SourceContainment(t *testing.T) {
	arizuko := fixture(t, "issue_comment_created")
	if _, _, err := mapEvent(store.GitHubSourceRepo, "kronael/arizuko", "issue_comment", "d", arizuko, fixedNow); err != nil {
		t.Errorf("own repo rejected: %v", err)
	}
	if _, _, err := mapEvent(store.GitHubSourceRepo, "Kronael/Arizuko", "issue_comment", "d", arizuko, fixedNow); err != nil {
		t.Errorf("case-insensitive repo rejected: %v", err)
	}
	if _, _, err := mapEvent(store.GitHubSourceRepo, "kronael/crackbox", "issue_comment", "d", arizuko, fixedNow); !errors.Is(err, errSourceMismatch) {
		t.Errorf("other repo err = %v, want mismatch", err)
	}
	if _, _, err := mapEvent(store.GitHubSourceOrg, "kronael", "issue_comment", "d", arizuko, fixedNow); err != nil {
		t.Errorf("own org rejected: %v", err)
	}
	if _, _, err := mapEvent(store.GitHubSourceOrg, "rhias", "issue_comment", "d", arizuko, fixedNow); !errors.Is(err, errSourceMismatch) {
		t.Errorf("other org err = %v, want mismatch", err)
	}
	if _, _, err := mapEvent(store.GitHubSourceApp, "x", "ping", "d", fixture(t, "ping"), fixedNow); err != nil {
		t.Errorf("app ping (sent before any installation) rejected: %v", err)
	}
	noInstall := []byte(`{"action":"opened","sender":{"login":"a","type":"User"},"issue":{"number":1,"title":"t"},
		"repository":{"name":"r","full_name":"a/r","owner":{"login":"a"}}}`)
	if _, _, err := mapEvent(store.GitHubSourceApp, "x", "issues", "d", noInstall, fixedNow); err == nil {
		t.Error("app source accepted a delivery without an installation")
	}
	if _, _, err := mapEvent(store.GitHubSourceApp, "x", "issue_comment", "d", []byte(`{"sender":`), fixedNow); err == nil {
		t.Error("invalid json accepted")
	}
	if _, _, err := mapEvent(store.GitHubSourceApp, "x", "issues", "d", []byte(`{"action":"opened","sender":{"login":"a"},"installation":{"id":1}}`), fixedNow); err == nil {
		t.Error("issues event without issue accepted")
	}
}

func TestMapEvent_DropsBots(t *testing.T) {
	_, drop, err := mapEvent(store.GitHubSourceApp, "x", "issue_comment", "d", fixture(t, "issue_comment_bot"), fixedNow)
	if err != nil || drop != "bot" {
		t.Fatalf("drop=%q err=%v", drop, err)
	}
}

func TestMapEvent_UnknownEventLandsOnRepo(t *testing.T) {
	body := []byte(`{"action":"created","sender":{"login":"kronael","type":"User"},"installation":{"id":1},
		"repository":{"name":"arizuko","full_name":"kronael/arizuko","owner":{"login":"kronael"}}}`)
	msg, _, err := mapEvent(store.GitHubSourceApp, "x", "release", "d1", body, fixedNow)
	if err != nil || msg.ChatJID != "github:kronael/arizuko" || msg.Verb != "release" || msg.Content != "kronael release created" || msg.ID != "gh-dd1" {
		t.Fatalf("release: %+v err=%v", msg, err)
	}
	orgOnly := []byte(`{"action":"member_added","sender":{"login":"kronael","type":"User"},"installation":{"id":1},"organization":{"login":"kronael-org"}}`)
	msg, _, err = mapEvent(store.GitHubSourceApp, "x", "organization", "d2", orgOnly, fixedNow)
	if err != nil || msg.ChatJID != "github:kronael-org" || msg.Topic != "" {
		t.Fatalf("organization: %+v err=%v", msg, err)
	}
	if _, _, err := mapEvent(store.GitHubSourceApp, "x", "organization", "", orgOnly, fixedNow); err != nil {
		t.Fatalf("missing delivery id: %v", err)
	}
}

package main

import (
	"errors"
	"fmt"
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
	msg, drop, err := mapEvent(kind, name, event, "d-"+file, fixture(t, file), fixedNow, "")
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
		{"pull_request_review_comment_created", "pull_request_review_comment", "github:kronael/arizuko/pr/12", "message", "pr/12", "gh-rc5678901234",
			"github:kronael", "gh-rc5678901200", "on gitd/verify.go:\n\nConstant-time compare — good. Also reject an empty secret.",
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
	a.Verb, b.Verb = "mention", "mention"
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

// GitHub keeps a pull request's assignees in the issue object, so a general
// comment on a PR assigned to the bot reads issue.assignees. This payload is
// issue_comment_created with the bot assigned and no @-mention in the text.
func TestMapEvent_PRCommentReadsIssueAssignees(t *testing.T) {
	raw := fixture(t, "issue_comment_created_assigned")
	cases := []struct {
		name, login, verb string
	}{
		{"bot assigned", botLogin, "mention"},
		{"another login", "alice", "message"},
		{"login unresolved", "", "message"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			msg, drop, err := mapEvent(store.GitHubSourceRepo, "kronael/arizuko", "issue_comment", "d", raw, fixedNow, c.login)
			if err != nil || drop != "" {
				t.Fatalf("err=%v drop=%q", err, drop)
			}
			if msg.ChatJID != "github:kronael/arizuko/pr/12" {
				t.Errorf("chat_jid = %q, want the pull request", msg.ChatJID)
			}
			if msg.Verb != c.verb {
				t.Errorf("verb = %q, want %q", msg.Verb, c.verb)
			}
		})
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
	if got := resolve(routes, opened); got.Folder != "eng" || got.Mode != "observe" {
		t.Errorf("unassigned PR opened resolved to %+v, want eng#observe", got)
	}
	comment := mapFixture(t, store.GitHubSourceRepo, "kronael/arizuko", "issue_comment", "issue_comment_created")
	if got := resolve(routes, comment); got.Folder != "eng" || got.Mode != "observe" {
		t.Errorf("comment on an unassigned PR resolved to %+v, want eng#observe", got)
	}
	assignedComment, drop, err := mapEvent(store.GitHubSourceRepo, "kronael/arizuko", "issue_comment", "d",
		fixture(t, "issue_comment_created_assigned"), fixedNow, botLogin)
	if err != nil || drop != "" {
		t.Fatalf("err=%v drop=%q", err, drop)
	}
	if got := resolve(routes, assignedComment); got.Folder != "eng" || got.Mode != "" {
		t.Errorf("comment on a PR assigned to the bot resolved to %+v, want eng trigger", got)
	}
	assignedOpened, _, err := mapEvent(store.GitHubSourceRepo, "kronael/arizuko", "pull_request", "d",
		body(`"action":"opened",`+assigned("hello", botLogin)), fixedNow, botLogin)
	if err != nil {
		t.Fatal(err)
	}
	if got := resolve(routes, assignedOpened); got.Folder != "eng" || got.Mode != "" {
		t.Errorf("PR opened assigned to the bot resolved to %+v, want eng trigger", got)
	}
	named, _, err := mapEvent(store.GitHubSourceRepo, "kronael/arizuko", "issue_comment", "d",
		body(`"action":"created","issue":{"number":9,"title":"t"},"comment":{"id":1,"body":"@arizuko-bot look"}`), fixedNow, botLogin)
	if err != nil {
		t.Fatal(err)
	}
	if got := resolve(routes, named); got.Folder != "eng" || got.Mode != "" {
		t.Errorf("comment naming the bot resolved to %+v, want eng trigger", got)
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
	if _, _, err := mapEvent(store.GitHubSourceRepo, "kronael/arizuko", "issue_comment", "d", arizuko, fixedNow, ""); err != nil {
		t.Errorf("own repo rejected: %v", err)
	}
	if _, _, err := mapEvent(store.GitHubSourceRepo, "Kronael/Arizuko", "issue_comment", "d", arizuko, fixedNow, ""); err != nil {
		t.Errorf("case-insensitive repo rejected: %v", err)
	}
	if _, _, err := mapEvent(store.GitHubSourceRepo, "kronael/crackbox", "issue_comment", "d", arizuko, fixedNow, ""); !errors.Is(err, errSourceMismatch) {
		t.Errorf("other repo err = %v, want mismatch", err)
	}
	if _, _, err := mapEvent(store.GitHubSourceOrg, "kronael", "issue_comment", "d", arizuko, fixedNow, ""); err != nil {
		t.Errorf("own org rejected: %v", err)
	}
	if _, _, err := mapEvent(store.GitHubSourceOrg, "rhias", "issue_comment", "d", arizuko, fixedNow, ""); !errors.Is(err, errSourceMismatch) {
		t.Errorf("other org err = %v, want mismatch", err)
	}
	if _, _, err := mapEvent(store.GitHubSourceApp, "x", "ping", "d", fixture(t, "ping"), fixedNow, ""); err != nil {
		t.Errorf("app ping (sent before any installation) rejected: %v", err)
	}
	noInstall := []byte(`{"action":"opened","sender":{"login":"a","type":"User"},"issue":{"number":1,"title":"t"},
		"repository":{"name":"r","full_name":"a/r","owner":{"login":"a"}}}`)
	if _, _, err := mapEvent(store.GitHubSourceApp, "x", "issues", "d", noInstall, fixedNow, ""); err == nil {
		t.Error("app source accepted a delivery without an installation")
	}
	if _, _, err := mapEvent(store.GitHubSourceApp, "x", "issue_comment", "d", []byte(`{"sender":`), fixedNow, ""); err == nil {
		t.Error("invalid json accepted")
	}
	if _, _, err := mapEvent(store.GitHubSourceApp, "x", "issues", "d", []byte(`{"action":"opened","sender":{"login":"a"},"installation":{"id":1}}`), fixedNow, ""); err == nil {
		t.Error("issues event without issue accepted")
	}
}

func TestMapEvent_DropsBots(t *testing.T) {
	_, drop, err := mapEvent(store.GitHubSourceApp, "x", "issue_comment", "d", fixture(t, "issue_comment_bot"), fixedNow, "")
	if err != nil || drop != "bot" {
		t.Fatalf("drop=%q err=%v", drop, err)
	}
}

func TestMapEvent_UnknownEventLandsOnRepo(t *testing.T) {
	body := []byte(`{"action":"created","sender":{"login":"kronael","type":"User"},"installation":{"id":1},
		"repository":{"name":"arizuko","full_name":"kronael/arizuko","owner":{"login":"kronael"}}}`)
	msg, _, err := mapEvent(store.GitHubSourceApp, "x", "release", "d1", body, fixedNow, "")
	if err != nil || msg.ChatJID != "github:kronael/arizuko" || msg.Verb != "release" || msg.Content != "kronael release created" || msg.ID != "gh-dd1" {
		t.Fatalf("release: %+v err=%v", msg, err)
	}
	orgOnly := []byte(`{"action":"member_added","sender":{"login":"kronael","type":"User"},"installation":{"id":1},"organization":{"login":"kronael-org"}}`)
	msg, _, err = mapEvent(store.GitHubSourceApp, "x", "organization", "d2", orgOnly, fixedNow, "")
	if err != nil || msg.ChatJID != "github:kronael-org" || msg.Topic != "" {
		t.Fatalf("organization: %+v err=%v", msg, err)
	}
	if _, _, err := mapEvent(store.GitHubSourceApp, "x", "organization", "", orgOnly, fixedNow, ""); err != nil {
		t.Fatalf("missing delivery id: %v", err)
	}
}

// TestMapEvent_AppAcceptsRepoWhenFlagged pins the GITHUB_APP_ACCEPTS_REPO
// bypass: an App-kind source takes a plain repository delivery, which carries
// no installation, and the JID still names the repository. BUGS L409.
func TestMapEvent_AppAcceptsRepoWhenFlagged(t *testing.T) {
	noInstall := []byte(`{"action":"created","sender":{"login":"a","type":"User"},
		"issue":{"number":7,"title":"t"},"comment":{"body":"hello"},
		"repository":{"name":"r","full_name":"a/r","owner":{"login":"a"}}}`)

	if _, _, err := mapEvent(store.GitHubSourceApp, "x", "issue_comment", "d", noInstall, fixedNow, ""); err == nil {
		t.Fatal("app source accepted a delivery without an installation while the flag is off")
	}

	appAcceptsRepo = true
	t.Cleanup(func() { appAcceptsRepo = false })

	msg, drop, err := mapEvent(store.GitHubSourceApp, "x", "issue_comment", "d", noInstall, fixedNow, "")
	if err != nil || drop != "" {
		t.Fatalf("flagged delivery refused: drop=%q err=%v", drop, err)
	}
	if want := "github:a/r/issue/7"; msg.ChatJID != want {
		t.Errorf("ChatJID = %q, want %q", msg.ChatJID, want)
	}
}

const botLogin = "arizuko-bot"

// body builds a webhook payload around one event's own fields.
func body(extra string) []byte {
	return []byte(`{` + extra + `,"sender":{"login":"rhias","type":"User"},"installation":{"id":1},
		"repository":{"name":"arizuko","full_name":"kronael/arizuko","owner":{"login":"kronael"}}}`)
}

const prFields = `"pull_request":{"number":5,"title":"t","body":"%s","head":{"ref":"a"},"base":{"ref":"main"}}`

// prAssigned is prFields with the assignee set GitHub sends on every
// pull_request-carrying event.
const prAssigned = `"pull_request":{"number":5,"title":"t","body":"%s","head":{"ref":"a"},"base":{"ref":"main"},"assignees":[{"login":"%s","type":"User"}]}`

func assigned(prBody, login string) string {
	return fmt.Sprintf(prAssigned, prBody, login)
}

// issueAssigned is the issue object of an issue_comment or issues delivery
// with an assignee set. GitHub keeps a pull request's assignees here too.
func issueAssigned(login string) string {
	return `"issue":{"number":9,"title":"t","assignees":[{"login":"` + login + `","type":"User"}]}`
}

// What an event IS decides its verb: being asked wakes the agent (mention),
// ambient repository traffic does not (the GitHub event name).
func TestMapEvent_VerbIsWhatTheEventIs(t *testing.T) {
	pr := func(body string) string { return strings.Replace(prFields, "%s", body, 1) }
	cases := []struct {
		name, event, payload, verb string
	}{
		{"review requested from the bot", "pull_request",
			`"action":"review_requested","requested_reviewer":{"login":"arizuko-bot"},` + pr(""), "mention"},
		{"review requested from the bot, other case", "pull_request",
			`"action":"review_requested","requested_reviewer":{"login":"Arizuko-Bot"},` + pr(""), "mention"},
		{"review requested from someone else", "pull_request",
			`"action":"review_requested","requested_reviewer":{"login":"alice"},` + pr(""), "pull_request"},
		{"review requested from a login holding the bot's name", "pull_request",
			`"action":"review_requested","requested_reviewer":{"login":"arizuko-bot2"},` + pr(""), "pull_request"},
		{"review requested from a team", "pull_request",
			`"action":"review_requested","requested_team":{"name":"arizuko-bot"},` + pr(""), "pull_request"},
		{"review request removed from the bot", "pull_request",
			`"action":"review_request_removed","requested_reviewer":{"login":"arizuko-bot"},` + pr(""), "pull_request"},
		{"PR assigned to the bot", "pull_request",
			`"action":"assigned","assignee":{"login":"arizuko-bot"},` + pr(""), "mention"},
		{"PR assigned to someone else", "pull_request",
			`"action":"assigned","assignee":{"login":"alice"},` + pr(""), "pull_request"},
		{"issue assigned to the bot", "issues",
			`"action":"assigned","assignee":{"login":"arizuko-bot"},"issue":{"number":9,"title":"t"}`, "mention"},
		{"issue assigned to someone else", "issues",
			`"action":"assigned","assignee":{"login":"alice"},"issue":{"number":9,"title":"t"}`, "issues"},
		{"PR unassigned from the bot", "pull_request",
			`"action":"unassigned","assignee":{"login":"arizuko-bot"},` + pr(""), "pull_request"},
		{"PR synchronized", "pull_request", `"action":"synchronize",` + pr(""), "pull_request"},
		{"PR labeled", "pull_request", `"action":"labeled",` + pr(""), "pull_request"},
		{"PR closed unmerged", "pull_request", `"action":"closed",` + pr(""), "pull_request"},
		{"PR opened", "pull_request", `"action":"opened",` + pr("hello"), "message"},
		{"PR opened naming the bot", "pull_request", `"action":"opened",` + pr("cc @arizuko-bot please look"), "mention"},
		{"PR opened naming a longer login", "pull_request", `"action":"opened",` + pr("cc @arizuko-bot2 please look"), "message"},
		{"issue opened naming the bot", "issues",
			`"action":"opened","issue":{"number":9,"title":"t","body":"@arizuko-bot ?"}`, "mention"},
		{"issue labeled naming the bot", "issues",
			`"action":"labeled","issue":{"number":9,"title":"t","body":"@arizuko-bot ?"}`, "issues"},
		{"comment", "issue_comment",
			`"action":"created","issue":{"number":9,"title":"t"},"comment":{"id":1,"body":"hello"}`, "message"},
		{"comment naming the bot", "issue_comment",
			`"action":"created","issue":{"number":9,"title":"t"},"comment":{"id":1,"body":"hey @arizuko-bot, look"}`, "mention"},
		{"comment naming the bot in other case", "issue_comment",
			`"action":"created","issue":{"number":9,"title":"t"},"comment":{"id":1,"body":"@ARIZUKO-BOT look"}`, "mention"},
		{"comment ending on the bot's name", "issue_comment",
			`"action":"created","issue":{"number":9,"title":"t"},"comment":{"id":1,"body":"ping @arizuko-bot"}`, "mention"},
		{"comment naming a longer login", "issue_comment",
			`"action":"created","issue":{"number":9,"title":"t"},"comment":{"id":1,"body":"hey @arizuko-bot2 look"}`, "message"},
		{"comment naming a longer login with a suffix", "issue_comment",
			`"action":"created","issue":{"number":9,"title":"t"},"comment":{"id":1,"body":"hey @arizuko-botany look"}`, "message"},
		{"comment naming a hyphenated longer login", "issue_comment",
			`"action":"created","issue":{"number":9,"title":"t"},"comment":{"id":1,"body":"hey @arizuko-bot-helper look"}`, "message"},
		{"comment naming a login the bot's name ends", "issue_comment",
			`"action":"created","issue":{"number":9,"title":"t"},"comment":{"id":1,"body":"hey @not-arizuko-bot look"}`, "message"},
		{"comment with an address", "issue_comment",
			`"action":"created","issue":{"number":9,"title":"t"},"comment":{"id":1,"body":"mail me@arizuko-bot.dev"}`, "message"},
		{"comment without the @", "issue_comment",
			`"action":"created","issue":{"number":9,"title":"t"},"comment":{"id":1,"body":"arizuko-bot look"}`, "message"},
		{"edited comment naming the bot", "issue_comment",
			`"action":"edited","issue":{"number":9,"title":"t"},"comment":{"id":1,"body":"@arizuko-bot"}`, "issue_comment"},
		{"review comment naming the bot", "pull_request_review_comment",
			`"action":"created",` + pr("") + `,"comment":{"id":2,"path":"a.go","body":"@arizuko-bot why?"}`, "mention"},
		{"review comment", "pull_request_review_comment",
			`"action":"created",` + pr("") + `,"comment":{"id":2,"path":"a.go","body":"why?"}`, "message"},
		{"review naming the bot", "pull_request_review",
			`"action":"submitted",` + pr("") + `,"review":{"id":3,"state":"commented","body":"@arizuko-bot fix"}`, "mention"},
		{"review with text", "pull_request_review",
			`"action":"submitted",` + pr("") + `,"review":{"id":3,"state":"commented","body":"fix"}`, "message"},
		{"comment on an issue assigned to the bot", "issue_comment",
			`"action":"created",` + issueAssigned("arizuko-bot") + `,"comment":{"id":1,"body":"hello"}`, "mention"},
		{"comment on an issue assigned to someone else", "issue_comment",
			`"action":"created",` + issueAssigned("alice") + `,"comment":{"id":1,"body":"hello"}`, "message"},
		{"comment on an unassigned issue", "issue_comment",
			`"action":"created","issue":{"number":9,"title":"t","assignees":[]},"comment":{"id":1,"body":"hello"}`, "message"},
		{"comment on an issue assigned to the bot in other case", "issue_comment",
			`"action":"created",` + issueAssigned("Arizuko-Bot") + `,"comment":{"id":1,"body":"hello"}`, "mention"},
		{"comment on an issue assigned to a longer login", "issue_comment",
			`"action":"created",` + issueAssigned("arizuko-bot2") + `,"comment":{"id":1,"body":"hello"}`, "message"},
		{"edited comment on an issue assigned to the bot", "issue_comment",
			`"action":"edited",` + issueAssigned("arizuko-bot") + `,"comment":{"id":1,"body":"hello"}`, "issue_comment"},
		{"issue opened already assigned to the bot", "issues",
			`"action":"opened",` + issueAssigned("arizuko-bot") + `,"assignee":{"login":"arizuko-bot"}`, "mention"},
		{"issue labeled while assigned to the bot", "issues",
			`"action":"labeled",` + issueAssigned("arizuko-bot"), "issues"},
		{"review comment on a PR assigned to the bot", "pull_request_review_comment",
			`"action":"created",` + assigned("", "arizuko-bot") + `,"comment":{"id":2,"path":"a.go","body":"why?"}`, "mention"},
		{"review comment on a PR assigned to someone else", "pull_request_review_comment",
			`"action":"created",` + assigned("", "alice") + `,"comment":{"id":2,"path":"a.go","body":"why?"}`, "message"},
		{"review with text on a PR assigned to the bot", "pull_request_review",
			`"action":"submitted",` + assigned("", "arizuko-bot") + `,"review":{"id":3,"state":"commented","body":"fix"}`, "mention"},
		{"review without text on a PR assigned to the bot", "pull_request_review",
			`"action":"submitted",` + assigned("", "arizuko-bot") + `,"review":{"id":3,"state":"approved","body":""}`, "pull_request_review"},
		{"PR opened already assigned to the bot", "pull_request",
			`"action":"opened",` + assigned("hello", "arizuko-bot"), "mention"},
		{"PR opened assigned to someone else", "pull_request",
			`"action":"opened",` + assigned("hello", "alice"), "message"},
		{"PR synchronized while assigned to the bot", "pull_request",
			`"action":"synchronize",` + assigned("", "arizuko-bot"), "pull_request"},
		{"PR labeled while assigned to the bot", "pull_request",
			`"action":"labeled",` + assigned("", "arizuko-bot"), "pull_request"},
		{"PR closed unmerged while assigned to the bot", "pull_request",
			`"action":"closed",` + assigned("", "arizuko-bot"), "pull_request"},
		{"approval without text", "pull_request_review",
			`"action":"submitted",` + pr("") + `,"review":{"id":3,"state":"approved","body":""}`, "pull_request_review"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			msg, drop, err := mapEvent(store.GitHubSourceApp, "x", c.event, "d", body(c.payload), fixedNow, botLogin)
			if err != nil || drop != "" {
				t.Fatalf("err=%v drop=%q", err, drop)
			}
			if msg.Verb != c.verb {
				t.Errorf("verb = %q, want %q", msg.Verb, c.verb)
			}
		})
	}
}

// Without a login (ingress-only gitd) nothing is a mention, and an empty login
// does not match an empty reviewer.
func TestMapEvent_NoLoginNoMention(t *testing.T) {
	for event, payload := range map[string]string{
		"pull_request":  `"action":"review_requested","requested_reviewer":{"login":""},` + strings.Replace(prFields, "%s", "@ cc", 1),
		"issue_comment": `"action":"created","issue":{"number":9,"title":"t"},"comment":{"id":1,"body":"@ hi"}`,
	} {
		msg, _, err := mapEvent(store.GitHubSourceApp, "x", event, "d", body(payload), fixedNow, "")
		if err != nil || msg.Verb == "mention" {
			t.Errorf("%s: verb=%q err=%v", event, msg.Verb, err)
		}
	}
}

// An unresolved login makes assignment unreadable too: a conversation assigned
// to the bot, or to an account with an empty login, is a message.
func TestMapEvent_NoLoginAssignedIsMessage(t *testing.T) {
	for _, login := range []string{"arizuko-bot", ""} {
		payload := `"action":"created",` + issueAssigned(login) + `,"comment":{"id":1,"body":"hello"}`
		msg, _, err := mapEvent(store.GitHubSourceApp, "x", "issue_comment", "d", body(payload), fixedNow, "")
		if err != nil || msg.Verb != "message" {
			t.Errorf("assignee %q: verb=%q err=%v, want message", login, msg.Verb, err)
		}
	}
}

// A mention reaches the folder through the rows `add` seeds, and the ambient
// events beside it still observe.
func TestMapEvent_MentionTriggersAmbientObserves(t *testing.T) {
	routes, err := store.GitHubSourceRoutes("app/x", "eng")
	if err != nil {
		t.Fatal(err)
	}
	for action, want := range map[string]string{"review_requested": "", "synchronize": "observe"} {
		payload := `"action":"` + action + `","requested_reviewer":{"login":"arizuko-bot"},` + strings.Replace(prFields, "%s", "", 1)
		msg, _, err := mapEvent(store.GitHubSourceApp, "x", "pull_request", "d", body(payload), fixedNow, botLogin)
		if err != nil {
			t.Fatal(err)
		}
		if got := resolve(routes, msg); got.Folder != "eng" || got.Mode != want {
			t.Errorf("%s resolved to %+v, want eng mode %q", action, got, want)
		}
	}
}

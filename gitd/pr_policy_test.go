package main

import (
	"strings"
	"testing"

	"github.com/kronael/arizuko/store"
)

func TestExplicitAskPolicyRoutesOnlyEligibleRequests(t *testing.T) {
	routes, err := store.GitHubSourceRoutes("repo/kronael/arizuko", "eng")
	if err != nil {
		t.Fatal(err)
	}
	pr := strings.Replace(prFields, "%s", "", 1)
	issuePR := `"issue":{"number":5,"pull_request":{"url":"https://api.github.com/repos/kronael/arizuko/pulls/5"}}`
	assignedIssuePR := `"issue":{"number":5,"pull_request":{},"assignees":[{"login":"arizuko-bot"}]}`
	for _, scenario := range []struct {
		name, event, fields string
		trigger             bool
	}{
		{"assigned head update", "pull_request", `"action":"synchronize",` + assigned("", botLogin), false},
		{"assigned label", "pull_request", `"action":"labeled",` + assigned("", botLogin), false},
		{"assigned closure", "pull_request", `"action":"closed",` + assigned("", botLogin), false},
		{"assigned empty approval", "pull_request_review", `"action":"submitted",` + assigned("", botLogin) + `,"review":{"id":7,"state":"approved"}`, false},
		{"foreign assignee", "pull_request", `"action":"synchronize",` + assigned("", "another-bot"), false},
		{"review request", "pull_request", `"action":"review_requested","requested_reviewer":{"login":"arizuko-bot"},` + pr, true},
		{"foreign review request", "pull_request", `"action":"review_requested","requested_reviewer":{"login":"another-bot"},` + pr, false},
		{"foreign request on assigned PR", "pull_request", `"action":"review_requested","requested_reviewer":{"login":"another-bot"},` + assigned("", botLogin), false},
		{"foreign assignment on assigned PR", "pull_request", `"action":"assigned","assignee":{"login":"another-bot"},` + assigned("", botLogin), false},
		{"PR mention", "issue_comment", `"action":"created",` + issuePR + `,"comment":{"id":42,"body":"@arizuko-bot look"}`, true},
		{"PR bystander", "issue_comment", `"action":"created",` + issuePR + `,"comment":{"id":42,"body":"hello"}`, false},
		{"assigned PR bystander", "issue_comment", `"action":"created",` + assignedIssuePR + `,"comment":{"id":42,"body":"hello"}`, false},
		{"assigned inline bystander", "pull_request_review_comment", `"action":"created",` + assigned("", botLogin) + `,"comment":{"id":42,"body":"hello"}`, false},
		{"assigned review discussion", "pull_request_review", `"action":"submitted",` + assigned("", botLogin) + `,"review":{"id":7,"state":"commented","body":"hello"}`, false},
		{"issue mention", "issue_comment", `"action":"created","issue":{"number":5},"comment":{"id":42,"body":"@arizuko-bot look"}`, true},
		{"issue bystander", "issue_comment", `"action":"created","issue":{"number":5},"comment":{"id":42,"body":"hello"}`, false},
		{"issue assignment to the bot", "issues", `"action":"assigned","assignee":{"login":"arizuko-bot"},` + issueAssigned(botLogin), true},
		{"issue assignment to someone else", "issues", `"action":"assigned","assignee":{"login":"alice"},` + issueAssigned("alice"), false},
		{"issue opened", "issues", `"action":"opened","issue":{"number":5,"body":"@arizuko-bot look"}`, true},
		{"issue opened without the bot", "issues", `"action":"opened","issue":{"number":5,"body":"look"}`, false},
		{"discussion mention", "discussion", `"action":"created","discussion":{"title":"t","body":"@arizuko-bot look"}`, true},
		{"discussion comment mention", "discussion_comment", `"action":"created","discussion":{"title":"t"},"comment":{"id":42,"body":"@arizuko-bot look"}`, true},
		{"commit comment mention", "commit_comment", `"action":"created","comment":{"id":42,"body":"@arizuko-bot look"}`, true},
		{"commit comment", "commit_comment", `"action":"created","comment":{"id":42,"body":"look"}`, false},
		{"push naming the bot", "push", `"ref":"refs/heads/x","commits":[{"id":"abc","message":"cc @arizuko-bot"}]`, true},
		{"CI output naming the bot", "check_run", `"action":"completed","check_run":{"name":"ci","status":"completed","conclusion":"failure","output":{"text":"@arizuko-bot look"}}`, true},
		{"CI output", "check_run", `"action":"completed","check_run":{"name":"ci","status":"completed","conclusion":"failure","output":{"text":"3 failed"}}`, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			msg, dropped, err := mapEvent(store.GitHubSourceRepo, "kronael/arizuko", scenario.event, "delivery", body(scenario.fields), fixedNow, botLogin)
			if err != nil || dropped != "" {
				t.Fatalf("Mapping failed: %q %v", dropped, err)
			}
			got := resolve(routes, msg)
			if got.Folder != "eng" || (got.Mode != "observe") != scenario.trigger {
				t.Fatalf("Trigger=%t resolved verb=%q to %+v", scenario.trigger, msg.Verb, got)
			}
		})
	}
}

func TestCommentActionDeliveryIdentitySurvivesOriginalObject(t *testing.T) {
	for _, event := range []string{"issue_comment", "pull_request_review_comment", "pull_request_review"} {
		t.Run(event, func(t *testing.T) {
			fields := assigned("", botLogin) + `,"issue":{"number":5,"pull_request":{}},"comment":{"id":42,"body":"changed","created_at":"2026-09-04T12:00:00Z","updated_at":"2026-09-05T12:00:00Z"},"review":{"id":7,"body":"changed","submitted_at":"2026-09-04T12:00:00Z"}`
			actions := []string{"edited", "deleted"}
			if event == "pull_request_review" {
				actions = []string{"edited", "dismissed"}
			}
			for _, action := range actions {
				raw := body(`"action":"` + action + `",` + fields)
				first, _, err := mapEvent(store.GitHubSourceRepo, "kronael/arizuko", event, "action-1", raw, fixedNow, botLogin)
				if err != nil {
					t.Fatal(err)
				}
				replay, _, err := mapEvent(store.GitHubSourceRepo, "kronael/arizuko", event, "action-1", raw, fixedNow, botLogin)
				if err != nil || replay.ID != first.ID {
					t.Fatal("Delivery replay changed identity", err)
				}
				second, _, err := mapEvent(store.GitHubSourceRepo, "kronael/arizuko", event, "action-2", raw, fixedNow, botLogin)
				if err != nil || first.ID != "gh-daction-1" || second.ID != "gh-daction-2" {
					t.Fatalf("Distinct actions aliased comment identity: %q %q %v", first.ID, second.ID, err)
				}
				if first.Timestamp != fixedNow.Unix() {
					t.Fatal("A new action retained the original comment timestamp")
				}
			}
		})
	}
}

func TestAutomatedPRStateIsObservedWithoutCommentLoops(t *testing.T) {
	e := newTestEnv(t, secretMap{"repo/kronael/arizuko": "fixture-secret"})
	e.srv.bot.login.Store(botLogin)
	for _, scenario := range []struct{ name, event, fields, want string }{
		{"closure of an assigned PR", "pull_request", `"action":"closed",` + assigned("", botLogin), "pull_request"},
		{"head update of an assigned PR", "pull_request", `"action":"synchronize",` + assigned("", botLogin), "pull_request"},
		{"closure of another's PR", "pull_request", `"action":"closed",` + assigned("", "another-bot"), "pull_request"},
		{"bot body naming the bot", "pull_request", `"action":"opened",` + assigned("@arizuko-bot look", "another-bot"), "pull_request"},
		{"automated review request", "pull_request", `"action":"review_requested","requested_reviewer":{"login":"arizuko-bot"},` + assigned("", "another-bot"), "mention"},
		{"bot comment", "pull_request_review_comment", `"action":"created",` + assigned("", botLogin) + `,"comment":{"id":42,"body":"@arizuko-bot look"}`, ""},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			for _, login := range []string{"automation-bot", botLogin} {
				raw := strings.Replace(string(body(scenario.fields)), `"sender":{"login":"rhias","type":"User"}`, `"sender":{"login":"`+login+`","type":"Bot"}`, 1)
				e.routd.mu.Lock()
				e.routd.got = nil
				e.routd.mu.Unlock()
				got := e.deliver(t, "/github/repo/kronael/arizuko", "fixture-secret", scenario.event, "automation", []byte(raw))
				messages := e.routd.messages()
				want := scenario.want
				if login == botLogin && want != "" {
					want = scenario.event
				}
				if got.Code != 200 || (want == "" && len(messages) != 0) ||
					(want != "" && (len(messages) != 1 || messages[0].Verb != want)) {
					t.Fatalf("login=%s status=%d messages=%+v", login, got.Code, messages)
				}
			}
		})
	}
}

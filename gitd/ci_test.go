package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kronael/arizuko/chanlib"
)

const ciURL = "https://github.com/kronael/arizuko/runs/1"

func ciBody(t *testing.T, event, action, status string, numbers []int) []byte {
	t.Helper()
	prs := make([]map[string]int, 0, len(numbers))
	for _, number := range numbers {
		prs = append(prs, map[string]int{"number": number})
	}
	raw, err := json.Marshal(map[string]any{
		"action": action, "sender": map[string]string{"login": "actions-bot", "type": "Bot"},
		"repository": map[string]any{"full_name": "kronael/arizuko", "name": "arizuko", "owner": map[string]string{"login": "kronael"}},
		event:        map[string]any{"status": status, "conclusion": "failure", "name": "test", "html_url": ciURL, "pull_requests": prs},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestCIFanoutAcknowledgesMixedDeliveryOutcomes(t *testing.T) {
	for _, scenario := range []struct {
		name                    string
		base, pr, want, pending int
		lost                    bool
	}{
		{"permanent then retained", 400, 500, 202, 1, false},
		{"retained then permanent", 500, 400, 202, 1, false},
		{"permanent then lost", 400, 500, 503, 0, true},
		{"lost then permanent", 500, 400, 503, 0, true},
		{"only permanent failure", 400, 200, 400, 0, false},
		{"all permanent", 400, 400, 400, 0, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			e := newTestEnv(t, secretMap{"repo/kronael/arizuko": "fixture-secret"})
			e.srv.bot.login.Store(botLogin)
			e.routd.statusFor = func(m chanlib.InboundMsg) int {
				if m.Topic == "pr/5" {
					return scenario.pr
				}
				return scenario.base
			}
			if scenario.lost {
				dropGitdEntities()
			}
			got := e.deliver(t, "/github/repo/kronael/arizuko", "fixture-secret", "check_run", "mixed", ciBody(t, "check_run", "completed", "completed", []int{5}))
			ids := map[string]bool{}
			for _, message := range e.routd.messages() {
				ids[message.ID] = true
			}
			if got.Code != scenario.want || len(ids) != 2 {
				t.Fatalf("status=%d want=%d messages=%+v body=%s", got.Code, scenario.want, e.routd.messages(), got.Body.String())
			}
			if !scenario.lost {
				if n, err := chanlib.PendingInboundCount(t.Context(), e.srv.chanStore); err != nil || n != scenario.pending {
					t.Fatalf("retained=%d want=%d err=%v", n, scenario.pending, err)
				}
			}
			if accepted := e.srv.bot.LastInboundAt() != 0; accepted != (scenario.want == 202) {
				t.Fatalf("accepted source status=%t for %d", accepted, got.Code)
			}
		})
	}
}

// botIsResponsibleAPI answers every GitHub lookup as if the bot were an assignee,
// a requested reviewer and a past reviewer of the PR, and counts the calls.
func botIsResponsibleAPI(t *testing.T, e *testEnv) *atomic.Int32 {
	t.Helper()
	var calls atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if strings.HasSuffix(r.URL.Path, "/reviews") {
			_, _ = w.Write([]byte(`[{"user":{"login":"arizuko-bot"},"submitted_at":"2026-01-01"}]`))
			return
		}
		_, _ = w.Write([]byte(`{"number":5,"assignees":[{"login":"arizuko-bot"}],"requested_reviewers":[{"login":"arizuko-bot"}],"base":{"repo":{"full_name":"kronael/arizuko"}}}`))
	}))
	t.Cleanup(api.Close)
	e.srv.bot.cfg.APIBase, e.srv.bot.cfg.Token = api.URL, "fixture-token"
	e.srv.bot.login.Store(botLogin)
	return &calls
}

func (e *testEnv) resetDeliveries() {
	e.routd.mu.Lock()
	e.routd.got = nil
	e.routd.mu.Unlock()
	e.srv.bot.lastInboundAt.Store(0)
	e.srv.bot.deliveries.Delete("repo/kronael/arizuko")
}

func TestCIWebhookFansOutObservationsWithoutLookup(t *testing.T) {
	e := newTestEnv(t, secretMap{"repo/kronael/arizuko": "fixture-secret"})
	calls := botIsResponsibleAPI(t, e)
	for _, scenario := range []struct {
		name, event, action, status string
		numbers                     []int
		code, messages              int
	}{
		{"completed check", "check_run", "completed", "completed", []int{5}, 200, 2},
		{"completed suite", "check_suite", "completed", "completed", []int{5}, 200, 2},
		{"completed workflow", "workflow_run", "completed", "completed", []int{5}, 200, 2},
		{"unrelated PR", "check_run", "completed", "completed", []int{6}, 200, 2},
		{"unassociated", "check_run", "completed", "completed", nil, 200, 1},
		{"queued", "check_run", "created", "queued", []int{5}, 200, 1},
		{"in progress", "workflow_run", "in_progress", "in_progress", []int{5}, 200, 1},
		{"deduplicated associations", "check_run", "completed", "completed", []int{5, 6, 5}, 200, 3},
		{"fanout bound", "check_run", "completed", "completed", []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}, 400, 0},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			e.resetDeliveries()
			raw := ciBody(t, scenario.event, scenario.action, scenario.status, scenario.numbers)
			got := e.deliver(t, "/github/repo/kronael/arizuko", "fixture-secret", scenario.event, "ci-delivery", raw)
			messages := e.routd.messages()
			if got.Code != scenario.code || len(messages) != scenario.messages {
				t.Fatalf("status=%d messages=%d; %s", got.Code, len(messages), got.Body.String())
			}
			for i, message := range messages {
				wantContent := fmt.Sprintf("[github kind=%s id=%s url=%s]\n\n%s %s: test (failure)", scenario.event, message.ID, ciURL, scenario.event, scenario.action)
				if message.Content != wantContent {
					t.Fatalf("CI content\n got %q\nwant %q", message.Content, wantContent)
				}
				if message.Verb != scenario.event {
					t.Fatalf("CI row %d has verb %q, want %q", i, message.Verb, scenario.event)
				}
				if i > 0 {
					number := scenario.numbers[i-1]
					if message.ChatJID != fmt.Sprintf("github:kronael/arizuko/pr/%d", number) || message.ID != fmt.Sprintf("gh-dci-delivery-pr%d", number) {
						t.Fatal("CI row escaped its associated PR", message)
					}
				}
			}
			if scenario.code == 400 && e.srv.bot.LastInboundAt() != 0 {
				t.Fatal("Refused CI delivery counted as accepted")
			}
		})
	}
	raw := ciBody(t, "check_run", "completed", "completed", []int{5})
	for _, foreign := range []string{
		strings.Replace(string(raw), `"full_name":"kronael/arizuko"`, `"full_name":"another/repository"`, 1),
		strings.Replace(string(raw), `"name":"arizuko"`, `"name":"foreign"`, 1),
	} {
		if got := e.deliver(t, "/github/repo/kronael/arizuko", "fixture-secret", "check_run", "foreign", []byte(foreign)); got.Code != 400 {
			t.Fatal("Foreign repository source was accepted", got.Body.String())
		}
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("CI deliveries made %d GitHub API calls", n)
	}
}

// Assignment, a pending review request and a past review make the bot
// "responsible" for a PR; none of them is an ask, so pushes, review states,
// edits and CI results on that PR stay observations. The explicit asks still
// start a turn.
func TestOnlyExplicitAsksAreMentions(t *testing.T) {
	e := newTestEnv(t, secretMap{"repo/kronael/arizuko": "fixture-secret"})
	calls := botIsResponsibleAPI(t, e)
	pr := strings.Replace(prFields, "%s", "", 1)
	issuePR := `"issue":{"number":5,"pull_request":{}}`
	for _, scenario := range []struct{ name, event, fields, verb string }{
		{"push to a PR", "pull_request", `"action":"synchronize",` + pr, "pull_request"},
		{"push to an assigned PR", "pull_request", `"action":"synchronize",` + assigned("", botLogin), "pull_request"},
		{"label on an assigned PR", "pull_request", `"action":"labeled",` + assigned("", botLogin), "pull_request"},
		{"closure of an assigned PR", "pull_request", `"action":"closed",` + assigned("", botLogin), "pull_request"},
		{"approval", "pull_request_review", `"action":"submitted",` + pr + `,"review":{"id":7,"state":"approved","body":""}`, "pull_request_review"},
		{"thread resolved", "pull_request_review_thread", `"action":"resolved",` + pr, "pull_request_review_thread"},
		{"comment edit", "issue_comment", `"action":"edited",` + issuePR + `,"comment":{"id":42,"body":"corrected"}`, "issue_comment"},
		{"comment deletion", "issue_comment", `"action":"deleted",` + issuePR + `,"comment":{"id":42}`, "issue_comment"},
		{"plain comment", "issue_comment", `"action":"created",` + issuePR + `,"comment":{"id":42,"body":"please fix this"}`, "message"},
		{"plain inline reply", "pull_request_review_comment", `"action":"created",` + pr + `,"comment":{"id":42,"body":"reply","in_reply_to_id":21}`, "message"},
		{"review request to someone else", "pull_request", `"action":"review_requested","requested_reviewer":{"login":"alice"},` + pr, "pull_request"},
		{"review request to the bot", "pull_request", `"action":"review_requested","requested_reviewer":{"login":"arizuko-bot"},` + pr, "mention"},
		{"assignment to the bot", "pull_request", `"action":"assigned","assignee":{"login":"arizuko-bot"},` + pr, "mention"},
		{"mention in a comment", "issue_comment", `"action":"created",` + issuePR + `,"comment":{"id":42,"body":"@arizuko-bot please fix this"}`, "mention"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			e.resetDeliveries()
			got := e.deliver(t, "/github/repo/kronael/arizuko", "fixture-secret", scenario.event, "ask", body(scenario.fields))
			messages := e.routd.messages()
			if got.Code != 200 || len(messages) != 1 || messages[0].Verb != scenario.verb {
				t.Fatalf("status=%d messages=%+v, want verb %q", got.Code, messages, scenario.verb)
			}
		})
	}
	for _, event := range []string{"check_run", "check_suite", "workflow_run"} {
		t.Run(event, func(t *testing.T) {
			e.resetDeliveries()
			got := e.deliver(t, "/github/repo/kronael/arizuko", "fixture-secret", event, "ci", ciBody(t, event, "completed", "completed", []int{5}))
			messages := e.routd.messages()
			if got.Code != 200 || len(messages) != 2 || messages[0].Verb != event || messages[1].Verb != event {
				t.Fatalf("status=%d messages=%+v, want verb %q on both rows", got.Code, messages, event)
			}
		})
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("classification made %d GitHub API calls", n)
	}
}

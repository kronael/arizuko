package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

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
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"number":5,"assignees":[{"login":"arizuko-bot"}],"requested_reviewers":[],"base":{"repo":{"full_name":"kronael/arizuko"}}}`))
			}))
			defer api.Close()
			e.srv.bot.cfg.APIBase = api.URL
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

func TestCIWebhookUsesCurrentAssignmentAndBoundedFanout(t *testing.T) {
	e := newTestEnv(t, secretMap{"repo/kronael/arizuko": "fixture-secret"})
	var mu sync.Mutex
	var calls []string
	var fail bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, r.URL.RequestURI())
		if r.Header.Get("Authorization") != "Bearer fixture-token" || r.Method != "GET" {
			t.Error("CI lookup did not use the existing authenticated GET client")
		}
		if fail {
			w.WriteHeader(503)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/reviews") {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		number, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/repos/kronael/arizuko/pulls/"))
		if err != nil {
			t.Error("Lookup escaped the verified repository", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		login := "another-bot"
		if number == 5 || number == 7 {
			login = botLogin
		}
		state := "open"
		if number == 7 {
			state = "closed"
		}
		if _, err := fmt.Fprintf(w, `{"number":%d,"state":%q,"assignees":[{"login":%q}],"requested_reviewers":[],"base":{"repo":{"full_name":"kronael/arizuko"}}}`, number, state, login); err != nil {
			t.Error(err)
		}
	}))
	defer api.Close()
	e.srv.bot.cfg.APIBase, e.srv.bot.cfg.Token = api.URL, "fixture-token"
	e.srv.bot.login.Store(botLogin)
	for _, scenario := range []struct {
		name, event, action, status       string
		numbers                           []int
		code, messages, lookups, mentions int
	}{
		{"completed check", "check_run", "completed", "completed", []int{5}, 200, 2, 1, 1},
		{"completed suite", "check_suite", "completed", "completed", []int{5}, 200, 2, 1, 1},
		{"completed workflow", "workflow_run", "completed", "completed", []int{5}, 200, 2, 1, 1},
		{"foreign assignee", "check_run", "completed", "completed", []int{6}, 200, 2, 2, 0},
		{"closed watched PR", "check_run", "completed", "completed", []int{7}, 200, 2, 1, 1},
		{"unassociated", "check_run", "completed", "completed", nil, 200, 1, 0, 0},
		{"queued", "check_run", "created", "queued", []int{5}, 200, 1, 0, 0},
		{"in progress", "workflow_run", "in_progress", "in_progress", []int{5}, 200, 1, 0, 0},
		{"deduplicated associations", "check_run", "completed", "completed", []int{5, 6, 5}, 200, 3, 3, 1},
		{"fanout bound", "check_run", "completed", "completed", []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}, 503, 0, 0, 0},
		{"lookup failed", "check_run", "completed", "completed", []int{5}, 503, 0, 1, 0},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			mu.Lock()
			calls, fail = nil, scenario.name == "lookup failed"
			mu.Unlock()
			e.routd.mu.Lock()
			e.routd.got = nil
			e.routd.mu.Unlock()
			e.srv.bot.lastInboundAt.Store(0)
			e.srv.bot.deliveries.Delete("repo/kronael/arizuko")
			raw := ciBody(t, scenario.event, scenario.action, scenario.status, scenario.numbers)
			got := e.deliver(t, "/github/repo/kronael/arizuko", "fixture-secret", scenario.event, "ci-delivery", raw)
			messages := e.routd.messages()
			mu.Lock()
			lookups := len(calls)
			mu.Unlock()
			mentions := 0
			wantPR := 5
			if scenario.name == "closed watched PR" {
				wantPR = 7
			}
			for _, message := range messages {
				wantContent := fmt.Sprintf("[github kind=%s id=%s url=%s]\n\n%s %s: test (failure)", scenario.event, message.ID, ciURL, scenario.event, scenario.action)
				if message.Content != wantContent {
					t.Fatalf("CI content\n got %q\nwant %q", message.Content, wantContent)
				}
				if message.Verb == "mention" {
					mentions++
					if message.ChatJID != fmt.Sprintf("github:kronael/arizuko/pr/%d", wantPR) || message.ID != fmt.Sprintf("gh-dci-delivery-pr%d", wantPR) {
						t.Fatal("CI trigger escaped its associated PR", message)
					}
				}
			}
			if got.Code != scenario.code || len(messages) != scenario.messages || lookups != scenario.lookups || mentions != scenario.mentions {
				t.Fatalf("status=%d messages=%d lookups=%d mentions=%d; %s", got.Code, len(messages), lookups, mentions, got.Body.String())
			}
			if scenario.code == 503 && e.srv.bot.LastInboundAt() != 0 {
				t.Fatal("Failed CI lookup counted as an accepted delivery")
			}
		})
	}
	t.Run("same delivery recovers after lookup failure", func(t *testing.T) {
		raw := ciBody(t, "check_run", "completed", "completed", []int{5})
		mu.Lock()
		fail = true
		mu.Unlock()
		if got := e.deliver(t, "/github/repo/kronael/arizuko", "fixture-secret", "check_run", "retry-delivery", raw); got.Code != 503 {
			t.Fatalf("Failed lookup returned %d", got.Code)
		}
		mu.Lock()
		fail = false
		mu.Unlock()
		for range 2 {
			e.routd.mu.Lock()
			e.routd.got = nil
			e.routd.mu.Unlock()
			got := e.deliver(t, "/github/repo/kronael/arizuko", "fixture-secret", "check_run", "retry-delivery", raw)
			messages := e.routd.messages()
			if got.Code != 200 || len(messages) != 2 || messages[1].ID != "gh-dretry-delivery-pr5" || messages[1].Verb != "mention" {
				t.Fatalf("Recovered delivery/replay lost its PR or identity: %d %+v", got.Code, messages)
			}
		}
	})
	mu.Lock()
	calls, fail = nil, false
	mu.Unlock()
	raw := ciBody(t, "check_run", "completed", "completed", []int{5})
	for _, foreign := range []string{
		strings.Replace(string(raw), `"full_name":"kronael/arizuko"`, `"full_name":"another/repository"`, 1),
		strings.Replace(string(raw), `"name":"arizuko"`, `"name":"foreign"`, 1),
	} {
		if got := e.deliver(t, "/github/repo/kronael/arizuko", "fixture-secret", "check_run", "foreign", []byte(foreign)); got.Code != 400 {
			t.Fatal("Foreign repository source was accepted", got.Body.String())
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 0 {
		t.Fatal("Foreign source reached the GitHub API", calls)
	}
}

func TestPRResponsibilityWebhook(t *testing.T) {
	e := newTestEnv(t, secretMap{"repo/kronael/arizuko": "fixture-secret"})
	e.srv.bot.login.Store(botLogin)
	e.srv.bot.cfg.Token = "fixture-token"
	prResponse := `{"number":5,"assignees":[],"requested_reviewers":[],"base":{"repo":{"full_name":"kronael/arizuko"}}}`
	pages := []string{`[]`}
	var calls []string
	timeoutDone := make(chan struct{})
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.RequestURI())
		if r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Error("missing authentication")
		}
		if strings.HasSuffix(r.URL.Path, "/reviews") {
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			if page < 1 || page > len(pages) {
				t.Errorf("unexpected page %d", page)
				w.WriteHeader(500)
				return
			}
			if pages[page-1] == "timeout" {
				<-r.Context().Done()
				close(timeoutDone)
				return
			}
			if pages[page-1] == "refuse" {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write([]byte(pages[page-1]))
			return
		}
		_, _ = w.Write([]byte(prResponse))
	}))
	defer api.Close()
	e.srv.bot.cfg.APIBase = api.URL
	comment := `"action":"created","issue":{"number":5,"pull_request":{}},"comment":{"id":42,"body":"please fix this"}`
	foreign := strings.TrimSuffix(strings.Repeat(`{"user":{"login":"other"},"submitted_at":"2026-01-01"},`, 100), ",")
	for _, scenario := range []struct {
		name, pr string
		pages    []string
		code     int
		mention  bool
		calls    int
	}{
		{"assignee", `{"number":5,"assignees":[{"login":"ARIZUKO-BOT"}],"requested_reviewers":[],"base":{"repo":{"full_name":"kronael/arizuko"}}}`, nil, 200, true, 1},
		{"pending", `{"number":5,"assignees":[],"requested_reviewers":[{"login":"ARIZUKO-BOT"}],"base":{"repo":{"full_name":"kronael/arizuko"}}}`, nil, 200, true, 1},
		{"submitted", "", []string{`[{"user":{"login":"ARIZUKO-BOT"},"submitted_at":"2026-01-01"}]`}, 200, true, 2},
		{"second page", "", []string{"[" + foreign + "]", `[{"user":{"login":"arizuko-bot"},"submitted_at":"2026-01-01"}]`}, 200, true, 3},
		{"absent after full page", "", []string{"[" + foreign + "]", `[]`}, 200, false, 3},
		{"unsubmitted", "", []string{`[{"user":{"login":"arizuko-bot"},"submitted_at":null}]`}, 200, false, 2},
		{"null reviews", "", []string{`null`}, 503, false, 2},
		{"malformed second page", "", []string{"[" + foreign + "]", `{}`}, 503, false, 3},
		{"refused second page", "", []string{"[" + foreign + "]", "refuse"}, 503, false, 3},
		{"wrong PR", `{"number":6,"assignees":[],"requested_reviewers":[],"base":{"repo":{"full_name":"kronael/arizuko"}}}`, nil, 503, false, 1},
		{"wrong repository", `{"number":5,"assignees":[],"requested_reviewers":[],"base":{"repo":{"full_name":"foreign/repo"}}}`, nil, 503, false, 1},
		{"missing reviewers", `{"number":5,"assignees":[],"base":{"repo":{"full_name":"kronael/arizuko"}}}`, nil, 503, false, 1},
		{"null assignees", `{"number":5,"assignees":null,"requested_reviewers":[],"base":{"repo":{"full_name":"kronael/arizuko"}}}`, nil, 503, false, 1},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			prResponse = scenario.pr
			if prResponse == "" {
				prResponse = `{"number":5,"assignees":[],"requested_reviewers":[],"base":{"repo":{"full_name":"kronael/arizuko"}}}`
			}
			pages = scenario.pages
			calls = nil
			e.routd.mu.Lock()
			e.routd.got = nil
			e.routd.mu.Unlock()
			e.srv.bot.lastInboundAt.Store(0)
			raw := body(comment)
			want, _, err := mapEvent("repo", "kronael/arizuko", "issue_comment", "responsibility", raw, fixedNow, botLogin)
			if err != nil {
				t.Fatal(err)
			}
			got := e.deliver(t, "/github/repo/kronael/arizuko", "fixture-secret", "issue_comment", "responsibility", raw)
			messages := e.routd.messages()
			if got.Code != scenario.code || len(calls) != scenario.calls {
				t.Fatalf("status=%d calls=%v body=%s", got.Code, calls, got.Body.String())
			}
			if scenario.code == 503 {
				if len(messages) != 0 || e.srv.bot.LastInboundAt() != 0 {
					t.Fatalf("failed classification forwarded or accepted: %+v", messages)
				}
				return
			}
			if len(messages) != 1 || (messages[0].Verb == "mention") != scenario.mention {
				t.Fatalf("messages=%+v", messages)
			}
			want.Verb = messages[0].Verb
			if messages[0].ID != want.ID || messages[0].Content != want.Content || messages[0].ChatJID != want.ChatJID || messages[0].Topic != want.Topic || messages[0].ReplyTo != want.ReplyTo {
				t.Fatalf("message changed: %+v want %+v", messages[0], want)
			}
			if scenario.calls > 1 && calls[1] != "/repos/kronael/arizuko/pulls/5/reviews?per_page=100&page=1" {
				t.Fatal(calls)
			}
			if scenario.calls > 2 && calls[2] != "/repos/kronael/arizuko/pulls/5/reviews?per_page=100&page=2" {
				t.Fatal(calls)
			}
		})
	}
	prResponse = `{"number":5,"assignees":[],"requested_reviewers":[],"base":{"repo":{"full_name":"kronael/arizuko"}}}`
	pages = []string{"[" + foreign + "]", "refuse"}
	e.routd.mu.Lock()
	e.routd.got = nil
	e.routd.mu.Unlock()
	raw := body(comment)
	failed := e.deliver(t, "/github/repo/kronael/arizuko", "fixture-secret", "issue_comment", "retry-review", raw)
	if failed.Code != 503 || len(e.routd.messages()) != 0 {
		t.Fatal("failed review lookup forwarded a message")
	}
	pages[1] = `[{"user":{"login":"arizuko-bot"},"submitted_at":"2026-01-01"}]`
	for range 2 {
		e.routd.mu.Lock()
		e.routd.got = nil
		e.routd.mu.Unlock()
		got := e.deliver(t, "/github/repo/kronael/arizuko", "fixture-secret", "issue_comment", "retry-review", raw)
		messages := e.routd.messages()
		if got.Code != 200 || len(messages) != 1 || messages[0].ID != "gh-c42" || messages[0].Verb != "mention" {
			t.Fatalf("replay status=%d messages=%+v", got.Code, messages)
		}
	}
	e.routd.mu.Lock()
	e.routd.got = nil
	e.routd.mu.Unlock()
	e.srv.bot.lastInboundAt.Store(0)
	pages = []string{"[" + foreign + "]", "timeout"}
	calls = nil
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest("POST", "/github/repo/kronael/arizuko", bytes.NewReader(raw)).WithContext(ctx)
	req.Header.Set("X-Hub-Signature-256", sign("fixture-secret", raw))
	req.Header.Set("X-GitHub-Event", "issue_comment")
	req.Header.Set("X-GitHub-Delivery", "timeout")
	response := httptest.NewRecorder()
	e.h.ServeHTTP(response, req)
	<-timeoutDone
	if response.Code != 503 || len(e.routd.messages()) != 0 || e.srv.bot.LastInboundAt() != 0 || len(calls) != 3 {
		t.Fatalf("canceled lookup status=%d messages=%+v", response.Code, e.routd.messages())
	}
}

func TestPRResponsibilityEventCoverage(t *testing.T) {
	e := newTestEnv(t, secretMap{"repo/kronael/arizuko": "fixture-secret"})
	e.srv.bot.login.Store(botLogin)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/reviews") {
			_, _ = w.Write([]byte(`[{"user":{"login":"arizuko-bot"},"submitted_at":"2026-01-01"}]`))
			return
		}
		_, _ = w.Write([]byte(`{"number":5,"assignees":[],"requested_reviewers":[],"base":{"repo":{"full_name":"kronael/arizuko"}}}`))
	}))
	defer api.Close()
	e.srv.bot.cfg.APIBase = api.URL
	pr := strings.Replace(prFields, "%s", "", 1)
	for _, scenario := range []struct{ event, fields string }{
		{"issue_comment", `"action":"edited","issue":{"number":5,"pull_request":{}},"comment":{"id":42,"body":"corrected"}`},
		{"issue_comment", `"action":"deleted","issue":{"number":5,"pull_request":{}},"comment":{"id":42}`},
		{"pull_request_review_comment", `"action":"created",` + pr + `,"comment":{"id":42,"body":"reply","in_reply_to_id":21}`},
		{"pull_request_review", `"action":"submitted",` + pr + `,"review":{"id":7,"state":"commented","body":""}`},
		{"pull_request_review_thread", `"action":"resolved",` + pr},
		{"pull_request", `"action":"synchronize",` + pr},
	} {
		t.Run(scenario.event+scenario.fields[:18], func(t *testing.T) {
			e.routd.mu.Lock()
			e.routd.got = nil
			e.routd.mu.Unlock()
			raw := body(scenario.fields)
			want, _, err := mapEvent("repo", "kronael/arizuko", scenario.event, "coverage", raw, fixedNow, botLogin)
			if err != nil {
				t.Fatal(err)
			}
			got := e.deliver(t, "/github/repo/kronael/arizuko", "fixture-secret", scenario.event, "coverage", raw)
			messages := e.routd.messages()
			if got.Code != 200 || len(messages) != 1 || messages[0].Verb != "mention" {
				t.Fatalf("status=%d messages=%+v", got.Code, messages)
			}
			if messages[0].ID != want.ID || messages[0].Content != want.Content || messages[0].ChatJID != want.ChatJID || messages[0].Topic != want.Topic || messages[0].ReplyTo != want.ReplyTo {
				t.Fatalf("changed message %+v want %+v", messages[0], want)
			}
		})
	}
	for _, event := range []string{"check_run", "check_suite", "workflow_run"} {
		e.routd.mu.Lock()
		e.routd.got = nil
		e.routd.mu.Unlock()
		got := e.deliver(t, "/github/repo/kronael/arizuko", "fixture-secret", event, event, ciBody(t, event, "completed", "completed", []int{5}))
		messages := e.routd.messages()
		if got.Code != 200 || len(messages) != 2 || messages[1].Verb != "mention" {
			t.Fatalf("%s status=%d messages=%+v", event, got.Code, messages)
		}
	}
}

func TestCIResponsibilityAndSelfSuppression(t *testing.T) {
	e := newTestEnv(t, secretMap{"repo/kronael/arizuko": "fixture-secret"})
	e.srv.bot.login.Store(botLogin)
	var assignees, reviewers, reviews string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/reviews") {
			_, _ = w.Write([]byte(reviews))
			return
		}
		_, _ = fmt.Fprintf(w, `{"number":5,"assignees":%s,"requested_reviewers":%s,"base":{"repo":{"full_name":"kronael/arizuko"}}}`, assignees, reviewers)
	}))
	defer api.Close()
	e.srv.bot.cfg.APIBase = api.URL
	for _, event := range []string{"check_run", "check_suite", "workflow_run"} {
		for _, scenario := range []struct {
			name, assignees, reviewers, reviews string
			self                                bool
		}{
			{"assignee", `[{"login":"arizuko-bot"}]`, `[]`, `[]`, false},
			{"requested", `[]`, `[{"login":"arizuko-bot"}]`, `[]`, false},
			{"submitted", `[]`, `[]`, `[{"user":{"login":"arizuko-bot"},"submitted_at":"2026-01-01"}]`, false},
			{"self", `[{"login":"arizuko-bot"}]`, `[]`, `[]`, true},
		} {
			t.Run(event+"/"+scenario.name, func(t *testing.T) {
				assignees, reviewers, reviews = scenario.assignees, scenario.reviewers, scenario.reviews
				e.routd.mu.Lock()
				e.routd.got = nil
				e.routd.mu.Unlock()
				raw := ciBody(t, event, "completed", "completed", []int{5})
				if scenario.self {
					raw = []byte(strings.Replace(string(raw), `"login":"actions-bot"`, `"login":"arizuko-bot"`, 1))
				}
				got := e.deliver(t, "/github/repo/kronael/arizuko", "fixture-secret", event, "ci-responsibility", raw)
				messages := e.routd.messages()
				if got.Code != 200 || len(messages) != 2 || (messages[1].Verb == "mention") == scenario.self {
					t.Fatalf("status=%d messages=%+v", got.Code, messages)
				}
			})
		}
	}
}

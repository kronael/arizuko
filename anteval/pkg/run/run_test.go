package run

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/anteval/pkg/check"
	"github.com/kronael/arizuko/anteval/pkg/spec"
)

var cbRe = regexp.MustCompile(`https?://[^\s"']+/cb/[^\s"']+`)

// agentSim is a compliant fake agent: on Inject it finds the callback URL in
// the task prompt and hits it (optionally echoing a minted token), exactly as
// a real agent following the case instructions would.
type agentSim struct {
	token string
	msgs  []check.Msg
}

func (a *agentSim) Inject(_, _, prompt string) (string, error) {
	url := cbRe.FindString(prompt)
	if url != "" {
		if a.token != "" {
			sep := "?"
			if strings.Contains(url, "?") {
				sep = "&"
			}
			url += sep + "token=" + a.token
		}
		http.Post(url, "", nil)
	}
	return "turn-1", nil
}
func (a *agentSim) RestMessages(string) ([]check.Msg, error) { return a.msgs, nil }
func (a *agentSim) McpMessages(string) ([]check.Msg, error)  { return a.msgs, nil }
func (a *agentSim) Cost(string) (int, error)                 { return 7, nil }

func TestDriveCallback(t *testing.T) {
	c := spec.Case{ID: "self-skill", Dimension: "self", Prompt: "curl {sink}/cb/{nonce}",
		Check: spec.Check{Kind: "callback"}}
	res := Drive(Config{Target: &agentSim{}, Cases: []spec.Case{c}, Nonce: "R",
		Poll: 5 * time.Millisecond})
	if len(res) != 1 || !res[0].Pass {
		t.Fatalf("want pass, got %+v", res)
	}
	if res[0].Tokens != 7 {
		t.Fatalf("want tokens=7, got %d", res[0].Tokens)
	}
}

func TestDriveCbTokenExpand(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/chat/TKN/" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c := spec.Case{ID: "chat-entrypoint", Dimension: "chat",
		Prompt: "mint a chat link, report it to {sink}/cb/{nonce}",
		Check:  spec.Check{Kind: "http_status", URL: "{target}/chat/{cb.token}/", Want: 200}}
	res := Drive(Config{Target: &agentSim{token: "TKN"}, Cases: []spec.Case{c},
		Nonce: "R", TargetBase: srv.URL, Poll: 5 * time.Millisecond})
	if !res[0].Pass {
		t.Fatalf("want pass via {cb.token} expansion, got %+v", res[0])
	}
}

// TestDriveStatusSeesRedirect: http_status grades the FIRST response. A
// gated /priv URL answers 303 → login; a redirect-following client would
// grade the login page's 200 and "gate engaged" could never pass (nor could
// a 3xx ever be asserted). Mirrors proxyd's requireAuth contract.
func TestDriveStatusSeesRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	}))
	defer srv.Close()
	c := spec.Case{ID: "priv-401", Dimension: "web", Prompt: "publish gated",
		Check: spec.Check{Kind: "http_status", URL: "{target}/priv/x", Want: 303}}
	res := Drive(Config{Target: &agentSim{}, Cases: []spec.Case{c},
		Nonce: "R", TargetBase: srv.URL, Poll: 5 * time.Millisecond})
	if !res[0].Pass {
		t.Fatalf("want 303 observed as 303, got %+v", res[0])
	}
}

type silentTarget struct{}

func (silentTarget) Inject(_, _, _ string) (string, error)    { return "t", nil }
func (silentTarget) RestMessages(string) ([]check.Msg, error) { return nil, nil }
func (silentTarget) McpMessages(string) ([]check.Msg, error)  { return nil, nil }
func (silentTarget) Cost(string) (int, error)                 { return 0, nil }

func TestDriveTokenBudget(t *testing.T) {
	c := spec.Case{ID: "b", Prompt: "curl {sink}/cb/{nonce}", MaxTokens: 5,
		Check: spec.Check{Kind: "callback"}}
	res := Drive(Config{Target: &agentSim{}, Cases: []spec.Case{c}, Nonce: "R",
		Poll: 5 * time.Millisecond})
	if res[0].Pass {
		t.Fatalf("want fail on token budget (cost 7 > 5), got %+v", res[0])
	}
	if !strings.Contains(res[0].Reason, "budget") {
		t.Fatalf("want budget reason, got %q", res[0].Reason)
	}
}

func TestDriveTimeout(t *testing.T) {
	c := spec.Case{ID: "x", Prompt: "noop", MaxWallMs: 40, Check: spec.Check{Kind: "callback"}}
	res := Drive(Config{Target: silentTarget{}, Cases: []spec.Case{c}, Nonce: "R",
		Poll: 5 * time.Millisecond})
	if res[0].Pass {
		t.Fatalf("want fail on timeout, got %+v", res[0])
	}
	if !strings.HasPrefix(res[0].Reason, "timeout") {
		t.Fatalf("want timeout reason, got %q", res[0].Reason)
	}
}

// TestRequiresHint_NamesTheMissingGrantOnTimeout: the 2026-08-08 live run lost
// four of eight cases to tools the eval folder was never granted, and every one
// reported the same "timeout: no callback" an agent failure produces. The agent
// had already named the exact missing grant in chat; the harness threw that away.
func TestRequiresHint_NamesTheMissingGrantOnTimeout(t *testing.T) {
	c := spec.Case{
		ID: "x", Prompt: "noop", MaxWallMs: 40,
		Requires: []string{"mcp:issue_webhook"},
		Check:    spec.Check{Kind: "callback"},
	}
	res := Drive(Config{Target: silentTarget{}, Cases: []spec.Case{c}, Nonce: "R",
		Poll: 5 * time.Millisecond})
	if len(res) != 1 || res[0].Pass {
		t.Fatalf("want one failing result, got %+v", res)
	}
	if !strings.Contains(res[0].Reason, "mcp:issue_webhook") {
		t.Errorf("reason %q does not name the required tool — the reader cannot tell an ungranted tool from a failed agent", res[0].Reason)
	}
}

// A case with no declared requirement must not grow a trailing hint.
func TestRequiresHint_SilentWhenNothingDeclared(t *testing.T) {
	c := spec.Case{ID: "y", Prompt: "noop", MaxWallMs: 40, Check: spec.Check{Kind: "callback"}}
	res := Drive(Config{Target: silentTarget{}, Cases: []spec.Case{c}, Nonce: "R",
		Poll: 5 * time.Millisecond})
	if len(res) != 1 {
		t.Fatalf("want one result, got %+v", res)
	}
	if strings.Contains(res[0].Reason, "case needs") {
		t.Errorf("reason %q added a hint for a case declaring none", res[0].Reason)
	}
}

// TestEachCaseGetsItsOwnTopic: cases must not share a conversation. On the
// 2026-08-08 live run all eight injected into the chat's default topic, so
// case 1's "I can't, that tool is root-only" sat in every later case's context
// — and a re-run with the tools ACTUALLY granted scored identically, because
// the agent was answering from history rather than its live tool list.
func TestEachCaseGetsItsOwnTopic(t *testing.T) {
	rec := &topicRecorder{}
	cases := []spec.Case{
		{ID: "a", Prompt: "x", MaxWallMs: 20, Check: spec.Check{Kind: "callback"}},
		{ID: "b", Prompt: "x", MaxWallMs: 20, Check: spec.Check{Kind: "callback"}},
	}
	Drive(Config{Target: rec, Cases: cases, Nonce: "R", Poll: 5 * time.Millisecond})
	if len(rec.topics) != 2 {
		t.Fatalf("got %d injections, want 2", len(rec.topics))
	}
	if rec.topics[0] == "" || rec.topics[1] == "" {
		t.Fatalf("a case injected with an empty topic (%q, %q) — that is the shared default session", rec.topics[0], rec.topics[1])
	}
	if rec.topics[0] == rec.topics[1] {
		t.Fatalf("both cases used topic %q; each case needs its own session", rec.topics[0])
	}
}

type topicRecorder struct {
	silentTarget
	topics []string
}

func (r *topicRecorder) Inject(_, topic, _ string) (string, error) {
	r.topics = append(r.topics, topic)
	return "t", nil
}

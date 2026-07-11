package run

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
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

func (a *agentSim) Inject(_, prompt string) (string, error) {
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

func (silentTarget) Inject(_, _ string) (string, error)      { return "t", nil }
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

// TestDriveNegateAbsent: a negated check passes when its positive condition
// never occurs within the budget window.
func TestDriveNegateAbsent(t *testing.T) {
	c := spec.Case{ID: "neg-absent", Prompt: "noop", MaxWallMs: 40,
		Check: spec.Check{Kind: "callback", Negate: true}}
	res := Drive(Config{Target: silentTarget{}, Cases: []spec.Case{c}, Nonce: "R",
		Poll: 5 * time.Millisecond})
	if !res[0].Pass {
		t.Fatalf("negate: want pass when condition absent, got %+v", res[0])
	}
	if !strings.Contains(res[0].Reason, "absent") {
		t.Fatalf("want 'absent' reason, got %q", res[0].Reason)
	}
}

// TestDriveNegatePresent: a negated check fails the instant its positive
// condition occurs — here the compliant agent fires the callback.
func TestDriveNegatePresent(t *testing.T) {
	c := spec.Case{ID: "neg-present", Prompt: "curl {sink}/cb/{nonce}", MaxWallMs: 2000,
		Check: spec.Check{Kind: "callback", Negate: true}}
	res := Drive(Config{Target: &agentSim{}, Cases: []spec.Case{c}, Nonce: "R",
		Poll: 5 * time.Millisecond})
	if res[0].Pass {
		t.Fatalf("negate: want fail when condition occurs, got %+v", res[0])
	}
	if !strings.Contains(res[0].Reason, "unexpected") {
		t.Fatalf("want 'unexpected' reason, got %q", res[0].Reason)
	}
}

// twoTurnTarget records every inject and only exposes the graded message after
// the SECOND inject — so a check that passes proves it ran against turn 2, and
// Cost keys per turn id so the report attributes spend to the graded turn.
type twoTurnTarget struct {
	injects []string
	msgs    []check.Msg
}

func (t *twoTurnTarget) Inject(_, prompt string) (string, error) {
	t.injects = append(t.injects, prompt)
	if len(t.injects) == 2 {
		t.msgs = []check.Msg{{FromBot: true, Text: prompt}}
	}
	return "turn-" + strconv.Itoa(len(t.injects)), nil
}
func (t *twoTurnTarget) RestMessages(string) ([]check.Msg, error) { return t.msgs, nil }
func (t *twoTurnTarget) McpMessages(string) ([]check.Msg, error)  { return t.msgs, nil }
func (t *twoTurnTarget) Cost(id string) (int, error) {
	if id == "turn-2" {
		return 42, nil
	}
	return 7, nil
}

// TestDriveTwoTurn: Prompt2 drives a second inject after a settle, the expander
// applies to it, and the check + cost track turn 2 (not turn 1).
func TestDriveTwoTurn(t *testing.T) {
	tgt := &twoTurnTarget{}
	c := spec.Case{ID: "twoturn", Prompt: "persist {nonce}", Prompt2: "/new report {nonce}",
		MaxWallMs: 90, Check: spec.Check{Kind: "rest_reply", Chat: "{chat}", Text: "{nonce}"}}
	res := Drive(Config{Target: tgt, Cases: []spec.Case{c}, Nonce: "R",
		Poll: 5 * time.Millisecond})
	if len(tgt.injects) != 2 {
		t.Fatalf("want 2 injects, got %d: %v", len(tgt.injects), tgt.injects)
	}
	if !strings.Contains(tgt.injects[0], "persist R-twoturn") {
		t.Fatalf("turn 1 inject wrong: %q", tgt.injects[0])
	}
	if !strings.HasPrefix(tgt.injects[1], "/new ") || !strings.Contains(tgt.injects[1], "R-twoturn") {
		t.Fatalf("turn 2 inject not the expanded Prompt2: %q", tgt.injects[1])
	}
	if !res[0].Pass {
		t.Fatalf("want pass on the graded turn 2, got %+v", res[0])
	}
	if res[0].Tokens != 42 {
		t.Fatalf("cost must track turn 2 (want 42), got %d", res[0].Tokens)
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

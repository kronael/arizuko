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

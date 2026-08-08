// Package run drives anteval cases against a live target and hosts the
// callback sink the agent's artifacts call back into. The harness only ever
// injects a task and observes a public-surface effect; the live agent does
// the real work with its own MCP tools.
package run

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/kronael/arizuko/anteval/pkg/check"
	"github.com/kronael/arizuko/anteval/pkg/report"
	"github.com/kronael/arizuko/anteval/pkg/spec"
)

// Target is the live instance under test, reached only through public
// surfaces (routd REST / proxyd HTTP / MCP). Tests supply a fake.
type Target interface {
	check.Reader
	// Inject posts one task. `topic` isolates the case in its own session —
	// see runCase for why sharing the chat default topic broke a live run.
	Inject(chat, topic, prompt string) (turnID string, err error)
	Cost(turnID string) (int, error)
}

// Config parameterizes a run.
type Config struct {
	Target     Target
	Cases      []spec.Case
	Nonce      string        // run nonce; per-case nonce is Nonce+"-"+case.ID
	TargetBase string        // public base URL of the instance (templated as {target})
	Chat       string        // eval agent chat JID; every task is injected here (templated as {chat})
	SinkBind   string        // local bind addr for the callback sink (default 127.0.0.1:0; live: :PORT on a routable iface)
	SinkURL    string        // externally reachable sink base URL agents call back to; empty = local bind addr
	Poll       time.Duration // await interval (default 2s)
}

// Drive runs every selected case and returns results. It binds the callback
// sink for the lifetime of the run.
func Drive(cfg Config) []report.Result {
	if cfg.Poll <= 0 {
		cfg.Poll = 2 * time.Second
	}
	bind := cfg.SinkBind
	if bind == "" {
		bind = "127.0.0.1:0"
	}
	s := newSink()
	srv, addr := s.serve(bind)
	defer srv.Close()
	if cfg.SinkURL == "" {
		cfg.SinkURL = addr
	}
	out := make([]report.Result, 0, len(cfg.Cases))
	for _, c := range cfg.Cases {
		out = append(out, runCase(cfg, s, c))
	}
	return out
}

func runCase(cfg Config, s *sink, c spec.Case) report.Result {
	start := time.Now()
	nonce := cfg.Nonce + "-" + c.ID
	expand := newExpander(map[string]string{
		"nonce": nonce, "sink": cfg.SinkURL, "target": cfg.TargetBase, "chat": cfg.Chat,
	}, s, nonce)
	r := report.Result{ID: c.ID, Dimension: c.Dimension}

	// Each case runs in its OWN topic, so it gets its own session. Sharing the
	// chat's default topic put every case in one conversation: on the
	// 2026-08-08 live run case 1 answered "I can't, that tool is root-only" and
	// cases 2-8 inherited that context, so re-running with the tools ACTUALLY
	// GRANTED changed nothing — the agent was answering from history, not from
	// its live tool list. The nonce already isolated each check; it has to
	// isolate the conversation too, or the eval measures the agent's mood after
	// case 1 rather than its capability.
	//
	// The topic goes in the message FIELD, not as a leading `#topic` line: steer
	// treats `#name` as a sticky command only when it is the WHOLE message, so a
	// `#nonce\nprompt` body would reach the agent as literal noise and set no
	// topic at all.
	turn, err := cfg.Target.Inject(cfg.Chat, nonce, expand(c.Prompt))
	if err != nil {
		r.Reason = "inject: " + err.Error()
		r.LatencyMs = msSince(start)
		return r
	}
	ctx := check.Ctx{HTTP: noRedirectClient, Sink: s, Reader: cfg.Target, Expand: expand}
	deadline := time.Now().Add(budget(c))
	for {
		pass, reason := check.Run(ctx, c.Check)
		if pass {
			r.Pass, r.Reason = true, reason
			break
		}
		if time.Now().After(deadline) {
			r.Reason = "timeout: " + reason + requiresHint(c)
			break
		}
		time.Sleep(cfg.Poll)
	}
	r.Tokens, _ = cfg.Target.Cost(turn)
	if r.Pass && c.MaxTokens > 0 && r.Tokens > c.MaxTokens {
		r.Pass = false
		r.Reason = fmt.Sprintf("token budget exceeded: %d > %d", r.Tokens, c.MaxTokens)
	}
	r.LatencyMs = msSince(start)
	return r
}

// noRedirectClient asserts the FIRST response of an http_status probe. The
// default client follows redirects, which made "gate engaged" unobservable:
// proxyd answers an unauthenticated /priv GET with 303 → /auth/login, and the
// follower graded the login page's 200 instead (cost the first live priv-401
// run, 2026-07-11). A status checker must see 3xx as 3xx.
var noRedirectClient = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func budget(c spec.Case) time.Duration {
	if c.MaxWallMs > 0 {
		return time.Duration(c.MaxWallMs) * time.Millisecond
	}
	return 90 * time.Second
}

func msSince(t time.Time) int64 { return time.Since(t).Milliseconds() }

// newExpander substitutes {key} vars and {cb.<key>} (a query param the agent
// handed back through the sink, e.g. a freshly minted chat-link token).
func newExpander(vars map[string]string, s *sink, nonce string) func(string) string {
	return func(in string) string {
		out := in
		for k, v := range vars {
			out = strings.ReplaceAll(out, "{"+k+"}", v)
		}
		for {
			i := strings.Index(out, "{cb.")
			if i < 0 {
				break
			}
			j := strings.Index(out[i:], "}")
			if j < 0 {
				break
			}
			key := out[i+4 : i+j]
			val := ""
			if hits := s.Hits(nonce); len(hits) > 0 {
				val = hits[len(hits)-1].Query[key]
			}
			out = out[:i] + val + out[i+j+1:]
		}
		return out
	}
}

// sink records every callback the agent's artifacts make, keyed by nonce.
type sink struct {
	mu   sync.Mutex
	hits map[string][]check.Hit
}

func newSink() *sink { return &sink{hits: map[string][]check.Hit{}} }

func (s *sink) Hits(nonce string) []check.Hit {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]check.Hit(nil), s.hits[nonce]...)
}

func (s *sink) serve(bind string) (io.Closer, string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/cb/", func(w http.ResponseWriter, r *http.Request) {
		nonce := strings.TrimPrefix(r.URL.Path, "/cb/")
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		q := map[string]string{}
		for k := range r.URL.Query() {
			q[k] = r.URL.Query().Get(k)
		}
		s.mu.Lock()
		s.hits[nonce] = append(s.hits[nonce], check.Hit{Query: q, Body: string(body)})
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	ln, err := net.Listen("tcp", bind)
	if err != nil {
		panic(err)
	}
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	return srv, "http://" + ln.Addr().String()
}

// requiresHint appends the case's declared tool requirements to a failure
// reason. A timeout that says only "no callback" cannot distinguish an agent
// that failed the task from an agent that was never granted the tool — and the
// second is an operator fix, not a capability finding.
func requiresHint(c spec.Case) string {
	if len(c.Requires) == 0 {
		return ""
	}
	return " — case needs " + strings.Join(c.Requires, ", ") +
		"; verify the eval folder holds them before reading this as an agent failure"
}

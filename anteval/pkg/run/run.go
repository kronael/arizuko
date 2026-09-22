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
	// Session reads the stored resume-session id for (folder, topic); "" =
	// none. The baseline reset (ensurePristineRoot) reads the ROOT topic
	// through this.
	Session(folder, topic string) (string, error)
}

// Config parameterizes a run.
type Config struct {
	Target     Target
	Cases      []spec.Case
	Nonce      string        // run nonce; per-case nonce is Nonce+"-"+case.ID
	TargetBase string        // public base URL of the instance (templated as {target})
	Chat       string        // eval agent chat JID; every task is injected here (templated as {chat})
	Folder     string        // eval folder owning Chat (baseline session reset); empty skips the reset — cmdRun requires it, so only pure-fake unit runs skip
	SinkBind   string        // local bind addr for the callback sink (default 127.0.0.1:0; live: :PORT on a routable iface)
	SinkURL    string        // externally reachable sink base URL agents call back to; empty = local bind addr
	Poll       time.Duration // await interval (default 2s)

	// baselineWait bounds how long one /new reset may take before the run
	// aborts (a running turn on the eval chat delays consumption). Tests
	// shrink it; zero = 90s.
	baselineWait time.Duration

	// settleWait bounds the wait for a case's own turn to finish before the
	// NEXT case is injected. Tests shrink it; zero = DefaultSettleWait.
	settleWait time.Duration
}

// DefaultSettleWait is the per-case settle bound. Exported so the bearer
// preflight can budget the same number the run will actually spend.
const DefaultSettleWait = 180 * time.Second

// Drive runs every selected case and returns results. It binds the callback
// sink for the lifetime of the run. A baseline failure aborts the run — the
// results so far come back with the error; everything after would have
// measured inherited history, not the agent.
func Drive(cfg Config) ([]report.Result, error) {
	if cfg.Poll <= 0 {
		cfg.Poll = 2 * time.Second
	}
	if cfg.baselineWait <= 0 {
		cfg.baselineWait = 90 * time.Second
	}
	if cfg.settleWait <= 0 {
		cfg.settleWait = DefaultSettleWait
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
	if err := probeSink(cfg.SinkURL); err != nil {
		return nil, err
	}
	out := make([]report.Result, 0, len(cfg.Cases))
	for _, c := range cfg.Cases {
		if err := ensurePristineRoot(cfg); err != nil {
			return out, fmt.Errorf("case %s: %w", c.ID, err)
		}
		r := runCase(cfg, s, c)
		// The case's own turn may still be running — a checker passes the
		// moment the observable effect lands, and a timeout gives up on the
		// clock, not on the agent. routd dispatches ONE turn per folder, so
		// injecting the next case now queues it behind this one and spends
		// its whole budget waiting to start. See BUGS L68.
		r.Note = settle(cfg, r.TurnID)
		out = append(out, r)
	}
	return out, nil
}

// ensurePristineRoot guarantees the eval folder's ROOT topic holds no session
// before a case is injected. Each case runs in its own fresh topic, and the first
// turn of any non-main topic forks main's session (specs/5/F) — so a root session
// means the case inherits every prior conversation in the chat (BUGS L54).
//
// The reset is `/new`: routd's steer consumes a bare /new before any turn, so the
// reset cannot re-create what it clears. The poll below is the proof it LANDED.
// Checked before EVERY case, not once per run: a group-context scheduled follow-up
// fires topic-less on the ROOT topic and persists its session there mid-run.
func ensurePristineRoot(cfg Config) error {
	if cfg.Folder == "" {
		return nil
	}
	sid, err := cfg.Target.Session(cfg.Folder, "")
	if err != nil {
		return fmt.Errorf("baseline: read root session: %w", err)
	}
	if sid == "" {
		return nil
	}
	if _, err := cfg.Target.Inject(cfg.Chat, "", "/new"); err != nil {
		return fmt.Errorf("baseline: inject /new: %w", err)
	}
	deadline := time.Now().Add(cfg.baselineWait)
	for {
		time.Sleep(cfg.Poll)
		sid, err = cfg.Target.Session(cfg.Folder, "")
		if err != nil {
			return fmt.Errorf("baseline: read root session: %w", err)
		}
		if sid == "" {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf(
				"baseline: root session %s survived /new for %s — quiesce the eval chat; a case injected now would fork this session (specs/5/F) and score its history",
				sid, cfg.baselineWait)
		}
	}
}

// settle waits for one case's turn to finish before the next case is injected.
//
// It exists because arizuko is CORRECT to serialize: routd runs one turn per
// folder, and every case shares the eval folder. anteval used to inject the next
// case the instant a checker resolved, so a case whose agent kept working past its
// budget starved every case after it — on krons 2026-08-22 the folder had no
// dispatch for 7m42s and three cases expired unstarted (BUGS L68). The failures
// land in ALPHABETICAL order, which reads like a pattern and is only the queue.
//
// The completion signal is the model gateway metering the turn. A turn the meter
// never reports must not hang the suite, so the wait is bounded and says so out
// loud — and an instance with no gateway has no meter, so it always waits out.
func settle(cfg Config, turnID string) string {
	if turnID == "" || cfg.Target == nil {
		return ""
	}
	deadline := time.Now().Add(cfg.settleWait)
	for {
		if n, err := cfg.Target.Cost(turnID); err == nil && n > 0 {
			return ""
		}
		if time.Now().After(deadline) {
			return fmt.Sprintf("turn %s never recorded a cost within %s; "+
				"the next case may start behind it", turnID, cfg.settleWait)
		}
		time.Sleep(cfg.Poll)
	}
}

func runCase(cfg Config, s *sink, c spec.Case) report.Result {
	start := time.Now()
	nonce := cfg.Nonce + "-" + c.ID
	expand := newExpander(map[string]string{
		"nonce": nonce, "sink": cfg.SinkURL, "target": cfg.TargetBase, "chat": cfg.Chat,
	}, s, nonce)
	r := report.Result{ID: c.ID, Dimension: c.Dimension}

	// Each case runs in its OWN topic, so it gets its own session. Sharing the
	// chat's default topic put every case in one conversation: on the 2026-08-08
	// live run case 1 answered "I can't, that tool is root-only" and cases 2-8
	// inherited that context, so re-running with the tools ACTUALLY GRANTED changed
	// nothing — the agent was answering from history, not from its live tool list.
	//
	// The topic goes in the message FIELD, not as a leading `#topic` line: steer
	// treats `#name` as a sticky command only when it is the WHOLE message.
	turn, err := cfg.Target.Inject(cfg.Chat, nonce, expand(c.Prompt))
	if err != nil {
		r.Reason = "inject: " + err.Error()
		r.LatencyMs = msSince(start)
		return r
	}
	r.TurnID = turn
	ctx := check.Ctx{HTTP: noRedirectClient, Sink: s, Reader: cfg.Target, Topic: nonce, Expand: expand}
	deadline := time.Now().Add(budget(c))
	for {
		pass, reason := check.Run(ctx, c.Check)
		if pass {
			r.Pass, r.Reason = true, reason
			break
		}
		if time.Now().After(deadline) {
			// NAME THE WALL. A bare "timeout" cannot be told from a dead agent,
			// and the two want opposite responses: a dead agent is the release
			// blocker this gate exists to catch, a slow queue is a precondition
			// the operator has to drain. The same build scored 5/9 during a
			// migration storm and 8/9 after it, with this case replying five
			// minutes past a 94s cap — so a red that does not say what it waited
			// for trains an operator to re-run the gate rather than read it
			// (BUGS L326).
			r.Reason = fmt.Sprintf("timeout after %s: %s%s",
				budget(c).Round(time.Second), reason, requiresHint(c))
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

// probeSink refuses a run whose --sink nobody can reach.
//
// An unreachable sink does not fail once. Every callback-based case waits out its
// whole budget and reports "no callback", which reads exactly like a broken
// product: a 2026-08-23 run scored 1/9 that way. The cause was mundane —
// `systemctl restart` made docker re-allocate the compose network, so the gateway
// address the sink pointed at no longer existed.
//
// The probe runs from the harness host, so it proves the address is live and
// bound, not that a sandboxed container can route to it.
func probeSink(sinkURL string) error {
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Get(sinkURL + "/cb/anteval-preflight")
	if err != nil {
		return fmt.Errorf("--sink %s is not reachable: %w — docker re-allocates the compose network "+
			"on restart, so re-read it: docker network inspect <instance>_default | grep Gateway",
			sinkURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("--sink %s answered %d, want 204 — something other than this run's sink is on that address",
			sinkURL, resp.StatusCode)
	}
	return nil
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

package run

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/anteval/pkg/check"
	"github.com/kronael/arizuko/anteval/pkg/report"
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
func (a *agentSim) Session(string, string) (string, error)   { return "", nil }

func TestDriveCallback(t *testing.T) {
	c := spec.Case{ID: "self-skill", Dimension: "self", Prompt: "curl {sink}/cb/{nonce}",
		Check: spec.Check{Kind: "callback"}}
	res := mustDrive(t, Config{Target: &agentSim{}, Cases: []spec.Case{c}, Nonce: "R",
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
	res := mustDrive(t, Config{Target: &agentSim{token: "TKN"}, Cases: []spec.Case{c},
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
	res := mustDrive(t, Config{Target: &agentSim{}, Cases: []spec.Case{c},
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
func (silentTarget) Session(string, string) (string, error)   { return "", nil }

func TestDriveTokenBudget(t *testing.T) {
	c := spec.Case{ID: "b", Prompt: "curl {sink}/cb/{nonce}", MaxTokens: 5,
		Check: spec.Check{Kind: "callback"}}
	res := mustDrive(t, Config{Target: &agentSim{}, Cases: []spec.Case{c}, Nonce: "R",
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
	res := mustDrive(t, Config{Target: silentTarget{}, Cases: []spec.Case{c}, Nonce: "R",
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
	res := mustDrive(t, Config{Target: silentTarget{}, Cases: []spec.Case{c}, Nonce: "R",
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
	res := mustDrive(t, Config{Target: silentTarget{}, Cases: []spec.Case{c}, Nonce: "R",
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
	mustDrive(t, Config{Target: rec, Cases: cases, Nonce: "R", Poll: 5 * time.Millisecond})
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

// mustDrive is Drive for tests that expect no baseline abort.
func mustDrive(t *testing.T, cfg Config) []report.Result {
	t.Helper()
	// Unit fakes are instantaneous; the live 180s settle would only add
	// wall-clock. Tests that exercise settle itself set this explicitly.
	if cfg.settleWait == 0 {
		cfg.settleWait = 20 * time.Millisecond
	}
	res, err := Drive(cfg)
	if err != nil {
		t.Fatalf("Drive: %v", err)
	}
	return res
}

// rootedTarget simulates routd's session store + steer: the ROOT topic starts
// with rootSession, and injecting a bare "/new" clears it (cmdNew's
// DeleteSession) — unless stuck, which models a chat whose root keeps a
// session no matter what (a turn mid-write, a broken steer).
type rootedTarget struct {
	silentTarget
	rootSession string
	stuck       bool
	injects     [][2]string // (topic, prompt) in order
}

func (r *rootedTarget) Inject(_, topic, prompt string) (string, error) {
	r.injects = append(r.injects, [2]string{topic, prompt})
	if topic == "" && prompt == "/new" && !r.stuck {
		r.rootSession = ""
	}
	return "t", nil
}

func (r *rootedTarget) Session(_, topic string) (string, error) {
	if topic == "" {
		return r.rootSession, nil
	}
	return "", nil
}

// TestDriveResetsDirtyRootBeforeCases: the fork fix (BUGS L54) made every
// case topic's first turn inherit the folder's MAIN session (specs/5/F), so a
// root session left by prior runs put 167 KB of history in front of every
// case. Drive must clear it — send /new to the ROOT topic and hold case
// injection until the root session is observably gone — before any case runs.
func TestDriveResetsDirtyRootBeforeCases(t *testing.T) {
	rec := &rootedTarget{rootSession: "56952d01"}
	c := spec.Case{ID: "a", Prompt: "x", MaxWallMs: 20, Check: spec.Check{Kind: "callback"}}
	mustDrive(t, Config{Target: rec, Cases: []spec.Case{c}, Nonce: "R",
		Folder: "eval", Poll: time.Millisecond})
	if len(rec.injects) != 2 {
		t.Fatalf("got %d injections %v, want [/new, case]", len(rec.injects), rec.injects)
	}
	if rec.injects[0] != [2]string{"", "/new"} {
		t.Fatalf("first injection = %v, want a bare /new on the ROOT topic", rec.injects[0])
	}
	if rec.injects[1][0] == "" {
		t.Fatalf("case injected into the root topic; it must get its own")
	}
}

// A pristine root must NOT get a /new: steer only consumes /new when it is the
// newest pending message, so an unneeded one risks leaking into a root turn as
// literal text — recreating the very session the reset exists to remove.
func TestDrivePristineRootSendsNoNew(t *testing.T) {
	rec := &rootedTarget{}
	c := spec.Case{ID: "a", Prompt: "x", MaxWallMs: 20, Check: spec.Check{Kind: "callback"}}
	mustDrive(t, Config{Target: rec, Cases: []spec.Case{c}, Nonce: "R",
		Folder: "eval", Poll: time.Millisecond})
	if len(rec.injects) != 1 || rec.injects[0][1] == "/new" {
		t.Fatalf("injections = %v, want only the case", rec.injects)
	}
}

// A root session that survives /new means every case would score inherited
// history — the run must abort loudly with no case results, not measure it.
func TestDriveAbortsWhenRootWontClear(t *testing.T) {
	rec := &rootedTarget{rootSession: "56952d01", stuck: true}
	c := spec.Case{ID: "a", Prompt: "x", MaxWallMs: 20, Check: spec.Check{Kind: "callback"}}
	res, err := Drive(Config{Target: rec, Cases: []spec.Case{c}, Nonce: "R",
		Folder: "eval", Poll: time.Millisecond, baselineWait: 30 * time.Millisecond})
	if err == nil {
		t.Fatal("want a baseline error, got none")
	}
	if !strings.Contains(err.Error(), "56952d01") {
		t.Errorf("error %q does not name the surviving session", err)
	}
	if len(res) != 0 {
		t.Fatalf("got %d results from an aborted run, want 0", len(res))
	}
}

// The reset runs before EVERY case, not once per run: a group-context
// scheduled follow-up (the state-persist pattern) fires topic-less on the
// ROOT topic mid-run and persists its session there.
func TestDriveResetsRootDirtiedMidRun(t *testing.T) {
	rec := &redirtyTarget{}
	cases := []spec.Case{
		{ID: "a", Prompt: "x", MaxWallMs: 20, Check: spec.Check{Kind: "callback"}},
		{ID: "b", Prompt: "x", MaxWallMs: 20, Check: spec.Check{Kind: "callback"}},
	}
	mustDrive(t, Config{Target: rec, Cases: cases, Nonce: "R",
		Folder: "eval", Poll: time.Millisecond})
	var news int
	for _, in := range rec.injects {
		if in == [2]string{"", "/new"} {
			news++
		}
	}
	if news != 1 {
		t.Fatalf("injections %v: want exactly one /new (before case b, whose baseline case a dirtied)", rec.injects)
	}
	if last := rec.injects[len(rec.injects)-1]; last[1] == "/new" {
		t.Fatalf("run ended on a /new; the reset must precede a case, not trail it")
	}
}

// redirtyTarget starts pristine and writes a root session on every CASE
// injection — the scheduled-follow-up contamination in miniature.
type redirtyTarget struct {
	rootedTarget
}

func (r *redirtyTarget) Inject(chat, topic, prompt string) (string, error) {
	id, err := r.rootedTarget.Inject(chat, topic, prompt)
	if topic != "" {
		r.rootSession = "cron-" + topic
	}
	return id, err
}

// slowTurn is an agent whose turn keeps running after the checker is satisfied:
// the callback fires at once, but the cost row only appears after N polls. It
// is the shape that starved the tail of a live run (BUGS L68).
type slowTurn struct {
	agentSim
	costAfter int
	reads     int
	injects   int
	busy      bool // true while a turn is unfinished — the invariant under test
	overlap   bool // set if a second inject arrived while busy
}

func (a *slowTurn) Inject(c, t, prompt string) (string, error) {
	if a.busy {
		a.overlap = true
	}
	a.busy = true
	a.injects++
	return a.agentSim.Inject(c, t, prompt)
}

func (a *slowTurn) Cost(string) (int, error) {
	a.reads++
	if a.reads < a.costAfter {
		return 0, nil
	}
	a.busy = false
	return 11, nil
}

func TestSettleWaitsForTheTurnToFinish(t *testing.T) {
	c := spec.Case{ID: "a", Prompt: "curl {sink}/cb/{nonce}", Check: spec.Check{Kind: "callback"}}
	ag := &slowTurn{costAfter: 4}
	res := mustDrive(t, Config{Target: ag, Cases: []spec.Case{c, c}, Nonce: "R",
		Poll: time.Millisecond, settleWait: 2 * time.Second})
	if ag.overlap {
		t.Fatal("a case was injected while the previous turn was still running — the tail starves (L68)")
	}
	if len(res) != 2 || !res[0].Pass || !res[1].Pass {
		t.Fatalf("want both passing, got %+v", res)
	}
	if res[0].Note != "" {
		t.Errorf("a turn that DID finish must carry no note: %q", res[0].Note)
	}
}

// A turn that never reports must not hang the suite; it warns and moves on.
func TestSettleGivesUpLoudly(t *testing.T) {
	c := spec.Case{ID: "a", Prompt: "curl {sink}/cb/{nonce}", Check: spec.Check{Kind: "callback"}}
	ag := &slowTurn{costAfter: 1 << 30} // never records
	res := mustDrive(t, Config{Target: ag, Cases: []spec.Case{c}, Nonce: "R",
		Poll: time.Millisecond, settleWait: 30 * time.Millisecond})
	if len(res) != 1 || !res[0].Pass {
		t.Fatalf("the case itself still passes: %+v", res)
	}
	if !strings.Contains(res[0].Note, "never recorded a cost") {
		t.Errorf("want a loud note, got %q", res[0].Note)
	}
}

// TestProbeSink_RefusesAnUnreachableAddress: an unreachable --sink does not fail
// once, it fails nine times as "no callback" and reads like a broken product. A
// 2026-08-23 run scored 1/9 that way because a restart moved the docker gateway.
func TestProbeSink_RefusesAnUnreachableAddress(t *testing.T) {
	// Port 1 on loopback: nothing binds it, so the connect fails immediately.
	err := probeSink("http://127.0.0.1:1")
	if err == nil {
		t.Fatal("an unreachable sink must refuse the run before any case runs")
	}
	if !strings.Contains(err.Error(), "Gateway") {
		t.Errorf("the error must point at how to re-read the address: %v", err)
	}
}

func TestProbeSink_AcceptsTheRunningSink(t *testing.T) {
	s := newSink()
	srv, addr := s.serve("127.0.0.1:0")
	defer srv.Close()
	if err := probeSink(addr); err != nil {
		t.Fatalf("the run's own sink must pass: %v", err)
	}
}

// A bare "timeout" cannot be told from a dead agent, and the two want opposite
// responses: a dead agent blocks the release, a slow queue is a precondition to
// drain. The same build scored 5/9 during a migration storm and 8/9 after it,
// with rest-roundtrip replying five minutes past a 94s cap — so a red that does
// not say what it waited for trains an operator to re-run the gate instead of
// reading it (BUGS L326).
func TestTimeoutReasonNamesTheWallItWaitedFor(t *testing.T) {
	c := spec.Case{ID: "z", Prompt: "noop", MaxWallMs: 40, Check: spec.Check{Kind: "callback"}}
	res := mustDrive(t, Config{Target: silentTarget{}, Cases: []spec.Case{c}, Nonce: "R",
		Poll: 5 * time.Millisecond})
	if len(res) != 1 || res[0].Pass {
		t.Fatalf("want one failing result, got %+v", res)
	}
	if !strings.Contains(res[0].Reason, "timeout after") {
		t.Errorf("reason %q does not say it waited on a wall", res[0].Reason)
	}
	// The budget must be the case's own, not the 90s default: a case that sets
	// its own cap and reports the default teaches the reader the wrong number.
	if !strings.Contains(res[0].Reason, "0s") {
		t.Errorf("reason %q does not carry the case's own budget", res[0].Reason)
	}
}

// A case that declares no wall reports the default it actually waited on.
func TestTimeoutReasonNamesTheDefaultWall(t *testing.T) {
	if got := budget(spec.Case{}).Round(time.Second).String(); got != "1m30s" {
		t.Fatalf("default budget = %s, want 1m30s; the reason would print the wrong wall", got)
	}
}

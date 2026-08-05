package runed

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/kronael/arizuko/container"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/types"
)

const (
	circuitBreakerThreshold = 3
	defaultRunTTL           = 20 * time.Minute
	defaultMaxConcurrent    = 5
)

// Manager owns the execution plane's run lifecycle: per-folder
// serialization (one live spawn per folder), the global concurrency cap,
// the steer-into-running path, the circuit breaker, token brokering, and
// the Runtime envelope. It is the body behind POST /v1/runs (spec 5/P §
// The routd↔runed interface, § The queue + container model).
//
// Admission is a pure claim-or-reject decision off the DB — runed keeps NO
// internal admission queue (that duplicated routd's DB-backed dispatch
// queue). A caller that can't be admitted gets a retryable busy outcome;
// routd re-feeds it on its own queue.
//
// State is DB-backed for restart recovery:
//   - active spawns: spawns WHERE state IN ('queued','running')
//   - failure counts: circuit_breaker table
//   - activeCount: COUNT(*) on live spawns
//
// The only in-memory state is steerFns: the live steer callbacks
// (container-lifetime, inherently non-persistable). The mutex guards just
// the atomic count-and-claim of the admit path.
type Manager struct {
	db *DB
	// executors dispatches the post-claim step by spawns.kind (spec 5/8
	// "Filesystem restore claims the folder's run slot"): KindAgent is wired
	// by NewManager from the production Runtime; other kinds (KindHold, and
	// any future folder-exclusive job) register via RegisterExecutor.
	// Executor IS the existing Runtime interface — no new interface for a
	// new kind.
	executors map[string]Runtime
	scopes    []types.Scope // runed's service scope ceiling for brokered tokens
	runTTL    time.Duration
	instance  string
	maxRun    int

	mu       sync.Mutex
	steerFns map[string]func(batch string) bool // folder -> steer callback (runtime-wired)
}

// ManagerConfig wires the Manager. Scopes is the ceiling for every
// brokered agent token (downscope guarantees scope ⊆ this ∩ requested).
// MaxConcurrent caps total live spawns (MAX_CONCURRENT_CONTAINERS).
type ManagerConfig struct {
	Scopes        []types.Scope
	RunTTL        time.Duration
	Instance      string
	MaxConcurrent int
}

// NewManager builds the run Manager.
func NewManager(db *DB, runtime Runtime, cfg ManagerConfig) *Manager {
	if cfg.RunTTL == 0 {
		cfg.RunTTL = defaultRunTTL
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = defaultMaxConcurrent
	}
	return &Manager{
		db:        db,
		executors: map[string]Runtime{KindAgent: runtime},
		scopes:    cfg.Scopes,
		runTTL:    cfg.RunTTL,
		instance:  cfg.Instance,
		maxRun:    cfg.MaxConcurrent,
		steerFns:  map[string]func(batch string) bool{},
	}
}

// RegisterExecutor wires an additional kind's post-claim executor onto the
// shared dispatch site (KindHold — spec 5/8). KindAgent is reserved for the
// production Runtime NewManager already wired and cannot be replaced here.
func (m *Manager) RegisterExecutor(kind string, exec Runtime) {
	if kind == "" || kind == KindAgent || exec == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.executors[kind] = exec
}

// executor resolves kind to its Executor, defaulting an empty kind to
// 'agent' (every pre-kind caller). Used only by cleanup paths (Kill) that
// must not fail on an already-validated spawn; the admission paths call
// requireExecutor up front and fail loud on an unknown kind instead of
// silently falling back.
func (m *Manager) executor(kind string) Runtime {
	m.mu.Lock()
	exec, ok := m.executors[kindOf(kind)]
	m.mu.Unlock()
	if !ok {
		return m.executors[KindAgent]
	}
	return exec
}

// requireExecutor rejects a kind with no registered executor. A caller or
// programming error, not a transient condition — admitting a spawn with
// nothing to dispatch to would claim the folder and never release it.
func (m *Manager) requireExecutor(kind string) error {
	m.mu.Lock()
	_, known := m.executors[kind]
	m.mu.Unlock()
	if !known {
		return fmt.Errorf("runed: unknown run kind %q", kind)
	}
	return nil
}

// kindOf defaults an empty wire kind to KindAgent — spawns.kind's column
// default, so every pre-kind caller and row reads the same either way.
func kindOf(kind string) string {
	if kind == "" {
		return KindAgent
	}
	return kind
}

// Run executes (or steers) one agent turn as a pure claim-or-reject
// executor — routd owns all queueing (spec 5/P § Run state). Three exits:
//   - admitted (idle folder, under cap): claim the slot, spawn, and block
//     until the run completes, returning the turn-boundary outcome.
//   - steered (folder busy, live container): write the batch in and return
//     steered:true immediately (an ack, not a turn-boundary outcome).
//   - busy (folder busy with a dead container, or global cap hit): return
//     busy:true immediately. runed does NOT queue; routd re-feeds on its own
//     DB-backed dispatch queue.
func (m *Manager) Run(ctx context.Context, req runedv1.RunRequest) (runedv1.RunOutcome, error) {
	kind := kindOf(req.Kind)
	if err := m.requireExecutor(kind); err != nil {
		return runedv1.RunOutcome{}, err
	}
	folder := string(req.Folder)

	// A new inbound resets the breaker for a broken folder (spec 5/P §
	// circuit breaker: "a new inbound resets it"). Scoped to KindAgent
	// (spec 5/8): a hold request is not an agent inbound and must not
	// reset the agent's failure streak. Done before admission so a retry
	// after 3 failures actually spawns.
	if kind == KindAgent {
		if failures, _ := m.db.GetFailures(folder); failures >= circuitBreakerThreshold {
			_ = m.db.ResetFailures(folder)
		}
	}

	claim, out, ok, err := m.admit(req, kind)
	if err != nil || !ok {
		return out, err
	}
	return m.spawn(ctx, req, claim.runID, claim.sessionID, claim.containerName), nil
}

// Hold claims a folder's run slot for an EXTERNAL caller (POST /v1/holds) and
// returns the handle immediately, leaving the slot claimed until the caller
// releases it (DELETE /v1/runs/{run_id}) or RunTTL expires it. It is a pause
// on the folder expressed as a run, not a second locking mechanism: the same
// admit() claim-or-reject step an agent turn goes through, so a hold inherits
// per-folder exclusion, the busy→routd-requeues backpressure, the RunTTL
// wedge protection, and spawns visibility (spec 5/8).
//
// Busy=true means the folder already has a live run — an agent turn or
// another hold — and nothing was claimed.
func (m *Manager) Hold(ctx context.Context, folder types.Folder, reason string) (runedv1.HoldOutcome, error) {
	if err := m.requireExecutor(KindHold); err != nil {
		return runedv1.HoldOutcome{}, err
	}
	// Reason rides on the spawn's topic so an operator looking at dashd's
	// runed page sees WHY the folder is held, with no column of its own.
	req := runedv1.RunRequest{Folder: folder, Topic: reason, Kind: KindHold}
	claim, _, ok, err := m.admit(req, KindHold)
	if err != nil {
		return runedv1.HoldOutcome{}, err
	}
	if !ok {
		return runedv1.HoldOutcome{Busy: true}, nil
	}

	// Detach from the HTTP request ctx — it dies the moment this response is
	// written, and the hold must outlive it — while keeping its values so the
	// run stays on the caller's trace. spawn() re-wraps with the RunTTL
	// deadline, so a holder that vanishes still frees the folder.
	go m.spawn(context.WithoutCancel(ctx), req, claim.runID, claim.sessionID, claim.containerName)
	return runedv1.HoldOutcome{RunID: claim.runID}, nil
}

// claim identifies a won run slot: what the post-claim step needs to execute.
type claim struct {
	runID         string
	sessionID     string
	containerName string
}

// admit is the atomic claim-or-reject critical section — the ONE place a
// folder's run slot is taken, shared by Run (an agent turn, executed inline)
// and Hold (an external folder-exclusive job, executed detached). m.mu
// serializes check-and-claim so concurrent callers can never both pass the
// cap or both claim one folder (single-process runed, so the mutex IS the
// BEGIN IMMEDIATE claim).
//
// ok=false means the caller returns out verbatim: a steer ack, or a busy
// reject routd re-feeds from its own queue.
func (m *Manager) admit(req runedv1.RunRequest, kind string) (c claim, out runedv1.RunOutcome, ok bool, err error) {
	folder := string(req.Folder)
	m.mu.Lock()

	// Folder busy? Steer into the live container, else reject as busy.
	active, err := m.db.GetActiveSpawn(folder)
	if err != nil {
		m.mu.Unlock()
		return claim{}, runedv1.RunOutcome{}, false, fmt.Errorf("check active spawn: %w", err)
	}
	if active != nil {
		// A non-agent request (e.g. a hold) carries no message and must
		// never be steered into a live agent's running container — steering
		// an empty batch into whatever turn is live is a real bug, not a
		// hypothetical (spec 5/8). Only KindAgent attempts the steer.
		var steered bool
		if kind == KindAgent {
			steer := m.steerFns[folder]
			steered = steer != nil && steer(req.MessageBatch)
		}
		runID, sessionID := active.RunID, active.SessionID
		m.mu.Unlock()
		if steered {
			_ = m.db.MarkSteered(runID)
			return claim{}, runedv1.RunOutcome{
				RunID: runID, Outcome: runedv1.OutcomeOK,
				SessionID: sessionID, Steered: true,
			}, false, nil
		}
		// Steer failed (container already exited), not attempted (non-agent
		// kind), or not yet wired: reject as busy. routd re-feeds next poll
		// (no internal queue).
		return claim{}, runedv1.RunOutcome{Busy: true}, false, nil
	}

	// Global concurrency cap: reject as busy when full (routd re-feeds).
	// Scoped to KindAgent (spec 5/8) — MaxConcurrent (MAX_CONCURRENT_
	// CONTAINERS) bounds container/host resource usage, which a
	// containerless kind doesn't consume. A non-agent kind is uncapped
	// here; its serialization comes entirely from the per-folder
	// exclusivity above (one live spawn per folder, any kind).
	if kind == KindAgent {
		activeCount, cerr := m.db.ActiveAgentCount()
		if cerr != nil {
			m.mu.Unlock()
			return claim{}, runedv1.RunOutcome{}, false, fmt.Errorf("check active count: %w", cerr)
		}
		if activeCount >= m.maxRun {
			m.mu.Unlock()
			return claim{}, runedv1.RunOutcome{Busy: true}, false, nil
		}
	}

	// Idle folder, under cap: claim the slot by creating the spawn row NOW
	// (while holding the lock) so concurrent callers see it immediately.
	c = claim{
		runID:     "run_" + randHex(8),
		sessionID: req.SessionID,
		containerName: fmt.Sprintf("arizuko-%s-%s-%d",
			m.instance, container.SanitizeFolder(folder), time.Now().UnixMilli()),
	}
	if c.sessionID == "" {
		c.sessionID = newUUID()
	}
	if err := m.db.CreateSpawn(Spawn{
		RunID: c.runID, Folder: folder, Topic: req.Topic, ContainerName: c.containerName,
		SessionID: c.sessionID, Kind: kind, State: "queued",
	}); err != nil {
		m.mu.Unlock()
		return claim{}, runedv1.RunOutcome{}, false, fmt.Errorf("create spawn: %w", err)
	}
	m.mu.Unlock()
	return c, runedv1.RunOutcome{}, true, nil
}

// spawn runs the full execution-session envelope for one fresh spawn. The
// spawn row is already created (under the Run lock) to claim the slot.
func (m *Manager) spawn(ctx context.Context, req runedv1.RunRequest, runID, sessionID, containerName string) runedv1.RunOutcome {
	folder := string(req.Folder)
	kind := kindOf(req.Kind)
	exec := m.executor(kind) // admit's caller already validated the kind.

	// Isolated (timed-isolated:*) runs are one-off containers, and a
	// non-agent kind has no agent session at all: neither gets a
	// session_log row. The spawns row still exists for GET/DELETE.
	var logID int64
	if !req.Isolated && kind == KindAgent {
		logID, _ = m.db.RecordSession(folder, sessionID)
		_ = m.db.SetSpawnSessionLogID(runID, logID)
	}

	_ = m.db.StartSpawn(runID, sessionID)

	// RunTTL's kill-deadline is armed HERE, at the shared dispatch site, by
	// wrapping ctx with a deadline — not inside any one executor (spec 5/8
	// "wedge protection... uniform across kinds": every kind's executor
	// gets this for free by honoring ctx cancellation, the ordinary Go
	// contract, instead of each one reimplementing a RunTTL-specific
	// timer). dockerRuntime's armCancel already kills on ctx.Done()
	// regardless of cause (deadline or an explicit cancel from routd
	// dropping the request), so wrapping here is the only change it needs.
	runCtx := ctx
	if m.runTTL > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, m.runTTL)
		defer cancel()
	}

	// RegisterSteer wires the steer callback into the live-run slot once the
	// Runtime's container + IPC are up, so a concurrent POST /v1/runs steers
	// into it instead of spawning afresh. A containerless (non-agent)
	// executor simply never calls it, registering no steer callback.
	res := exec.Run(runCtx, RunSpec{
		RunID: runID, Folder: folder, ContainerName: containerName,
		Topic: req.Topic, ChatJID: req.ChatJID, Channel: req.Channel,
		SessionID: sessionID, MessageBatch: req.MessageBatch,
		TriggerSender: req.TriggerSender, CallerSub: req.CallerSub,
		TurnID: req.TurnID, Kind: kind, Isolated: req.Isolated,
		Elevated: req.Elevated,
		Model:    req.Model, ContainerConfig: req.ContainerConfig,
		ShareReadOnly: req.ShareReadOnly, Egress: req.Egress, WebPublish: req.WebPublish,
		EgressAllowlist: req.EgressAllowlist,
		Secrets:         req.Secrets,
		RunTTL:          m.runTTL,
		RegisterSteer:   func(steer func(batch string) bool) { m.SetSteer(folder, runID, steer) },
	})

	state := "exited"
	failed := res.Outcome == runedv1.OutcomeError
	if failed {
		state = "error"
	}
	endSession := sessionID
	if res.NewSessionID != "" {
		endSession = res.NewSessionID
	}
	if err := m.db.EndSpawn(runID, state, res.Outcome, res.ExitCode); err != nil {
		// spawn row stays 'running' if this fails → zombie that blocks the cap.
		// Operator must reset it manually: UPDATE spawns SET state='exited' WHERE run_id=?
		slog.Error("runed: EndSpawn failed — spawn may become zombie", "run_id", runID, "err", err)
	}
	if !req.Isolated && kind == KindAgent {
		if err := m.db.EndSession(logID, res.NewSessionID, res.Outcome, res.Error, res.MessageCount); err != nil {
			slog.Error("runed: EndSession failed", "log_id", logID, "err", err)
		}
	}
	breakerTripped := m.endRun(folder, runID, failed, kind)

	out := runedv1.RunOutcome{
		RunID: runID, Outcome: res.Outcome, SessionID: endSession, Error: res.Error,
	}
	// The run that pushes the folder to the threshold reports breaker_open on
	// the response the caller awaits (no separate endpoint) — and it actually
	// ran (spec 5/P § circuit breaker).
	if breakerTripped {
		out.BreakerOpen = true
		if out.Error == "" {
			out.Error = "circuit breaker open"
		}
	}
	return out
}

// endRun clears the steer callback and updates the breaker counter in the
// DB. Returns true when this exit trips the breaker (failure count reaches
// the threshold on this run). The slot frees implicitly (no running row);
// there is no admission queue to drain — routd re-feeds next poll.
//
// Breaker accounting is scoped to KindAgent (spec 5/8): a failed hold run
// must never count against the agent's breaker and stop it spawning —
// spawns.state/outcome/exit_code still record the hold's own
// success/failure (EndSpawn, unconditional), only the breaker's
// interpretation of failure changes here.
func (m *Manager) endRun(folder, runID string, failed bool, kind string) bool {
	var tripped bool
	if kind == KindAgent {
		if failed {
			newCount, _ := m.db.IncrFailures(folder)
			tripped = newCount == circuitBreakerThreshold
		} else {
			// Any clean exit resets the breaker (silent included); a folder
			// alternating error/silent must never creep to the threshold.
			_ = m.db.ResetFailures(folder)
		}
	}

	// Clear the steer callback (the container is gone).
	m.mu.Lock()
	delete(m.steerFns, folder)
	m.mu.Unlock()
	return tripped
}

// SetSteer wires a folder's live-run steer callback (the IPC write +
// SIGUSR1). The production Runtime calls this once the container is up so
// a concurrent POST /v1/runs can steer into it.
func (m *Manager) SetSteer(folder, runID string, steer func(batch string) bool) {
	// Verify the run_id is still active for this folder before wiring.
	active, _ := m.db.GetActiveSpawn(folder)
	if active == nil || active.RunID != runID {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.steerFns[folder] = steer
}

// Kill stops a run (DELETE /v1/runs/{id}) and frees its slot. The spawn's
// recorded kind selects which executor's Kill runs (spec 5/8) — a backup
// run's Kill is dispatched the same way an agent's is, no special-casing.
// Idempotent: killing an already-exited run is a no-op 200. A deliberate
// kill records state=killed WITHOUT outcome=error and does NOT count toward
// the breaker (it's operator intent, not a run failure). The slot frees
// implicitly once the row is terminal; routd re-feeds any pending batch.
func (m *Manager) Kill(runID string) error {
	sp, err := m.db.GetSpawn(runID)
	if err != nil {
		return err
	}
	live := sp.State == "running" || sp.State == "queued"
	if live {
		_ = m.executor(sp.Kind).Kill(sp.ContainerName)
		// KillSpawn re-checks the state in SQL — a run that completed normally
		// between GetSpawn and here keeps its terminal state (not 'killed').
		_ = m.db.KillSpawn(runID)
	}
	// Clear the steer callback (the container is gone).
	m.mu.Lock()
	delete(m.steerFns, sp.Folder)
	m.mu.Unlock()
	return nil
}

// StopFolder is the operator-kill path (routd's /stop): it resolves the
// folder's live spawn and Kills it. Returns the killed run_id (or "" + killed
// false when the folder has no live spawn — routd renders the no-active text).
// Kill records state=killed without counting toward the breaker.
func (m *Manager) StopFolder(folder string) (runID string, killed bool, err error) {
	runID = m.ActiveRunID(folder)
	if runID == "" {
		return "", false, nil
	}
	if err := m.Kill(runID); err != nil {
		return "", false, err
	}
	return runID, true, nil
}

// ActiveRunID returns the run_id of a folder's live spawn, or "" when none.
func (m *Manager) ActiveRunID(folder string) string {
	runID, _ := m.db.ActiveSpawnForFolder(folder)
	return runID
}

// ActiveCount returns the number of live spawns (test/observability).
func (m *Manager) ActiveCount() int {
	n, _ := m.db.ActiveCount()
	return n
}

// intersect returns the requested scope ∩ the ceiling. Empty or fully
// disjoint requested scope yields the EMPTY brokered scope (fail closed) —
// runed never broadens an agent to its full ceiling on a missing/bad ask.
func intersect(ceiling, want []types.Scope) []types.Scope {
	set := map[types.Scope]bool{}
	for _, s := range ceiling {
		set[s] = true
	}
	out := []types.Scope{}
	for _, s := range want {
		if set[s] {
			out = append(out, s)
		}
	}
	return out
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// newUUID mints a RFC4122 v4 UUID (the harness session id; opaque to routd).
func newUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

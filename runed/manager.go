package runed

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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
	db       *DB
	runtime  Runtime
	broker   Broker
	scopes   []types.Scope // runed's service scope ceiling for brokered tokens
	runTTL   time.Duration
	instance string
	maxRun   int

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
func NewManager(db *DB, runtime Runtime, broker Broker, cfg ManagerConfig) *Manager {
	if cfg.RunTTL == 0 {
		cfg.RunTTL = defaultRunTTL
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = defaultMaxConcurrent
	}
	return &Manager{
		db:       db,
		runtime:  runtime,
		broker:   broker,
		scopes:   cfg.Scopes,
		runTTL:   cfg.RunTTL,
		instance: cfg.Instance,
		maxRun:   cfg.MaxConcurrent,
		steerFns: map[string]func(batch string) bool{},
	}
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
	folder := string(req.Folder)

	// A new inbound resets the breaker for a broken folder (spec 5/P §
	// circuit breaker: "a new inbound resets it"). Done before admission so
	// a retry after 3 failures actually spawns.
	if failures, _ := m.db.GetFailures(folder); failures >= circuitBreakerThreshold {
		_ = m.db.ResetFailures(folder)
	}

	// The admission decision and spawn-row creation are one atomic critical
	// section: m.mu serializes check-and-claim so concurrent Runs can never
	// both pass the cap or both spawn one folder (single-process runed, so
	// the mutex IS the BEGIN IMMEDIATE claim).
	m.mu.Lock()

	// Folder busy? Steer into the live container, else reject as busy.
	active, err := m.db.GetActiveSpawn(folder)
	if err != nil {
		m.mu.Unlock()
		return runedv1.RunOutcome{}, fmt.Errorf("check active spawn: %w", err)
	}
	if active != nil {
		steer := m.steerFns[folder]
		steered := steer != nil && steer(req.MessageBatch)
		runID, sessionID := active.RunID, active.SessionID
		m.mu.Unlock()
		if steered {
			_ = m.db.MarkSteered(runID)
			return runedv1.RunOutcome{
				RunID: runID, Outcome: runedv1.OutcomeOK,
				SessionID: sessionID, Steered: true,
			}, nil
		}
		// Steer failed (container already exited) or not yet wired: reject as
		// busy. routd re-feeds next poll (no internal queue).
		return runedv1.RunOutcome{Busy: true}, nil
	}

	// Global concurrency cap: reject as busy when full (routd re-feeds).
	activeCount, err := m.db.ActiveCount()
	if err != nil {
		m.mu.Unlock()
		return runedv1.RunOutcome{}, fmt.Errorf("check active count: %w", err)
	}
	if activeCount >= m.maxRun {
		m.mu.Unlock()
		return runedv1.RunOutcome{Busy: true}, nil
	}

	// Idle folder, under cap: claim the slot by creating the spawn row NOW
	// (while holding the lock) so concurrent Runs see it immediately.
	runID := "run_" + randHex(8)
	sessionID := req.SessionID
	if sessionID == "" {
		sessionID = newUUID()
	}
	containerName := fmt.Sprintf("arizuko-%s-%s-%d", m.instance, container.SanitizeFolder(folder), time.Now().UnixMilli())
	if err := m.db.CreateSpawn(Spawn{
		RunID: runID, Folder: folder, Topic: req.Topic, ContainerName: containerName,
		SessionID: sessionID, State: "queued",
	}); err != nil {
		m.mu.Unlock()
		return runedv1.RunOutcome{}, fmt.Errorf("create spawn: %w", err)
	}
	m.mu.Unlock()

	return m.spawn(ctx, req, runID, sessionID, containerName), nil
}

// spawn runs the full execution-session envelope for one fresh spawn. The
// spawn row is already created (under the Run lock) to claim the slot.
func (m *Manager) spawn(ctx context.Context, req runedv1.RunRequest, runID, sessionID, containerName string) runedv1.RunOutcome {
	folder := string(req.Folder)

	// Isolated (timed-isolated:*) runs are one-off containers: no session_log
	// row. The spawns row still exists for GET/DELETE.
	var logID int64
	if !req.Isolated {
		logID, _ = m.db.RecordSession(folder, sessionID)
		// Update the spawn row with the session_log_id.
		_ = m.db.SetSpawnSessionLogID(runID, logID)
	}

	// broker the downscoped capability token (spec 5/P § brokering).
	want := intersect(m.scopes, req.CapabilityScopes)
	jws, jti, expiresAt, berr := m.broker.Broker(ctx, req.CallerSub, folder, want, m.runTTL)
	if berr != nil {
		if err := m.db.EndSpawn(runID, "error", runedv1.OutcomeError, -1); err != nil {
			slog.Error("runed: EndSpawn failed on broker error — spawn may become zombie", "run_id", runID, "err", err)
		}
		if !req.Isolated {
			if err := m.db.EndSession(logID, "", runedv1.OutcomeError, "broker: "+berr.Error(), 0); err != nil {
				slog.Error("runed: EndSession failed on broker error", "log_id", logID, "err", err)
			}
		}
		m.endRun(folder, runID, true)
		return runedv1.RunOutcome{RunID: runID, Outcome: runedv1.OutcomeError, Error: "broker: " + berr.Error()}
	}

	scopeJSON, _ := json.Marshal(want)
	_ = m.db.RecordToken(jti, runID, "service:runed", folder, string(scopeJSON), expiresAt)
	_ = m.db.SetSpawnToken(runID, jti)
	_ = m.db.StartSpawn(runID, sessionID)

	// Enforce runTTL as a kill-deadline: m.runTTL is the intended run ceiling
	// (broker token TTL), but a CONTAINER_TIMEOUT > runTTL would let the
	// container outrun it. The Runtime honors RunTTL from within the run path
	// (kill armed once the run is underway, stopped when it returns) so no
	// detached manager timer races container creation.
	//
	// RegisterSteer wires the steer callback into the live-run slot once the
	// Runtime's container + IPC are up, so a concurrent POST /v1/runs steers
	// into it instead of spawning afresh.
	res := m.runtime.Run(ctx, RunSpec{
		RunID: runID, Folder: folder, ContainerName: containerName,
		Topic: req.Topic, ChatJID: req.ChatJID, Channel: req.Channel,
		SessionID: sessionID, MessageBatch: req.MessageBatch,
		TriggerSender: req.TriggerSender, CallerSub: req.CallerSub,
		TurnID: req.TurnID, Token: jws, Isolated: req.Isolated,
		Elevated: req.Elevated,
		Model:    req.Model, ContainerConfig: req.ContainerConfig,
		Grants: req.Grants, EgressAllowlist: req.EgressAllowlist,
		Secrets:       req.Secrets,
		RunTTL:        m.runTTL,
		RegisterSteer: func(steer func(batch string) bool) { m.SetSteer(folder, runID, steer) },
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
	if !req.Isolated {
		if err := m.db.EndSession(logID, res.NewSessionID, res.Outcome, res.Error, res.MessageCount); err != nil {
			slog.Error("runed: EndSession failed", "log_id", logID, "err", err)
		}
	}
	breakerTripped := m.endRun(folder, runID, failed)

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
func (m *Manager) endRun(folder, runID string, failed bool) bool {
	// Update circuit breaker in DB.
	var tripped bool
	if failed {
		newCount, _ := m.db.IncrFailures(folder)
		tripped = newCount == circuitBreakerThreshold
	} else {
		// Any clean exit resets the breaker (silent included); a folder
		// alternating error/silent must never creep to the threshold.
		_ = m.db.ResetFailures(folder)
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

// Kill stops a run's container (DELETE /v1/runs/{id}) and frees its slot.
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
		_ = m.runtime.Kill(sp.ContainerName)
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

package runed

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
// serialization (one live spawn per folder), the global concurrency cap +
// waiting queue, the steer-into-running path, the circuit breaker, token
// brokering, and the Runtime envelope. It is the body behind POST /v1/runs
// (spec 5/P § The routd↔runed interface, § The queue + container model).
//
// State is DB-backed for restart recovery:
//   - active spawns: spawns WHERE state IN ('queued','running')
//   - failure counts: circuit_breaker table
//   - activeCount: COUNT(*) on live spawns
//
// Only two pieces remain in-memory:
//   - steerFns: the live steer callbacks (container-lifetime, non-persistable)
//   - waiting: the FIFO admission queue (waiters reconnect on restart)
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
	waiting  []*waiter                          // FIFO admission queue (over cap or folder busy)
}


// waiter is one Run blocked on admission (folder busy or cap reached); it
// is released (ch closed) when a slot frees AND its folder is idle.
type waiter struct {
	folder string
	ch     chan struct{}
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

// Run executes (or steers) one agent turn. Synchronous for the turn
// boundary: it blocks until the run completes, returning the outcome +
// session_id. If the folder already has a live spawn, it steers the batch
// into it and returns immediately with steered:true (an ack, not a
// turn-boundary outcome) — spec 5/P § Steer-into-running-container.
func (m *Manager) Run(ctx context.Context, req runedv1.RunRequest) (runedv1.RunOutcome, error) {
	folder := string(req.Folder)

	// A new inbound resets the breaker for a broken folder (spec 5/P §
	// circuit breaker: "a new inbound resets it"). Done before admission so
	// a retry after 3 failures actually spawns.
	if failures, _ := m.db.GetFailures(folder); failures >= circuitBreakerThreshold {
		_ = m.db.ResetFailures(folder)
	}

	for {
		// The admission decision and spawn-row creation must be atomic to
		// prevent concurrent runs from all passing the cap check. We hold
		// m.mu for the entire check-and-claim sequence.
		m.mu.Lock()

		// Check for a live spawn in the DB.
		active, err := m.db.GetActiveSpawn(folder)
		if err != nil {
			m.mu.Unlock()
			return runedv1.RunOutcome{}, fmt.Errorf("check active spawn: %w", err)
		}

		if active != nil {
			// Folder busy: try to steer into the running container.
			steer := m.steerFns[folder]
			steered := steer != nil && steer(req.MessageBatch)
			runID, sessionID := active.RunID, active.SessionID
			if steered {
				m.mu.Unlock()
				_ = m.db.MarkSteered(runID)
				return runedv1.RunOutcome{
					RunID: runID, Outcome: runedv1.OutcomeOK,
					SessionID: sessionID, Steered: true,
				}, nil
			}
			// Steer failed (container already exited) or not yet wired: queue
			// behind the live run, then retry the whole decision.
			w := &waiter{folder: folder, ch: make(chan struct{})}
			m.waiting = append(m.waiting, w)
			m.mu.Unlock()
			if err := waitFor(ctx, w.ch); err != nil {
				m.dropWaiter(w)
				return runedv1.RunOutcome{}, err
			}
			continue
		}

		// Check the global concurrency cap from DB.
		activeCount, err := m.db.ActiveCount()
		if err != nil {
			m.mu.Unlock()
			return runedv1.RunOutcome{}, fmt.Errorf("check active count: %w", err)
		}
		if activeCount >= m.maxRun {
			// At the global cap: queue and retry once a slot frees.
			w := &waiter{folder: folder, ch: make(chan struct{})}
			m.waiting = append(m.waiting, w)
			m.mu.Unlock()
			if err := waitFor(ctx, w.ch); err != nil {
				m.dropWaiter(w)
				return runedv1.RunOutcome{}, err
			}
			continue
		}

		// Idle folder, under cap: claim the slot by creating the spawn row
		// NOW (while holding the lock) so concurrent Runs see it immediately.
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
		m.db.EndSpawn(runID, "error", runedv1.OutcomeError, -1)
		if !req.Isolated {
			m.db.EndSession(logID, "", runedv1.OutcomeError, "broker: "+berr.Error(), 0)
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
		Model: req.Model, ContainerConfig: req.ContainerConfig,
		Grants: req.Grants, EgressAllowlist: req.EgressAllowlist,
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
	m.db.EndSpawn(runID, state, res.Outcome, res.ExitCode)
	if !req.Isolated {
		m.db.EndSession(logID, res.NewSessionID, res.Outcome, res.Error, res.MessageCount)
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

// endRun clears the steer callback, updates the breaker counter in the DB,
// and drains an admission waiter. Returns true when this exit trips the breaker
// (failure count reaches the threshold on this run).
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

	// Clear the steer callback and drain waiters.
	m.mu.Lock()
	delete(m.steerFns, folder)
	m.drainLocked()
	m.mu.Unlock()
	return tripped
}

// drainLocked releases FIFO waiters whose folder is now idle and that fit
// under the cap. Caller holds m.mu. A released Run re-checks admission under
// the lock (it may steer if the folder went busy again, or re-queue).
func (m *Manager) drainLocked() {
	activeCount, _ := m.db.ActiveCount()
	kept := m.waiting[:0]
	freed := map[string]bool{}
	for _, w := range m.waiting {
		folderActive, _ := m.db.ActiveSpawnForFolder(w.folder)
		if activeCount < m.maxRun && folderActive == "" && !freed[w.folder] {
			freed[w.folder] = true // one waiter per idle folder per drain pass
			close(w.ch)
			continue
		}
		kept = append(kept, w)
	}
	m.waiting = kept
}

// dropWaiter removes a cancelled waiter from the queue (its Run's ctx died).
func (m *Manager) dropWaiter(w *waiter) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, x := range m.waiting {
		if x == w {
			m.waiting = append(m.waiting[:i], m.waiting[i+1:]...)
			return
		}
	}
}

// waitFor blocks until the waiter is released or ctx is cancelled.
func waitFor(ctx context.Context, ch chan struct{}) error {
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
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

// Kill stops a run's container (DELETE /v1/runs/{id}) and frees its queue
// slot. Idempotent: killing an already-exited run is a no-op 200. A
// deliberate kill records state=killed WITHOUT outcome=error and does NOT
// count toward the breaker (it's operator intent, not a run failure).
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
	// Clear steer callback and drain waiters (the DB row is already terminal).
	m.mu.Lock()
	delete(m.steerFns, sp.Folder)
	m.drainLocked()
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

package runed

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

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
type Manager struct {
	db       *DB
	runtime  Runtime
	broker   Broker
	scopes   []types.Scope // runed's service scope ceiling for brokered tokens
	runTTL   time.Duration
	instance string
	maxRun   int

	mu          sync.Mutex
	active      map[string]*folderRun // folder -> live run (the exclusivity gate)
	failures    map[string]int        // folder -> consecutive failures (breaker)
	activeCount int                   // total live spawns (cap denominator)
	waiting     []*waiter             // FIFO admission queue (over cap or folder busy)
}

// folderRun tracks a folder's live spawn for the steer path + exclusivity.
type folderRun struct {
	runID     string
	sessionID string
	steer     func(batch string) bool // SIGUSR1 + IPC write; false = container already exited
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
		active:   map[string]*folderRun{},
		failures: map[string]int{},
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
	m.mu.Lock()
	if m.failures[folder] >= circuitBreakerThreshold {
		m.failures[folder] = 0
	}
	m.mu.Unlock()

	for {
		// One locked critical section: the steer-check AND the live-run
		// registration. Two concurrent runs for one idle folder cannot both
		// spawn — exactly one wins the registration; the other steers or
		// waits.
		m.mu.Lock()
		if fr := m.active[folder]; fr != nil {
			// Folder busy: try to steer into the running container.
			steered := fr.steer != nil && fr.steer(req.MessageBatch)
			runID, sessionID := fr.runID, fr.sessionID
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
		if m.activeCount >= m.maxRun {
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
		// Idle folder, under cap: resolve the effective session id (resume or
		// fresh) and register the live run now (still holding the lock) so a
		// racing Run that steers in sees the real session id, not the empty
		// req.SessionID a fresh spawn would otherwise carry until spawn() mints
		// one.
		runID := "run_" + randHex(8)
		sessionID := req.SessionID
		if sessionID == "" {
			sessionID = newUUID()
		}
		m.active[folder] = &folderRun{runID: runID, sessionID: sessionID}
		m.activeCount++
		m.mu.Unlock()
		return m.spawn(ctx, req, runID, sessionID), nil
	}
}

// spawn runs the full execution-session envelope for one fresh spawn. The
// live-run slot is already registered (under the Run lock) and is freed by
// endRun on every exit path.
func (m *Manager) spawn(ctx context.Context, req runedv1.RunRequest, runID, sessionID string) runedv1.RunOutcome {
	folder := string(req.Folder)
	containerName := fmt.Sprintf("arizuko-%s-%s-%d", m.instance, safeFolder(folder), time.Now().UnixMilli())

	// session id (resume or fresh) is resolved by Run under the lock and
	// stamped into the live-run slot before this spawn starts, so a racing
	// steer sees the real id.

	// broker the downscoped capability token (spec 5/P § brokering).
	want := intersect(m.scopes, req.CapabilityScopes)
	jws, jti, expiresAt, berr := m.broker.Broker(ctx, req.CallerSub, folder, want, m.runTTL)
	if berr != nil {
		m.endRun(folder, runID, runedv1.OutcomeError, true)
		return runedv1.RunOutcome{RunID: runID, Outcome: runedv1.OutcomeError, Error: "broker: " + berr.Error()}
	}

	// session_log + spawns rows.
	logID, _ := m.db.RecordSession(folder, sessionID)
	_ = m.db.CreateSpawn(Spawn{
		RunID: runID, Folder: folder, Topic: req.Topic, ContainerName: containerName,
		SessionLogID: logID, MCPTokenJTI: jti, SessionID: sessionID, State: "queued",
	})
	scopeJSON, _ := json.Marshal(want)
	_ = m.db.RecordToken(jti, runID, "service:runed", folder, string(scopeJSON), expiresAt)
	_ = m.db.StartSpawn(runID, sessionID)

	// run the envelope. RegisterSteer wires the steer callback into the
	// live-run slot once the Runtime's container + IPC are up, so a
	// concurrent POST /v1/runs steers into it instead of spawning afresh.
	res := m.runtime.Run(ctx, RunSpec{
		RunID: runID, Folder: folder, ContainerName: containerName,
		Topic: req.Topic, ChatJID: req.ChatJID,
		SessionID: sessionID, MessageBatch: req.MessageBatch,
		TriggerSender: req.TriggerSender, CallerSub: req.CallerSub,
		TurnID: req.TurnID, Token: jws, Isolated: req.Isolated,
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
	m.db.EndSession(logID, res.NewSessionID, res.Outcome, res.Error, res.MessageCount)
	breakerTripped := m.endRun(folder, runID, res.Outcome, failed)

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

// endRun frees the folder's live-run slot, updates the breaker counter, and
// drains an admission waiter. Returns true when this exit trips the breaker
// (failure count reaches the threshold on this run).
func (m *Manager) endRun(folder, runID, outcome string, failed bool) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	fr := m.active[folder]
	if fr == nil || fr.runID != runID {
		return false
	}
	tripped := false
	if failed {
		m.failures[folder]++
		tripped = m.failures[folder] == circuitBreakerThreshold
	} else if outcome == runedv1.OutcomeOK {
		m.failures[folder] = 0
	}
	delete(m.active, folder)
	if m.activeCount > 0 {
		m.activeCount--
	}
	m.drainLocked()
	return tripped
}

// drainLocked releases FIFO waiters whose folder is now idle and that fit
// under the cap. Caller holds m.mu. A released Run re-checks admission under
// the lock (it may steer if the folder went busy again, or re-queue).
func (m *Manager) drainLocked() {
	kept := m.waiting[:0]
	freed := map[string]bool{}
	for _, w := range m.waiting {
		if m.activeCount < m.maxRun && m.active[w.folder] == nil && !freed[w.folder] {
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
	m.mu.Lock()
	defer m.mu.Unlock()
	if fr := m.active[folder]; fr != nil && fr.runID == runID {
		fr.steer = steer
	}
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
		_ = m.db.EndSpawn(runID, "killed", "", -1)
	}
	// Free the slot if this run still owns its folder's live registration
	// (the synchronous spawn goroutine may not have returned yet).
	m.mu.Lock()
	if fr := m.active[sp.Folder]; fr != nil && fr.runID == runID {
		delete(m.active, sp.Folder)
		if m.activeCount > 0 {
			m.activeCount--
		}
		m.drainLocked()
	}
	m.mu.Unlock()
	return nil
}

// ActiveRunID returns the run_id of a folder's live spawn, or "" when none.
func (m *Manager) ActiveRunID(folder string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if fr := m.active[folder]; fr != nil {
		return fr.runID
	}
	return ""
}

// ActiveCount returns the number of live spawns (test/observability).
func (m *Manager) ActiveCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.activeCount
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

func safeFolder(folder string) string {
	return strings.NewReplacer("/", "-", " ", "-").Replace(folder)
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

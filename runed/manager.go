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
)

// Manager owns the execution plane's run lifecycle: per-folder
// serialization (one live spawn per folder), the steer-into-running path,
// the circuit breaker, token brokering, and the Runtime envelope. It is
// the body behind POST /v1/runs (spec 5/P § The routd↔runed interface).
type Manager struct {
	db      *DB
	runtime Runtime
	broker  Broker
	scopes  []types.Scope // runed's service scope ceiling for brokered tokens
	runTTL  time.Duration
	instance string

	mu     sync.Mutex
	active map[string]*folderRun // folder -> live run
}

// folderRun tracks a folder's live spawn for the steer path.
type folderRun struct {
	runID     string
	sessionID string
	steer     func(batch string) bool // SIGUSR1 + IPC write; false = container already exited
	failures  int
}

// ManagerConfig wires the Manager. Scopes is the ceiling for every
// brokered agent token (downscope guarantees scope ⊆ this ∩ requested).
type ManagerConfig struct {
	Scopes   []types.Scope
	RunTTL   time.Duration
	Instance string
}

// NewManager builds the run Manager.
func NewManager(db *DB, runtime Runtime, broker Broker, cfg ManagerConfig) *Manager {
	if cfg.RunTTL == 0 {
		cfg.RunTTL = defaultRunTTL
	}
	return &Manager{
		db:       db,
		runtime:  runtime,
		broker:   broker,
		scopes:   cfg.Scopes,
		runTTL:   cfg.RunTTL,
		instance: cfg.Instance,
		active:   map[string]*folderRun{},
	}
}

// Run executes (or steers) one agent turn. Synchronous for the turn
// boundary: it blocks until the run completes, returning the outcome +
// session_id. If the folder already has a live spawn, it steers the batch
// into it and returns immediately with steered:true (an ack, not a
// turn-boundary outcome) — spec 5/P § Steer-into-running-container.
func (m *Manager) Run(ctx context.Context, req runedv1.RunRequest) (runedv1.RunOutcome, error) {
	folder := string(req.Folder)

	// Steer path: a live spawn for this folder absorbs the batch.
	m.mu.Lock()
	if fr := m.active[folder]; fr != nil {
		steered := fr.steer != nil && fr.steer(req.MessageBatch)
		runID, sessionID := fr.runID, fr.sessionID
		m.mu.Unlock()
		if steered {
			_ = m.db.MarkSteered(runID)
			return runedv1.RunOutcome{
				RunID: runID, Outcome: runedv1.OutcomeOK,
				SessionID: sessionID, Steered: true,
			}, nil
		}
		// Steer failed (container already exited): fall through to a
		// fresh synchronous spawn.
	} else {
		m.mu.Unlock()
	}

	return m.spawn(ctx, req)
}

// spawn runs the full execution-session envelope for one fresh spawn.
func (m *Manager) spawn(ctx context.Context, req runedv1.RunRequest) (runedv1.RunOutcome, error) {
	folder := string(req.Folder)
	runID := "run_" + randHex(8)
	containerName := fmt.Sprintf("arizuko-%s-%s-%d", m.instance, safeFolder(folder), time.Now().UnixMilli())

	// 1. resolve session id (resume or fresh).
	sessionID := req.SessionID
	if sessionID == "" {
		sessionID = newUUID()
	}

	// 2. broker the downscoped capability token (spec 5/P § brokering).
	ttl := m.runTTL
	want := intersect(m.scopes, req.CapabilityScopes)
	jws, jti, expiresAt, berr := m.broker.Broker(ctx, req.CallerSub, folder, want, ttl)
	if berr != nil {
		return runedv1.RunOutcome{RunID: runID, Outcome: runedv1.OutcomeError, Error: "broker: " + berr.Error()}, nil
	}

	// 3. session_log + spawns rows.
	logID, _ := m.db.RecordSession(folder, sessionID)
	_ = m.db.CreateSpawn(Spawn{
		RunID: runID, Folder: folder, Topic: req.Topic, ContainerName: containerName,
		SessionLogID: logID, MCPTokenJTI: jti, SessionID: sessionID, State: "queued",
	})
	scopeJSON, _ := json.Marshal(want)
	_ = m.db.RecordToken(jti, runID, "service:runed", folder, string(scopeJSON), expiresAt)
	_ = m.db.StartSpawn(runID, sessionID)

	// register live run for the steer path.
	fr := &folderRun{runID: runID, sessionID: sessionID}
	m.mu.Lock()
	if prev := m.active[folder]; prev != nil {
		fr.failures = prev.failures
	}
	// circuit breaker: 3 consecutive failures opens it.
	breakerOpen := fr.failures >= circuitBreakerThreshold
	m.active[folder] = fr
	m.mu.Unlock()

	if breakerOpen {
		m.endRun(folder, runID, "error", runedv1.OutcomeError, 1, "", "circuit breaker open", 0)
		return runedv1.RunOutcome{RunID: runID, Outcome: runedv1.OutcomeError,
			Error: "circuit breaker open", BreakerOpen: true}, nil
	}

	// 4-7. run the envelope (the Runtime owns socket/spawn/stream/teardown).
	res := m.runtime.Run(ctx, RunSpec{
		RunID: runID, Folder: folder, Topic: req.Topic, ChatJID: req.ChatJID,
		SessionID: req.SessionID, MessageBatch: req.MessageBatch,
		TriggerSender: req.TriggerSender, CallerSub: req.CallerSub,
		TurnID: req.TurnID, Token: jws, Isolated: req.Isolated,
	})

	state := "exited"
	if res.Outcome == runedv1.OutcomeError {
		state = "error"
	}
	endSession := sessionID
	if res.NewSessionID != "" {
		endSession = res.NewSessionID
	}
	m.endRun(folder, runID, state, res.Outcome, res.ExitCode, res.NewSessionID, res.Error, res.MessageCount)
	_ = m.db.EndSession(logID, res.NewSessionID, res.Outcome, res.Error, res.MessageCount)

	return runedv1.RunOutcome{
		RunID: runID, Outcome: res.Outcome, SessionID: endSession, Error: res.Error,
	}, nil
}

// endRun records the spawn's terminal state and updates the folder's
// breaker counter, freeing the live-run slot.
func (m *Manager) endRun(folder, runID, state, outcome string, exitCode int, newSession, errMsg string, msgs int) {
	_ = m.db.EndSpawn(runID, state, outcome, exitCode)
	m.mu.Lock()
	defer m.mu.Unlock()
	fr := m.active[folder]
	if fr == nil || fr.runID != runID {
		return
	}
	if outcome == runedv1.OutcomeError {
		fr.failures++
	} else {
		fr.failures = 0
	}
	// keep the failure count on the folder for the next spawn's breaker,
	// but clear the live-run slot.
	next := &folderRun{failures: fr.failures}
	m.active[folder] = next
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

// ActiveRunID returns the run_id of a folder's live spawn, or "" when none.
func (m *Manager) ActiveRunID(folder string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if fr := m.active[folder]; fr != nil {
		return fr.runID
	}
	return ""
}

func intersect(ceiling, want []types.Scope) []types.Scope {
	if len(want) == 0 {
		return ceiling
	}
	set := map[types.Scope]bool{}
	for _, s := range ceiling {
		set[s] = true
	}
	var out []types.Scope
	for _, s := range want {
		if set[s] {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return ceiling
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

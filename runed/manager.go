package runed

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/types"
)

const defaultMaxConcurrent = 5

// Manager owns the execution plane's run lifecycle: per-topic serialization
// (one live spawn per folder and topic; a hold takes the whole folder), the
// global concurrency cap and the Runtime envelope
// (spec 5/P-runed). Admission is a pure claim-or-reject decision off the DB; runed
// keeps NO internal admission queue, and a caller that cannot be admitted
// gets a retryable busy outcome and routd re-feeds it.
type Manager struct {
	db *DB
	// executors dispatches the post-claim step by spawns.kind (spec 5/8):
	// KindAgent is wired by NewManager, other kinds register via
	// RegisterExecutor. Executor IS the Runtime interface.
	executors map[string]Runtime
	runTTL    time.Duration
	instance  string
	maxRun    int
	// cfg + folders are what renderTurn reads: with the request they decide
	// every fact one turn delivers to its guest (spec 6/41 §"The renderer").
	cfg     *core.Config
	folders *groupfolder.Resolver

	mu sync.Mutex
}

// ManagerConfig wires the renderer, deadline and concurrent agent cap.
type ManagerConfig struct {
	RunTTL    time.Duration
	Instance  string
	MaxAgents int
	// Cfg + Folders wire the turn renderer. An agent run refuses without them:
	// there is no envelope to hand a substrate.
	Cfg     *core.Config
	Folders *groupfolder.Resolver
}

// NewManager builds the run Manager.
func NewManager(db *DB, runtime Runtime, cfg ManagerConfig) *Manager {
	if cfg.RunTTL == 0 {
		cfg.RunTTL = core.RunTimeout()
	}
	if cfg.MaxAgents <= 0 {
		cfg.MaxAgents = defaultMaxConcurrent
	}
	return &Manager{
		db:        db,
		executors: map[string]Runtime{KindAgent: runtime},
		runTTL:    cfg.RunTTL,
		instance:  cfg.Instance,
		maxRun:    cfg.MaxAgents,
		cfg:       cfg.Cfg,
		folders:   cfg.Folders,
	}
}

// RegisterExecutor wires an additional kind's post-claim executor (spec 5/8).
// KindAgent is reserved for the Runtime NewManager wired.
func (m *Manager) RegisterExecutor(kind string, exec Runtime) {
	if kind == "" || kind == KindAgent || exec == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.executors[kind] = exec
}

// executor resolves kind to its Executor, defaulting an empty kind to 'agent'.
// Used only by cleanup paths (Kill) that must not fail on an already-validated
// spawn; admission calls requireExecutor and fails loud.
func (m *Manager) executor(kind string) Runtime {
	m.mu.Lock()
	exec, ok := m.executors[kindOf(kind)]
	m.mu.Unlock()
	if !ok {
		return m.executors[KindAgent]
	}
	return exec
}

// requireExecutor rejects a kind with no registered executor: admitting a
// spawn with nothing to dispatch to would claim the folder and never release
// it.
func (m *Manager) requireExecutor(kind string) error {
	m.mu.Lock()
	_, known := m.executors[kind]
	m.mu.Unlock()
	if !known {
		return fmt.Errorf("runed: unknown run kind %q", kind)
	}
	return nil
}

// kindOf defaults an empty wire kind to KindAgent.
func kindOf(kind string) string {
	if kind == "" {
		return KindAgent
	}
	return kind
}

// Run executes one agent turn as a pure claim-or-reject executor; routd owns
// all queueing. A busy folder or global cap rejects without claiming a slot.
func (m *Manager) Run(ctx context.Context, req runedv1.RunRequest) (runedv1.RunOutcome, error) {
	kind := kindOf(req.Kind)
	if err := m.requireExecutor(kind); err != nil {
		return runedv1.RunOutcome{}, err
	}
	claim, out, ok, err := m.admit(ctx, req, kind)
	if err != nil || !ok {
		return out, err
	}
	return m.spawn(ctx, req, claim.runID, claim.sessionID, claim.containerName), nil
}

// Hold claims a folder's run slot for an EXTERNAL caller and returns the
// handle immediately, leaving the slot claimed until the caller releases it
// or RunTTL expires it. It is a pause on the folder expressed as a run, not a
// second locking mechanism. Busy=true means nothing was claimed.
func (m *Manager) Hold(ctx context.Context, folder types.Folder, reason string) (runedv1.HoldOutcome, error) {
	if err := m.requireExecutor(KindHold); err != nil {
		return runedv1.HoldOutcome{}, err
	}
	// Reason rides on the spawn's topic so an operator looking at dashd's
	// runed page sees WHY the folder is held, with no column of its own.
	req := runedv1.RunRequest{Folder: folder, Topic: reason, Kind: KindHold}
	claim, _, ok, err := m.admit(ctx, req, KindHold)
	if err != nil {
		return runedv1.HoldOutcome{}, err
	}
	if !ok {
		return runedv1.HoldOutcome{Busy: true}, nil
	}

	// Detach from the HTTP request ctx, which dies the moment this response is
	// written, while keeping its values so the run stays on the caller's
	// trace. spawn() re-wraps with the RunTTL deadline.
	go m.spawn(context.WithoutCancel(ctx), req, claim.runID, claim.sessionID, claim.containerName)
	return runedv1.HoldOutcome{RunID: claim.runID}, nil
}

// claim identifies a won run slot: what the post-claim step needs to execute.
type claim struct {
	runID         string
	sessionID     string
	containerName string
}

// admit is the atomic claim-or-reject critical section, the ONE place a
// folder's run slot is taken, shared by Run and Hold. m.mu serializes
// check-and-claim (single-process runed). ok=false means the caller returns
// out verbatim.
func (m *Manager) admit(ctx context.Context, req runedv1.RunRequest, kind string) (c claim, out runedv1.RunOutcome, ok bool, err error) {
	folder := string(req.Folder)
	m.mu.Lock()

	active, err := m.db.LiveSpawnsInFolder(ctx, folder)
	if err != nil {
		m.mu.Unlock()
		return claim{}, runedv1.RunOutcome{}, false, fmt.Errorf("check active spawn: %w", err)
	}
	if slices.ContainsFunc(active, func(sp Spawn) bool {
		return kind == KindHold || sp.Kind == KindHold || sp.Topic == req.Topic
	}) {
		m.mu.Unlock()
		return claim{}, runedv1.RunOutcome{Busy: true}, false, nil
	}

	// The global cap is scoped to KindAgent (spec 5/8): MaxAgents bounds
	// container resources, which a containerless kind does not consume; its
	// serialization is the per-folder exclusivity above.
	if kind == KindAgent {
		activeCount, cerr := m.db.LiveAgentSpawnCount(ctx)
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
		containerName: fmt.Sprintf("arizuko-%s-%s-%d-%s",
			m.instance, container.SanitizeFolder(folder), time.Now().UnixMilli(), randHex(4)),
	}
	if c.sessionID == "" {
		c.sessionID = core.NewSessionID()
	}
	if err := m.db.CreateSpawn(ctx, Spawn{
		RunID: c.runID, Folder: folder, Topic: req.Topic, TurnID: req.TurnID, ContainerName: c.containerName,
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

	// Take the claim BEFORE anything is recorded or launched. started=false
	// means a DELETE killed this run between admit's CreateSpawn and here:
	// launching anyway would put a second container on one folder's mount
	// and open a session_log row nothing will close. Reported as Busy, so
	// routd leaves the cursor un-advanced.
	started, err := m.db.StartSpawn(ctx, runID, sessionID)
	if err != nil {
		slog.Error("runed: StartSpawn failed — launch aborted",
			"run_id", runID, "folder", folder, "kind", kind, "err", err)
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if endErr := m.db.EndSpawn(cleanupCtx, runID, "error", runedv1.OutcomeError, 0); endErr != nil {
			slog.Error("runed: EndSpawn failed — spawn may become zombie", "run_id", runID, "err", endErr)
		}
		return runedv1.RunOutcome{Busy: true}
	}
	if !started {
		slog.Warn("runed: launch aborted — run killed before start",
			"run_id", runID, "folder", folder, "kind", kind)
		return runedv1.RunOutcome{Busy: true}
	}

	// Isolated runs are one-off containers, and a non-agent kind has no agent
	// session: neither gets a session_log row.
	var logID string
	if !req.Isolated && kind == KindAgent {
		var lerr error
		if logID, lerr = m.db.OpenSession(ctx, folder, sessionID); lerr != nil {
			slog.Error("runed: OpenSession failed — the run has no history row",
				"run_id", runID, "folder", folder, "err", lerr)
		} else if lerr = m.db.LinkSessionLog(ctx, runID, logID); lerr != nil {
			slog.Error("runed: LinkSessionLog failed", "run_id", runID, "log_id", logID, "err", lerr)
		}
	}

	runCtx := ctx
	ttl := m.runTTL
	if ttl > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, ttl)
		defer cancel()
	}

	// The session id the renderer must resume is the claim's, not the
	// request's: admit mints one for a fresh turn.
	req.SessionID = sessionID
	res := m.execute(runCtx, exec, req, kind, runID, containerName, ttl)

	state := "exited"
	failed := res.Outcome == runedv1.OutcomeError
	if failed {
		state = "error"
	}
	cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancelCleanup()
	if err := m.db.EndSpawn(cleanupCtx, runID, state, res.Outcome, res.ExitCode); err != nil {
		// The row stays 'running' if this fails: a zombie that blocks the cap
		// until an operator resets it.
		slog.Error("runed: EndSpawn failed — spawn may become zombie", "run_id", runID, "err", err)
	}
	if logID != "" {
		if err := m.db.CloseSession(cleanupCtx, folder, logID, res.Outcome, res.Error, res.MessageCount); err != nil {
			slog.Error("runed: CloseSession failed", "log_id", logID, "err", err)
		}
	}

	return runedv1.RunOutcome{
		RunID: runID, Outcome: res.Outcome, Error: res.Error,
		Terminal: res.Terminal,
		TimedOut: errors.Is(runCtx.Err(), context.DeadlineExceeded),
	}
}

// execute renders this turn's envelope and hands it to the kind's executor.
// One renderer, two consumers: a substrate reads the Turn and interprets none
// of it (spec 6/41 §"The seam"). A kind that starts nothing — a hold — gets the
// handle and the folder, which is all its executor reads.
func (m *Manager) execute(
	ctx context.Context, exec Runtime, req runedv1.RunRequest,
	kind, runID, handle string, ttl time.Duration,
) RunResult {
	turn := Turn{Handle: handle, Folder: string(req.Folder)}
	if kind == KindAgent {
		if m.cfg == nil || m.folders == nil {
			// Loud and terminal: with no renderer there is no envelope, and a
			// guest started without one would run with no mounts and no prompt.
			slog.Error("runed: no turn renderer wired (ManagerConfig.Cfg/Folders) — the agent run is refused",
				"run_id", runID, "folder", req.Folder)
			return RunResult{
				Outcome: runedv1.OutcomeError, Terminal: true,
				Error: "runed: no turn renderer configured (ManagerConfig.Cfg + Folders)",
			}
		}
		// A gateway instance withholds the model credential from the guest
		// (spawnSecrets), so a turn arriving with no minted key would reach no
		// model at all and read as a harness fault. Refuse before anything is
		// started: the only way here is a routd that did not mint, which is a
		// version skew or a bug, never a state to run a turn in.
		if m.cfg.ModelGatewayURL != "" && req.ModelGatewayKey == "" {
			return RunResult{
				Outcome: runedv1.OutcomeError, Terminal: true,
				Error: "model gateway configured but this run carries no virtual key; routd mints one per turn (runed/README.md)",
			}
		}
		if ipcDir, err := turnIpcDir(m.folders, req); err == nil {
			defer endCodexStage(m.cfg, ipcDir)
		}
		var err error
		turn, err = renderTurn(m.cfg, m.folders, req, handle, ttl)
		if err != nil {
			return runFailure(ctx, fmt.Errorf("render turn: %w", err))
		}
	}
	return exec.Run(ctx, turn)
}

// Kill stops a run (DELETE /v1/runs/{id}) and frees its slot; the spawn's
// recorded kind selects which executor's Kill runs (spec 5/8). Idempotent:
// killing an already-exited run is a no-op 200. A deliberate kill records
// state=killed WITHOUT outcome=error.
func (m *Manager) Kill(ctx context.Context, runID string) error {
	sp, err := m.db.SpawnByRunID(ctx, runID)
	if err != nil {
		return err
	}
	live := sp.State == "running" || sp.State == "queued"
	if live {
		if err := m.executor(sp.Kind).Kill(sp.ContainerName); err != nil {
			return err
		}
		// KillSpawn re-checks liveness on the write — a run that completed
		// normally between the read above and here keeps its terminal state.
		if kerr := m.db.KillSpawn(ctx, runID); kerr != nil {
			return kerr
		}
	}
	return nil
}

// StopFolder is the operator-kill path (routd's /stop): it Kills every live
// spawn of the folder, or with a turnID only the spawn that turn started.
// runID names the newest run it killed; killed=false with "" means none was
// live.
func (m *Manager) StopFolder(ctx context.Context, folder, turnID string) (runID string, killed bool, err error) {
	active, err := m.db.LiveSpawnsInFolder(ctx, folder)
	if err != nil {
		return "", false, err
	}
	for _, sp := range active {
		if turnID != "" && sp.TurnID != turnID {
			continue
		}
		if err := m.Kill(ctx, sp.RunID); err != nil {
			return runID, killed, err
		}
		if !killed {
			runID, killed = sp.RunID, true
		}
	}
	return runID, killed, nil
}

// ActiveRunID returns the run_id of a folder's newest live spawn, or "" when
// none.
func (m *Manager) ActiveRunID(ctx context.Context, folder string) (string, error) {
	active, err := m.db.LiveSpawnsInFolder(ctx, folder)
	if err != nil || len(active) == 0 {
		return "", err
	}
	return active[0].RunID, nil
}

// ActiveCount returns the number of live spawns (test/observability).
func (m *Manager) ActiveCount(ctx context.Context) int {
	n, _ := m.db.LiveSpawnCount(ctx)
	return n
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

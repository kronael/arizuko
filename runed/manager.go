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
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/types"
)

const (
	circuitBreakerThreshold = 3
	defaultRunTTL           = 20 * time.Minute
	defaultMaxConcurrent    = 5
)

// Manager owns the execution plane's run lifecycle: per-folder serialization
// (one live spawn per folder), the global concurrency cap, the
// steer-into-running path, the circuit breaker and the Runtime envelope
// (spec 5/P-runed). Admission is a pure claim-or-reject decision off the DB; runed
// keeps NO internal admission queue, and a caller that cannot be admitted
// gets a retryable busy outcome and routd re-feeds it. The only in-memory
// state is steerFns, the live steer callbacks.
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
	// steerFns is run_id -> steer callback. Keyed by RUN, not by folder:
	// keyed by folder, run A's exit disarmed run B for its whole life (BUGS
	// L111). The DB's active-spawn row is the only liveness authority, so an
	// entry for a dead run is unreachable rather than merely deleted on time.
	steerFns map[string]func(batch string) bool
}

// ManagerConfig wires the Manager. MaxConcurrent caps total live spawns
// (MAX_CONCURRENT_CONTAINERS).
type ManagerConfig struct {
	RunTTL        time.Duration
	Instance      string
	MaxConcurrent int
	// Cfg + Folders wire the turn renderer. An agent run refuses without them:
	// there is no envelope to hand a substrate.
	Cfg     *core.Config
	Folders *groupfolder.Resolver
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
		runTTL:    cfg.RunTTL,
		instance:  cfg.Instance,
		maxRun:    cfg.MaxConcurrent,
		cfg:       cfg.Cfg,
		folders:   cfg.Folders,
		steerFns:  map[string]func(batch string) bool{},
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

// Run executes (or steers) one agent turn as a pure claim-or-reject executor;
// routd owns all queueing (spec 5/P-runed). Three exits: admitted (claim the slot,
// spawn, block until the run completes), steered (folder busy with a live
// container: write the batch in and ack), or busy (folder busy with a dead
// container, or the global cap is hit).
func (m *Manager) Run(ctx context.Context, req runedv1.RunRequest) (runedv1.RunOutcome, error) {
	kind := kindOf(req.Kind)
	if err := m.requireExecutor(kind); err != nil {
		return runedv1.RunOutcome{}, err
	}
	folder := string(req.Folder)

	// A new inbound resets the breaker (spec 5/P-runed). Scoped to KindAgent: a
	// hold is not an agent inbound. Done before admission so a retry after 3
	// failures actually spawns.
	if kind == KindAgent {
		failures, err := m.db.FailureStreak(ctx, folder)
		if err != nil {
			return runedv1.RunOutcome{}, fmt.Errorf("read the failure streak: %w", err)
		}
		if failures >= circuitBreakerThreshold {
			if err := m.db.ClearFailures(ctx, folder); err != nil {
				return runedv1.RunOutcome{}, fmt.Errorf("reset the breaker: %w", err)
			}
		}
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

	// Folder busy? Steer into the live container, else reject as busy.
	active, err := m.db.LiveSpawnInFolder(ctx, folder)
	if err != nil {
		m.mu.Unlock()
		return claim{}, runedv1.RunOutcome{}, false, fmt.Errorf("check active spawn: %w", err)
	}
	if active != nil {
		// A non-agent request carries no message and must never be steered
		// into a live agent's container (spec 5/8).
		var steered bool
		if kind == KindAgent {
			steer := m.steerFns[active.RunID]
			steered = steer != nil && steer(req.MessageBatch)
		}
		runID := active.RunID
		m.mu.Unlock()
		if steered {
			if merr := m.db.MarkSteered(ctx, runID); merr != nil {
				slog.Error("runed: could not record the steer", "run_id", runID, "err", merr)
			}
			return claim{}, runedv1.RunOutcome{
				RunID: runID, Outcome: runedv1.OutcomeOK, Steered: true,
			}, false, nil
		}
		// Steer failed (container exited), not attempted, or not yet wired:
		// busy, and routd re-feeds.
		return claim{}, runedv1.RunOutcome{Busy: true}, false, nil
	}

	// The global cap is scoped to KindAgent (spec 5/8): MaxConcurrent bounds
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
		containerName: fmt.Sprintf("arizuko-%s-%s-%d",
			m.instance, container.SanitizeFolder(folder), time.Now().UnixMilli()),
	}
	if c.sessionID == "" {
		c.sessionID = newUUID()
	}
	if err := m.db.CreateSpawn(ctx, Spawn{
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

	// Take the claim BEFORE anything is recorded or launched. started=false
	// means a DELETE killed this run between admit's CreateSpawn and here:
	// launching anyway would put a second container on one folder's mount,
	// open a session_log row nothing will close, and run endRun against the
	// folder's NEXT run. Reported as Busy, so routd leaves the cursor
	// un-advanced without counting a deliberate kill toward the breaker.
	started, err := m.db.StartSpawn(ctx, runID, sessionID)
	if err != nil {
		// The row stays 'queued' and holds the folder until runed restarts
		// (ExpireOrphans); aborting is still right, since we cannot launch on
		// a claim we could not take.
		slog.Error("runed: StartSpawn failed — launch aborted, spawn may stay queued",
			"run_id", runID, "folder", folder, "kind", kind, "err", err)
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

	// The turn's ONE deadline is armed HERE, at the shared dispatch site, not
	// inside any one executor (spec 5/8): every kind gets it by honoring ctx
	// cancellation, and a substrate with a timer of its own reads
	// ctx.Deadline(). dockerRuntime's armCancel kills on ctx.Done() whatever
	// the cause.
	runCtx := ctx
	ttl := m.turnTTL(req)
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
	if err := m.db.EndSpawn(ctx, runID, state, res.Outcome, res.ExitCode); err != nil {
		// The row stays 'running' if this fails: a zombie that blocks the cap
		// until an operator resets it.
		slog.Error("runed: EndSpawn failed — spawn may become zombie", "run_id", runID, "err", err)
	}
	if logID != "" {
		if err := m.db.CloseSession(ctx, folder, logID, res.Outcome, res.Error, res.MessageCount); err != nil {
			slog.Error("runed: CloseSession failed", "log_id", logID, "err", err)
		}
	}
	breakerTripped := m.endRun(ctx, folder, runID, failed, kind)

	out := runedv1.RunOutcome{
		RunID: runID, Outcome: res.Outcome, Error: res.Error,
		Terminal: res.Terminal,
	}
	// The run that pushes the folder to the threshold reports breaker_open on
	// the response the caller awaits (spec 5/P-runed § circuit breaker).
	if breakerTripped {
		out.BreakerOpen = true
		if out.Error == "" {
			out.Error = "circuit breaker open"
		}
	}
	return out
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
				Error: "model gateway configured but this run carries no virtual key; routd mints one per turn (spec 6/23)",
			}
		}
		turn = renderTurn(m.cfg, m.folders, req, handle, ttl)
	}
	// RegisterSteer wires the steer callback once the guest + IPC are up; a
	// guestless executor never calls it. It is the Manager's hook, not a
	// rendered fact, so it is attached here rather than by the renderer.
	turn.RegisterSteer = func(steer func(batch string) bool) { m.SetSteer(runID, steer) }
	return exec.Run(ctx, turn)
}

// turnTTL is the one deadline: the run ceiling (RUNED_RUN_TIMEOUT), lowered by
// the guest ceiling the group's container_config Timeout sets — an input to this
// deadline, never a substrate timer of its own (spec 6/41 §"The seam"). The
// guest ceiling is floored at the idle timeout plus a grace period, so it can
// never cut in before the idle timer has given up.
func (m *Manager) turnTTL(req runedv1.RunRequest) time.Duration {
	if m.cfg == nil {
		return m.runTTL
	}
	guest := m.cfg.Timeout
	if gc := groupConfig(req.ContainerConfig); gc.Timeout > 0 {
		guest = gc.Timeout
	}
	if grace := m.cfg.IdleTimeout + containerGracePeriod; guest < grace {
		guest = grace
	}
	if m.runTTL <= 0 || guest < m.runTTL {
		return guest
	}
	return m.runTTL
}

// endRun clears the steer callback and updates the breaker counter. Returns
// true when this exit trips the breaker. Breaker accounting is scoped to
// KindAgent (spec 5/8): a failed hold must never stop the agent spawning.
func (m *Manager) endRun(ctx context.Context, folder, runID string, failed bool, kind string) bool {
	var tripped bool
	if kind == KindAgent {
		if failed {
			newCount, err := m.db.RecordFailure(ctx, folder)
			if err != nil {
				// The streak is what opens the breaker, so a count that did not
				// land is a wedge nobody sees: the folder keeps failing and the
				// caller is never told to stop.
				slog.Error("runed: could not count a failure against the breaker",
					"folder", folder, "run_id", runID, "err", err)
			}
			tripped = newCount == circuitBreakerThreshold
		} else {
			// Any clean exit resets the breaker (silent included); a folder
			// alternating error/silent must never creep to the threshold.
			if err := m.db.ClearFailures(ctx, folder); err != nil {
				slog.Error("runed: could not clear the breaker", "folder", folder, "err", err)
			}
		}
	}

	// Clear this run's steer callback (its container is gone). Deleting by
	// run_id cannot disarm a successor that already claimed the folder.
	m.mu.Lock()
	delete(m.steerFns, runID)
	m.mu.Unlock()
	return tripped
}

// SetSteer wires a run's steer callback once the container is up. No liveness
// check: the run_id IS the key, so a callback for a run that has already
// ended is an entry nothing can reach, since admit only looks up the run_id
// the DB reports active.
func (m *Manager) SetSteer(runID string, steer func(batch string) bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.steerFns[runID] = steer
}

// Kill stops a run (DELETE /v1/runs/{id}) and frees its slot; the spawn's
// recorded kind selects which executor's Kill runs (spec 5/8). Idempotent:
// killing an already-exited run is a no-op 200. A deliberate kill records
// state=killed WITHOUT outcome=error and does NOT count toward the breaker.
func (m *Manager) Kill(ctx context.Context, runID string) error {
	sp, err := m.db.SpawnByRunID(ctx, runID)
	if err != nil {
		return err
	}
	live := sp.State == "running" || sp.State == "queued"
	if live {
		_ = m.executor(sp.Kind).Kill(sp.ContainerName)
		// KillSpawn re-checks liveness on the write — a run that completed
		// normally between the read above and here keeps its terminal state.
		if kerr := m.db.KillSpawn(ctx, runID); kerr != nil {
			return kerr
		}
	}
	// Killing an already-terminal run_id disarms only that run, never
	// whichever run has since claimed the folder.
	m.mu.Lock()
	delete(m.steerFns, runID)
	m.mu.Unlock()
	return nil
}

// StopFolder is the operator-kill path (routd's /stop): it resolves the
// folder's live spawn and Kills it. killed=false with "" means the folder has
// no live spawn.
func (m *Manager) StopFolder(ctx context.Context, folder string) (runID string, killed bool, err error) {
	runID, err = m.ActiveRunID(ctx, folder)
	if err != nil || runID == "" {
		return "", false, err
	}
	if err := m.Kill(ctx, runID); err != nil {
		return "", false, err
	}
	return runID, true, nil
}

// ActiveRunID returns the run_id of a folder's live spawn, or "" when none.
func (m *Manager) ActiveRunID(ctx context.Context, folder string) (string, error) {
	active, err := m.db.LiveSpawnInFolder(ctx, folder)
	if err != nil || active == nil {
		return "", err
	}
	return active.RunID, nil
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

// newUUID mints a RFC4122 v4 UUID (the harness session id; opaque to routd).
func newUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

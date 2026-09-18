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

// Manager owns the execution plane's run lifecycle: per-folder serialization (one
// live spawn per folder), the global concurrency cap, the steer-into-running path,
// the circuit breaker, token brokering, and the Runtime envelope. It is the body
// behind POST /v1/runs (spec 5/P).
//
// Admission is a pure claim-or-reject decision off the DB — runed keeps NO
// internal admission queue, which duplicated routd's. A caller that cannot be
// admitted gets a retryable busy outcome and routd re-feeds it.
//
// State is DB-backed for restart recovery (active spawns, failure counts, live
// count). The only in-memory state is steerFns, the live steer callbacks, which
// are container-lifetime and inherently non-persistable.
type Manager struct {
	db *DB
	// executors dispatches the post-claim step by spawns.kind (spec 5/8
	// "Filesystem restore claims the folder's run slot"): KindAgent is wired
	// by NewManager from the production Runtime; other kinds (KindHold, and
	// any future folder-exclusive job) register via RegisterExecutor.
	// Executor IS the existing Runtime interface — no new interface for a
	// new kind.
	executors map[string]Runtime
	runTTL    time.Duration
	instance  string
	maxRun    int

	mu sync.Mutex
	// steerFns is run_id -> steer callback (runtime-wired). Keyed by RUN, not
	// by folder, because that is whose lifetime it has. Keyed by folder it was
	// a second, weaker record of which run is live, and it disagreed with the
	// spawns table: endRun and Kill both deleted a folder's entry without
	// asking which run owned it, so run A's exit disarmed run B for its whole
	// life (BUGS L111). With the run as the key the DB's active-spawn row is
	// the only liveness authority — admit looks the callback up by the run_id
	// it just read from the DB, so an entry for a dead run is unreachable
	// rather than merely deleted on time.
	steerFns map[string]func(batch string) bool
}

// ManagerConfig wires the Manager. MaxConcurrent caps total live spawns
// (MAX_CONCURRENT_CONTAINERS).
type ManagerConfig struct {
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

// Run executes (or steers) one agent turn as a pure claim-or-reject executor —
// routd owns all queueing (spec 5/P). Three exits: admitted (claim the slot,
// spawn, block until the run completes), steered (folder busy with a live
// container: write the batch in and return an ack), or busy (folder busy with a
// dead container, or the global cap is hit). runed does NOT queue.
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

// Hold claims a folder's run slot for an EXTERNAL caller and returns the handle
// immediately, leaving the slot claimed until the caller releases it or RunTTL
// expires it. It is a pause on the folder expressed as a run, not a second locking
// mechanism: the same admit() step an agent turn goes through, so a hold inherits
// per-folder exclusion, the busy→requeue backpressure, the wedge protection and
// spawns visibility. Busy=true means nothing was claimed.
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

// admit is the atomic claim-or-reject critical section — the ONE place a folder's
// run slot is taken, shared by Run and Hold. m.mu serializes check-and-claim so
// concurrent callers can never both pass the cap or both claim one folder
// (single-process runed, so the mutex IS the BEGIN IMMEDIATE claim). ok=false
// means the caller returns out verbatim.
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
		// A non-agent request (e.g. a hold) carries no message and must
		// never be steered into a live agent's running container — steering
		// an empty batch into whatever turn is live is a real bug, not a
		// hypothetical (spec 5/8). Only KindAgent attempts the steer.
		var steered bool
		if kind == KindAgent {
			steer := m.steerFns[active.RunID]
			steered = steer != nil && steer(req.MessageBatch)
		}
		runID, sessionID := active.RunID, active.SessionID
		m.mu.Unlock()
		if steered {
			if merr := m.db.MarkSteered(ctx, runID); merr != nil {
				slog.Error("runed: could not record the steer", "run_id", runID, "err", merr)
			}
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

	// Take the claim BEFORE anything is recorded or launched. started=false means a
	// DELETE killed this run between admit's CreateSpawn and here, so the row is
	// already terminal and the folder reads FREE — launching anyway would put a
	// second container on one folder's mount. Reported as Busy, so routd leaves the
	// cursor un-advanced without counting a deliberate kill toward the breaker.
	// Returning here also keeps an aborted launch from opening a session_log row
	// nothing will close, and from running endRun — which would clear a real failure
	// streak and delete a steer callback that belongs to the folder's NEXT run.
	started, err := m.db.StartSpawn(ctx, runID, sessionID)
	if err != nil {
		// Spawn row stays 'queued' → the folder is held until runed restarts
		// (ExpireOrphans), the same operator-visible zombie EndSpawn failure
		// leaves. Aborting is still right: we cannot launch on a claim we could
		// not take.
		slog.Error("runed: StartSpawn failed — launch aborted, spawn may stay queued",
			"run_id", runID, "folder", folder, "kind", kind, "err", err)
		return runedv1.RunOutcome{Busy: true}
	}
	if !started {
		slog.Warn("runed: launch aborted — run killed before start",
			"run_id", runID, "folder", folder, "kind", kind)
		return runedv1.RunOutcome{Busy: true}
	}

	// Isolated (timed-isolated:*) runs are one-off containers, and a
	// non-agent kind has no agent session at all: neither gets a
	// session_log row. The spawns row still exists for GET/DELETE.
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
		GHWrite:         req.GHWrite,
		EgressAllowlist: req.EgressAllowlist,
		Secrets:         req.Secrets,
		ModelGatewayKey: req.ModelGatewayKey,
		RunTTL:          m.runTTL,
		RegisterSteer:   func(steer func(batch string) bool) { m.SetSteer(runID, steer) },
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
	if err := m.db.EndSpawn(ctx, runID, state, res.Outcome, res.ExitCode); err != nil {
		// spawn row stays 'running' if this fails → zombie that blocks the cap.
		// Operator must reset it manually: UPDATE spawns SET state='exited' WHERE run_id=?
		slog.Error("runed: EndSpawn failed — spawn may become zombie", "run_id", runID, "err", err)
	}
	if logID != "" {
		if err := m.db.CloseSession(ctx, folder, logID, res.NewSessionID,
			res.Outcome, res.Error, res.MessageCount); err != nil {
			slog.Error("runed: CloseSession failed", "log_id", logID, "err", err)
		}
	}
	breakerTripped := m.endRun(ctx, folder, runID, failed, kind)

	out := runedv1.RunOutcome{
		RunID: runID, Outcome: res.Outcome, SessionID: endSession, Error: res.Error,
		Terminal: res.Terminal,
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

// endRun clears the steer callback and updates the breaker counter in the DB.
// Returns true when this exit trips the breaker. The slot frees implicitly and
// routd re-feeds next poll.
//
// Breaker accounting is scoped to KindAgent (spec 5/8): a failed hold run must
// never count against the agent's breaker and stop it spawning. EndSpawn still
// records the hold's own outcome unconditionally.
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

// SetSteer wires a run's steer callback (the IPC write + SIGUSR1). The
// production Runtime calls this once the container is up so a concurrent
// POST /v1/runs can steer into it.
//
// No liveness check: the run_id IS the key, so wiring a callback for a run that
// has already ended stores an entry nothing can reach — admit only ever looks
// up the run_id the DB reports active. The check this replaced read the DB on
// every wire to answer a question the key now answers.
func (m *Manager) SetSteer(runID string, steer func(batch string) bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.steerFns[runID] = steer
}

// Kill stops a run (DELETE /v1/runs/{id}) and frees its slot. The spawn's
// recorded kind selects which executor's Kill runs (spec 5/8) — a backup
// run's Kill is dispatched the same way an agent's is, no special-casing.
// Idempotent: killing an already-exited run is a no-op 200. A deliberate
// kill records state=killed WITHOUT outcome=error and does NOT count toward
// the breaker (it's operator intent, not a run failure). The slot frees
// implicitly once the row is terminal; routd re-feeds any pending batch.
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
	// Clear this run's steer callback (its container is gone). Killing an
	// already-terminal run_id now disarms only that run, never whichever run
	// has since claimed the folder.
	m.mu.Lock()
	delete(m.steerFns, runID)
	m.mu.Unlock()
	return nil
}

// StopFolder is the operator-kill path (routd's /stop): it resolves the
// folder's live spawn and Kills it. Returns the killed run_id (or "" + killed
// false when the folder has no live spawn — routd renders the no-active text).
// Kill records state=killed without counting toward the breaker.
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

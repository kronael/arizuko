package runed

import (
	"context"
	"sync"

	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

// Run kinds — the spawns.kind values that select Manager.Run's post-claim
// executor (spec 5/8 "Filesystem restore claims the folder's run slot").
const (
	// KindAgent is a normal turn: spawn the agent container. The column
	// default, so every pre-kind caller and row means this.
	KindAgent = "agent"
	// KindHold claims a folder's run slot on behalf of an EXTERNAL process
	// (POST /v1/holds) that needs the folder to itself — a filesystem
	// restore, a vacuum, a migration. runed itself does no work for it.
	KindHold = "hold"
)

// holdRuntime is KindHold's executor. It spawns nothing and does nothing:
// the claimed run slot IS the hold, so its only job is to keep the spawn row
// live until the holder releases it (DELETE /v1/runs/{run_id} → Manager.Kill
// → this Kill) or Manager.spawn's RunTTL deadline expires it.
//
// Everything a hold needs it inherits from the shared claim path rather than
// reimplementing: per-folder exclusion (one live spawn per folder, any kind),
// backpressure (an agent turn arriving mid-hold gets Busy and routd re-feeds
// from its own queue — a hold registers NO steer callback, so nothing is ever
// written into it), wedge protection (the RunTTL deadline on ctx), and
// visibility (it is a spawns row like any other).
type holdRuntime struct {
	mu sync.Mutex
	// live maps a run's executor handle (RunSpec.ContainerName — Manager
	// mints one per run regardless of kind; for a hold it names no container)
	// to that hold's release signal.
	live map[string]*heldSlot
}

// heldSlot is one hold's release signal. once makes closing idempotent:
// concurrent releases, and a release racing the RunTTL expiry, are both
// ordinary.
type heldSlot struct {
	release chan struct{}
	once    sync.Once
}

func (s *heldSlot) close() { s.once.Do(func() { close(s.release) }) }

// NewHoldRuntime builds the KindHold executor. Register it with
// Manager.RegisterExecutor(KindHold, ...).
func NewHoldRuntime() Runtime {
	return &holdRuntime{live: map[string]*heldSlot{}}
}

// slot returns the handle's release signal, creating it if this is the first
// of Run/Kill to arrive.
//
// Kill legitimately lands BEFORE Run: Manager.Hold hands the caller a handle
// the instant the slot is claimed, while the run's own goroutine is still
// starting, so a caller that releases immediately would otherwise close a
// channel nobody had created yet and wedge the folder until RunTTL. Creating
// the signal on first touch makes an early release a tombstone Run sees
// straight away. Found by TestHoldReleaseUnblocks, which was order-dependent
// green before this.
func (h *holdRuntime) slot(handle string) *heldSlot {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.live[handle]
	if !ok {
		s = &heldSlot{release: make(chan struct{})}
		h.live[handle] = s
	}
	return s
}

// Run blocks until the hold is released or ctx dies. ctx carries
// Manager.spawn's RunTTL deadline, so a holder that vanishes mid-job expires
// as outcome=error — loud in spawns, and the folder frees itself instead of
// staying wedged forever.
func (h *holdRuntime) Run(ctx context.Context, spec RunSpec) RunResult {
	slot := h.slot(spec.ContainerName)
	defer func() {
		h.mu.Lock()
		delete(h.live, spec.ContainerName)
		h.mu.Unlock()
	}()

	select {
	case <-slot.release:
		return RunResult{Outcome: runedv1.OutcomeOK}
	case <-ctx.Done():
		return RunResult{
			Outcome: runedv1.OutcomeError,
			Error:   "hold expired before release: " + ctx.Err().Error(),
		}
	}
}

// Kill releases the hold: Run returns and Manager.spawn ends the spawn row,
// freeing the slot. Idempotent — releasing an already-released or
// already-expired hold is a no-op 200, matching dockerRuntime.Kill.
func (h *holdRuntime) Kill(handle string) error {
	h.slot(handle).close()
	return nil
}

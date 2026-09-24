package runed

import (
	"context"
	"sync"

	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

// Run kinds: the spawns.kind values that select Manager.Run's post-claim
// executor (spec 5/8).
const (
	// KindAgent is a normal turn: spawn the agent container. The default.
	KindAgent = "agent"
	// KindHold claims a folder's run slot on behalf of an EXTERNAL process
	// (POST /v1/holds); runed itself does no work for it.
	KindHold = "hold"
)

// holdRuntime is KindHold's executor. It spawns nothing: the claimed run slot
// IS the hold, so its only job is to keep the spawn row live until the holder
// releases it (DELETE /v1/runs/{run_id}) or RunTTL expires it. Everything
// else — per-folder exclusion, backpressure, wedge protection, visibility —
// it inherits from the shared claim path.
type holdRuntime struct {
	mu sync.Mutex
	// live maps a run's executor handle (Turn.Handle, which names no guest for
	// a hold) to that hold's release signal.
	live map[string]*heldSlot
}

// heldSlot is one hold's release signal; once makes closing idempotent.
type heldSlot struct {
	release chan struct{}
	once    sync.Once
}

func (s *heldSlot) close() { s.once.Do(func() { close(s.release) }) }

// NewHoldRuntime builds the KindHold executor.
func NewHoldRuntime() Runtime {
	return &holdRuntime{live: map[string]*heldSlot{}}
}

// slot returns the handle's release signal, creating it if this is the first
// of Run/Kill to arrive. Kill legitimately lands BEFORE Run: Manager.Hold
// hands the caller a handle the instant the slot is claimed, so an immediate
// release would otherwise close a channel nobody had created and wedge the
// folder until RunTTL (TestHoldReleaseUnblocks).
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

// Run blocks until the hold is released or ctx dies. ctx carries the turn
// deadline, so a holder that vanishes mid-job expires as outcome=error and
// the folder frees itself.
func (h *holdRuntime) Run(ctx context.Context, t Turn) RunResult {
	slot := h.slot(t.Handle)
	defer func() {
		h.mu.Lock()
		delete(h.live, t.Handle)
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

// Kill releases the hold: Run returns and Manager.spawn ends the spawn row.
// Idempotent, matching dockerRuntime.Kill.
func (h *holdRuntime) Kill(handle string) error {
	h.slot(handle).close()
	return nil
}

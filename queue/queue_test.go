package queue

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewQueue(t *testing.T) {
	q := New(3, t.TempDir())
	if q.maxConcurrent != 3 {
		t.Fatalf("expected maxConcurrent 3, got %d", q.maxConcurrent)
	}
	if q.ActiveCount() != 0 {
		t.Fatal("expected 0 active")
	}
}

func TestEnqueueMessageCheckStartsContainer(t *testing.T) {
	q := New(5, t.TempDir())

	var called atomic.Bool
	q.SetProcessMessagesFn(func(jid string) (bool, error) {
		called.Store(true)
		return true, nil
	})

	q.EnqueueMessageCheck("g1")
	time.Sleep(50 * time.Millisecond)
	if !called.Load() {
		t.Fatal("processMessages not called")
	}
}

func TestEnqueueMessageCheckQueuesWhenActive(t *testing.T) {
	q := New(5, t.TempDir())

	var calls atomic.Int32
	started := make(chan struct{}, 1)
	block := make(chan struct{})
	q.SetProcessMessagesFn(func(jid string) (bool, error) {
		n := calls.Add(1)
		if n == 1 {
			started <- struct{}{}
			<-block // only first call blocks
		}
		return true, nil
	})
	// DB says there are pending messages — drain should re-process
	q.SetHasPendingFn(func(jid string) bool { return true })

	q.EnqueueMessageCheck("g1")
	<-started

	// Second enqueue while active — no flag, but hasPending will trigger drain
	q.EnqueueMessageCheck("g1")

	close(block)
	time.Sleep(100 * time.Millisecond)

	if calls.Load() < 2 {
		t.Fatalf("expected >= 2 calls (initial + drain), got %d", calls.Load())
	}
}

func TestConcurrencyLimit(t *testing.T) {
	q := New(1, t.TempDir())

	started := make(chan struct{})
	block := make(chan struct{})
	q.SetProcessMessagesFn(func(jid string) (bool, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-block
		return true, nil
	})

	q.EnqueueMessageCheck("g1")
	<-started

	// g2 should be queued, not started
	q.EnqueueMessageCheck("g2")
	time.Sleep(50 * time.Millisecond)

	if q.ActiveCount() != 1 {
		t.Fatalf("expected 1 active, got %d", q.ActiveCount())
	}

	q.mu.Lock()
	waiting := len(q.waitingGroups)
	q.mu.Unlock()
	if waiting != 1 {
		t.Fatalf("expected 1 waiting, got %d", waiting)
	}

	close(block)
	time.Sleep(100 * time.Millisecond)
}

func TestCircuitBreaker(t *testing.T) {
	q := New(5, t.TempDir())

	var calls atomic.Int32
	q.SetProcessMessagesFn(func(jid string) (bool, error) {
		calls.Add(1)
		return false, fmt.Errorf("boom") // a real processing error
	})

	// 3 errors to trip breaker
	for range 3 {
		q.EnqueueMessageCheck("g1")
		time.Sleep(50 * time.Millisecond)
	}

	q.mu.Lock()
	failures := q.groups["g1"].consecutiveFailures
	q.mu.Unlock()
	if failures < circuitBreakerThreshold {
		t.Fatalf("expected >= %d failures, got %d", circuitBreakerThreshold, failures)
	}

	// New message resets breaker
	q.EnqueueMessageCheck("g1")
	time.Sleep(50 * time.Millisecond)

	q.mu.Lock()
	failures = q.groups["g1"].consecutiveFailures
	q.mu.Unlock()
	// After reset + another failure, should be 1
	if failures >= circuitBreakerThreshold {
		t.Fatal("circuit breaker should have been reset")
	}
}

// A clean return with no output (silent turn / observe / already-current) is
// NOT a failure — it must never trip the breaker. Counting no-output as failure
// false-tripped discord channels after silent turns.
func TestCircuitBreaker_SilentNoOpDoesNotTrip(t *testing.T) {
	q := New(5, t.TempDir())
	q.SetProcessMessagesFn(func(jid string) (bool, error) {
		return false, nil // success with no output (silent no-op)
	})

	for range circuitBreakerThreshold + 2 {
		q.EnqueueMessageCheck("g1")
		time.Sleep(40 * time.Millisecond)
	}

	q.mu.Lock()
	failures := q.groups["g1"].consecutiveFailures
	q.mu.Unlock()
	if failures != 0 {
		t.Fatalf("silent no-op tripped breaker: %d failures, want 0", failures)
	}
}

func TestShutdownBlocksEnqueue(t *testing.T) {
	q := New(5, t.TempDir())
	q.SetProcessMessagesFn(func(jid string) (bool, error) {
		return true, nil
	})

	q.Shutdown()

	var called atomic.Bool
	q.SetProcessMessagesFn(func(jid string) (bool, error) {
		called.Store(true)
		return true, nil
	})

	q.EnqueueMessageCheck("g1")
	time.Sleep(50 * time.Millisecond)
	if called.Load() {
		t.Fatal("should not process after shutdown")
	}
}

// SendMessages drives mid-loop steering: writes one IPC file per
// message and signals the container. Must be a no-op when no container
// is active for the group.

// Regression: when ant's runner exits ("Input empty, exiting") between
// gated's queue check and the SIGUSR1, kill fails. Previously the error
// was swallowed (`_ = exec.Command(...).Run()`) and SendMessages returned
// true — the chat cursor advanced as if the steer succeeded, but the IPC
// file was orphaned and no future poll would pick the message back up
// until the user sent something else. Symptom on sloth 2026-05-10 20:23:
// user message disappeared, no reply, no error logged. The fix marks the
// slot inactive and returns false so the caller falls through to
// EnqueueMessageCheck, spawning a fresh container that drains the orphan.
// Two JIDs that map to the same folder must serialize: only one container
// runs at a time, the other waits and starts after the first finishes.
// Regression: at startup, recoverPendingMessages and checkMigrationVersion
// would enqueue different JIDs (telegram:..., atlas) for the same
// folder, spawning two parallel containers that double-narrated logs.
func TestEnqueueSerializesByFolder(t *testing.T) {
	q := New(5, t.TempDir())
	q.SetFolderForJidFn(func(jid string) string {
		switch jid {
		case "telegram:atlas", "atlas":
			return "atlas"
		}
		return ""
	})

	// hasPending=false so each finishing run does NOT self-restart;
	// drainWaitingLocked is what we want to exercise here.
	q.SetHasPendingFn(func(string) bool { return false })

	var concurrent atomic.Int32
	var maxSeen atomic.Int32
	seenJids := map[string]bool{}
	var smu sync.Mutex
	gate := make(chan struct{})
	q.SetProcessMessagesFn(func(jid string) (bool, error) {
		n := concurrent.Add(1)
		if n > maxSeen.Load() {
			maxSeen.Store(n)
		}
		smu.Lock()
		seenJids[jid] = true
		smu.Unlock()
		<-gate
		concurrent.Add(-1)
		return true, nil
	})

	q.EnqueueMessageCheck("telegram:atlas")
	q.EnqueueMessageCheck("atlas")

	time.Sleep(80 * time.Millisecond)
	if got := concurrent.Load(); got != 1 {
		t.Fatalf("expected 1 concurrent run for shared folder, got %d", got)
	}
	q.mu.Lock()
	waiting := len(q.waitingGroups)
	q.mu.Unlock()
	if waiting != 1 {
		t.Fatalf("expected 1 jid waiting on folder, got %d", waiting)
	}

	close(gate)
	time.Sleep(150 * time.Millisecond)

	if maxSeen.Load() != 1 {
		t.Fatalf("expected max concurrency 1, got %d", maxSeen.Load())
	}
	smu.Lock()
	defer smu.Unlock()
	if !seenJids["telegram:atlas"] || !seenJids["atlas"] {
		t.Fatalf("both JIDs must run; saw %v", seenJids)
	}
}

// Regression: SendMessages' signal-fail teardown must not double-decrement
// activeCount (or free a slot it no longer owns) when runForGroup finished
// the same activation concurrently. We simulate that interleaving inside the
// signalContainer hook (which runs with q.mu released): it performs exactly
// what runForGroup's cleanup does — clear the slot, decrement, free the
// folder, then start a fresh activation for the same folder. After the hook
// returns an error, SendMessages re-locks; the fixed code sees s.active is
// already false and leaves the new activation's bookkeeping untouched.
// Regression: when SendMessages' signal-fail teardown frees the only slot,
// any group parked in waitingGroups (here g2, queued because the concurrency
// limit was hit) must be drained — exactly as runForGroup's cleanup does.
// Without the drain, g2 starves: the steered g1 re-enqueues and retakes the
// slot, leaving g2 stuck until an unrelated event happens to drain it.

package queue

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewQueue(t *testing.T) {
	q := New(3)
	if q.maxConcurrent != 3 {
		t.Fatalf("expected maxConcurrent 3, got %d", q.maxConcurrent)
	}
	if q.ActiveCount() != 0 {
		t.Fatal("expected 0 active")
	}
}

func TestEnqueueMessageCheckStartsContainer(t *testing.T) {
	q := New(5)

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

// A second enqueue for a group that is already running is COLLAPSED: the
// running turn drains whatever arrived, and routd's poll re-feeds anything
// left. The queue never self-restarts a group it just finished.
func TestEnqueueMessageCheckCollapsesWhileActive(t *testing.T) {
	q := New(5)

	var calls atomic.Int32
	started := make(chan struct{}, 1)
	block := make(chan struct{})
	q.SetProcessMessagesFn(func(jid string) (bool, error) {
		if calls.Add(1) == 1 {
			started <- struct{}{}
			<-block // only first call blocks
		}
		return true, nil
	})

	q.EnqueueMessageCheck("g1")
	<-started

	q.EnqueueMessageCheck("g1")
	if n := calls.Load(); n != 1 {
		t.Fatalf("enqueue while active started %d runs, want 1", n)
	}

	close(block)
	time.Sleep(100 * time.Millisecond)

	if n := calls.Load(); n != 1 {
		t.Fatalf("group ran %d times, want 1: the finished run must not restart itself", n)
	}
	if q.ActiveCount() != 0 {
		t.Fatalf("activeCount = %d after the run, want 0", q.ActiveCount())
	}
}

func TestConcurrencyLimit(t *testing.T) {
	q := New(1)

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
	q := New(5)

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
	q := New(5)
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
	q := New(5)
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

// Two JIDs that map to the same folder must serialize: only one container
// runs at a time, the other waits and starts after the first finishes.
// Regression: at startup, recoverPendingMessages and checkMigrationVersion
// would enqueue different JIDs (telegram:..., atlas) for the same
// folder, spawning two parallel containers that double-narrated logs.
func TestEnqueueSerializesByFolder(t *testing.T) {
	q := New(5)
	q.SetFolderForJidFn(func(jid string) string {
		switch jid {
		case "telegram:atlas", "atlas":
			return "atlas"
		}
		return ""
	})

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

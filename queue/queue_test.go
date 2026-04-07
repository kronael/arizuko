package queue

import (
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
		return false, nil // failure
	})

	// 3 failures to trip breaker
	for i := 0; i < 3; i++ {
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

func TestBase36(t *testing.T) {
	if got := base36(0); got != "0000" {
		t.Fatalf("base36(0) = %q, want 0000", got)
	}
	if got := base36(36); got != "0010" {
		t.Fatalf("base36(36) = %q, want 0010", got)
	}
	// max value 36^4 - 1
	if got := base36(1679615); got != "zzzz" {
		t.Fatalf("base36(1679615) = %q, want zzzz", got)
	}
}

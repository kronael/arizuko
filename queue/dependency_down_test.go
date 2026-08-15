package queue

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// BUGS C9: a restart of the execution plane is not a verdict on any group. A
// krons deploy logged `connect: connection refused` to runed:8080 and opened
// four groups' breakers before runed had finished starting — the queue charged
// one dependency outage to every group at once.
func TestDependencyDownDoesNotCountTowardBreaker(t *testing.T) {
	q := New(5)

	var calls atomic.Int32
	q.SetProcessMessagesFn(func(string) (bool, error) {
		calls.Add(1)
		return false, fmt.Errorf("post /v1/runs: %w", ErrDependencyDown)
	})

	for range circuitBreakerThreshold + 2 {
		q.EnqueueMessageCheck("g1")
		time.Sleep(50 * time.Millisecond)
	}
	if got := calls.Load(); got == 0 {
		t.Fatal("fixture is vacuous: processMessages never ran")
	}

	q.mu.Lock()
	failures := q.groups["g1"].consecutiveFailures
	q.mu.Unlock()
	if failures != 0 {
		t.Errorf("unreachable execution plane charged to the group: %d failures, want 0", failures)
	}
}

// The exemption must not swallow a real failure: once runed is answering and
// rejecting this group's work, the breaker counts again.
func TestRealFailureStillCountsAfterDependencyRecovers(t *testing.T) {
	q := New(5)

	var down atomic.Bool
	down.Store(true)
	q.SetProcessMessagesFn(func(string) (bool, error) {
		if down.Load() {
			return false, fmt.Errorf("post /v1/runs: %w", ErrDependencyDown)
		}
		return false, fmt.Errorf("boom")
	})

	for range circuitBreakerThreshold {
		q.EnqueueMessageCheck("g1")
		time.Sleep(50 * time.Millisecond)
	}
	down.Store(false)
	for range circuitBreakerThreshold {
		q.EnqueueMessageCheck("g1")
		time.Sleep(50 * time.Millisecond)
	}

	q.mu.Lock()
	failures := q.groups["g1"].consecutiveFailures
	q.mu.Unlock()
	// EnqueueMessageCheck resets a tripped streak before each run, so the streak
	// tops out AT the threshold rather than climbing past it. The assertion that
	// matters is that real failures still accumulate at all.
	if failures == 0 {
		t.Error("real processing failures stopped counting toward the breaker")
	}
}

// A dependency-down return must not be mistaken for success either: it cannot
// clear a streak that real failures already built.
func TestDependencyDownDoesNotResetAnExistingStreak(t *testing.T) {
	q := New(5)

	var down atomic.Bool
	q.SetProcessMessagesFn(func(string) (bool, error) {
		if down.Load() {
			return false, fmt.Errorf("post /v1/runs: %w", ErrDependencyDown)
		}
		return false, fmt.Errorf("boom")
	})

	q.EnqueueMessageCheck("g1")
	time.Sleep(50 * time.Millisecond)
	q.mu.Lock()
	before := q.groups["g1"].consecutiveFailures
	q.mu.Unlock()
	if before != 1 {
		t.Fatalf("fixture: %d failures after one real error, want 1", before)
	}

	down.Store(true)
	q.EnqueueMessageCheck("g1")
	time.Sleep(50 * time.Millisecond)

	q.mu.Lock()
	after := q.groups["g1"].consecutiveFailures
	q.mu.Unlock()
	if after != before {
		t.Errorf("dependency-down moved the streak: %d -> %d, want it untouched", before, after)
	}
}

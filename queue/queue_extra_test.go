package queue

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// TestEnqueueMessageCheck_CircuitBreakerBlocksThenReset exercises the
// threshold edge: exactly 3 failures open the breaker; the 4th enqueue
// after a new message resets and runs.
func TestEnqueueMessageCheck_CircuitBreakerBlocksThenReset(t *testing.T) {
	q := New(5)

	var calls atomic.Int32
	q.SetProcessMessagesFn(func(jid string) (bool, error) {
		calls.Add(1)
		return false, fmt.Errorf("fail") // a real error every call
	})

	// Trip the circuit breaker: 3 failures.
	for range circuitBreakerThreshold {
		q.EnqueueMessageCheck("g1")
		time.Sleep(50 * time.Millisecond)
	}

	q.mu.Lock()
	tripped := q.groups["g1"].consecutiveFailures
	q.mu.Unlock()
	if tripped < circuitBreakerThreshold {
		t.Fatalf("expected >= %d failures, got %d", circuitBreakerThreshold, tripped)
	}

	// 4th enqueue should reset and run (calls counter increments).
	before := calls.Load()
	q.EnqueueMessageCheck("g1")
	time.Sleep(80 * time.Millisecond)

	after := calls.Load()
	if after <= before {
		t.Fatalf("4th enqueue after reset didn't trigger processMessages")
	}
}

// TestActiveCount_AfterConcurrentRuns ensures activeCount returns to 0 after
// multiple concurrent runs finish, so no phantom-active leak.
func TestActiveCount_AfterConcurrentRuns(t *testing.T) {
	q := New(4)

	gate := make(chan struct{})
	q.SetProcessMessagesFn(func(jid string) (bool, error) {
		<-gate
		return true, nil
	})

	for i := range 4 {
		q.EnqueueMessageCheck(fmt.Sprintf("g%d", i))
	}
	time.Sleep(50 * time.Millisecond)
	if q.ActiveCount() != 4 {
		t.Fatalf("activeCount = %d during run, want 4", q.ActiveCount())
	}
	close(gate)
	time.Sleep(150 * time.Millisecond)
	if q.ActiveCount() != 0 {
		t.Fatalf("activeCount = %d after all done, want 0", q.ActiveCount())
	}
}

package queue

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// TestRegisterProcess_SetsContainerAndFolder verifies RegisterProcess wires
// the container name and folder correctly, making the slot ready for
// SendMessages without requiring a running docker.
func TestRegisterProcess_SetsContainerAndFolder(t *testing.T) {
	q := New(1, t.TempDir())
	q.RegisterProcess("g1", "arizuko-g1", "grp/child")

	q.mu.Lock()
	defer q.mu.Unlock()
	s := q.groups["g1"]
	if s == nil {
		t.Fatal("group state not created by RegisterProcess")
	}
	if s.containerName != "arizuko-g1" {
		t.Errorf("containerName = %q, want arizuko-g1", s.containerName)
	}
	if s.groupFolder != "grp/child" {
		t.Errorf("groupFolder = %q, want grp/child", s.groupFolder)
	}
}

// TestRegisterProcess_EmptyFolderPreservesExisting verifies that calling
// RegisterProcess with an empty groupFolder does not clobber a previously
// set folder (race where the container runner calls Register twice).
func TestRegisterProcess_EmptyFolderPreservesExisting(t *testing.T) {
	q := New(1, t.TempDir())
	q.RegisterProcess("g1", "old-container", "my-folder")
	q.RegisterProcess("g1", "new-container", "") // empty groupFolder

	q.mu.Lock()
	defer q.mu.Unlock()
	s := q.groups["g1"]
	if s.groupFolder != "my-folder" {
		t.Errorf("groupFolder = %q after empty update, want my-folder", s.groupFolder)
	}
	if s.containerName != "new-container" {
		t.Errorf("containerName = %q, want new-container", s.containerName)
	}
}

// TestEnqueueMessageCheck_CircuitBreakerBlocksThenReset exercises the
// threshold edge: exactly 3 failures open the breaker; the 4th enqueue
// after a new message resets and runs.
func TestEnqueueMessageCheck_CircuitBreakerBlocksThenReset(t *testing.T) {
	q := New(5, t.TempDir())

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
	q := New(4, t.TempDir())
	q.SetHasPendingFn(func(string) bool { return false })

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

// TestWriteIpcFile_AtomicNamingFormat confirms each call produces a .json
// file with the expected timestamp-base36 name pattern and no .tmp leftovers.
// TestSendMessages_SignalFailDoesNotDoubleDecrementAlreadyCleared is a
// simplified version of the double-decrement regression: if the slot is
// already inactive (s.active == false) when SendMessages' error branch
// re-locks, the branch must be a no-op.

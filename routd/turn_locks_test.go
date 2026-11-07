package routd

import (
	"fmt"
	"runtime"
	"testing"
)

func TestTurnLocksReclaimed(t *testing.T) {
	for i := range 100 {
		unlock := lockTurn(fmt.Sprint(i))
		unlock()
	}
	turnLocks.Lock()
	count := len(turnLocks.entries)
	turnLocks.Unlock()
	if count != 0 {
		t.Fatalf("retained %d turn locks", count)
	}
}

func TestTurnLockWaiters(t *testing.T) {
	unlock := lockTurn("waiters")
	entered := make(chan struct{})
	release := make(chan struct{})
	go func() { defer lockTurn("waiters")(); close(entered); <-release }()
	for {
		turnLocks.Lock()
		refs := turnLocks.entries["waiters"].refs
		turnLocks.Unlock()
		if refs == 2 {
			break
		}
		runtime.Gosched()
	}
	unlock()
	<-entered
	turnLocks.Lock()
	refs := turnLocks.entries["waiters"].refs
	turnLocks.Unlock()
	if refs != 1 {
		t.Fatalf("waiter lost its lock: refs = %d", refs)
	}
	close(release)
	unlock = lockTurn("waiters")
	unlock()
}

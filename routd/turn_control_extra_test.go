package routd

import (
	"fmt"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// TestActiveCount_AfterConcurrentRuns ensures activeCount returns to 0 after
// multiple concurrent runs finish, so no phantom-active leak.
func TestActiveCount_AfterConcurrentRuns(t *testing.T) {
	q := newTurnControl(4)

	gate := make(chan struct{})
	setExecutionProcessor(q, nil, func(jid string) (bool, error) {
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

// A chat parked behind its folder's run, or behind the run cap, is reported to
// the parked fn; a chat that starts is not.
func TestEnqueueReportsParkedChats(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := newTurnControl(1)
		folder := func(jid string) string {
			if jid == "c" {
				return ""
			}
			return "f"
		}
		var mu sync.Mutex
		var parked []string
		q.SetParkedFn(func(jid string) {
			mu.Lock()
			parked = append(parked, jid)
			mu.Unlock()
		})
		gate := make(chan struct{})
		started := make(chan struct{}, 3)
		setExecutionProcessor(q, folder, func(string) (bool, error) {
			started <- struct{}{}
			<-gate
			return true, nil
		})
		q.EnqueueMessageCheck("a")
		<-started
		q.EnqueueMessageCheck("b") // folder f held by a
		synctest.Wait()
		q.EnqueueMessageCheck("c") // run cap full
		synctest.Wait()
		mu.Lock()
		got := append([]string(nil), parked...)
		mu.Unlock()
		if len(got) != 2 || got[0] != "b" || got[1] != "c" {
			t.Fatalf("parked = %v, want [b c]", got)
		}
		close(gate)
	})
}

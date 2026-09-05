package queue

// BUGS L161: every deploy restarts routd before runed listens, so the first
// polls always fail and the work re-feeds. At ERROR that was 26 lines in one
// krons restart, which trains the reader to filter the line that would carry a
// real outage. The level belongs to the OUTAGE, not to the group.

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type levelBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *levelBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *levelBuf) lines(level string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, l := range strings.Split(b.buf.String(), "\n") {
		if strings.Contains(l, "level="+level) && strings.Contains(l, "execution plane") {
			n++
		}
	}
	return n
}

func captureQueueLogs(t *testing.T) *levelBuf {
	t.Helper()
	buf := &levelBuf{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// downQueue returns a queue whose processMessages always reports the plane
// unreachable, plus a clock the test advances by hand.
func downQueue(t *testing.T) (*GroupQueue, *atomic.Int64, *atomic.Int32) {
	t.Helper()
	q := New(20)
	var nanos atomic.Int64
	base := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	q.now = func() time.Time { return base.Add(time.Duration(nanos.Load())) }

	var calls atomic.Int32
	q.SetProcessMessagesFn(func(string) (bool, error) {
		calls.Add(1)
		return false, fmt.Errorf("post /v1/runs: %w", ErrDependencyDown)
	})
	return q, &nanos, &calls
}

// runGroups enqueues one poll's worth of groups and waits for them to finish.
func runGroups(t *testing.T, q *GroupQueue, calls *atomic.Int32, n int) {
	t.Helper()
	want := calls.Load() + int32(n)
	for i := range n {
		q.EnqueueMessageCheck(fmt.Sprintf("g%d", i))
	}
	deadline := time.Now().Add(3 * time.Second)
	for calls.Load() < want && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if calls.Load() < want {
		t.Fatalf("only %d of %d group runs completed", calls.Load(), want)
	}
}

// A restart is a WARN, however many groups it hits. This is the measured krons
// shape: 26 groups all failing inside the same poll.
func TestDependencyDownRestartIsNotAnError(t *testing.T) {
	logs := captureQueueLogs(t)
	q, _, calls := downQueue(t)

	runGroups(t, q, calls, 26)

	if got := logs.lines("ERROR"); got != 0 {
		t.Fatalf("a restart logged %d ERROR lines, want 0", got)
	}
	if got := logs.lines("WARN"); got != 26 {
		t.Fatalf("logged %d WARN lines for 26 groups, want 26", got)
	}
}

// An outage that outlives the restart window IS an operator's problem, and is
// said ONCE — not once per group per poll, which is the noise this replaces.
func TestDependencyDownOutageErrorsExactlyOnce(t *testing.T) {
	logs := captureQueueLogs(t)
	q, nanos, calls := downQueue(t)

	runGroups(t, q, calls, 3) // opens the outage inside the grace window
	if got := logs.lines("ERROR"); got != 0 {
		t.Fatalf("errored inside the grace window: %d lines", got)
	}

	nanos.Store(int64(dependencyDownGrace + time.Second))
	runGroups(t, q, calls, 5) // first poll past the window
	runGroups(t, q, calls, 5) // and the next one

	if got := logs.lines("ERROR"); got != 1 {
		t.Fatalf("a sustained outage logged %d ERROR lines, want exactly 1", got)
	}
}

// Recovery clears the outage, so the NEXT one is reported afresh rather than
// suppressed by the last one's state.
func TestDependencyDownRecoveryReopensTheOutage(t *testing.T) {
	logs := captureQueueLogs(t)
	q := New(20)
	var nanos atomic.Int64
	base := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	q.now = func() time.Time { return base.Add(time.Duration(nanos.Load())) }

	var down atomic.Bool
	var calls atomic.Int32
	down.Store(true)
	q.SetProcessMessagesFn(func(string) (bool, error) {
		calls.Add(1)
		if down.Load() {
			return false, fmt.Errorf("post /v1/runs: %w", ErrDependencyDown)
		}
		return true, nil
	})

	// First outage: opens inside the grace, crosses it on the next poll.
	runGroups(t, q, &calls, 2)
	nanos.Store(int64(dependencyDownGrace + time.Second))
	runGroups(t, q, &calls, 2)
	if got := logs.lines("ERROR"); got != 1 {
		t.Fatalf("first outage logged %d ERROR lines, want 1", got)
	}

	// The plane answers: the outage is over.
	down.Store(false)
	runGroups(t, q, &calls, 1)

	// A second outage of its own must be reported again, not suppressed by the
	// first one's state.
	down.Store(true)
	runGroups(t, q, &calls, 2)
	nanos.Store(int64(3 * dependencyDownGrace))
	runGroups(t, q, &calls, 2)
	if got := logs.lines("ERROR"); got != 2 {
		t.Fatalf("second outage logged a total of %d ERROR lines, want 2", got)
	}
}

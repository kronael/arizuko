package chanlib

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTypingRefresher_RefreshesUntilStopped(t *testing.T) {
	var sends atomic.Int32
	var clears atomic.Int32
	r := NewTypingRefresher(20*time.Millisecond, time.Second,
		func(string) bool { sends.Add(1); return true },
		func(string) { clears.Add(1) },
	)

	r.Set("jid1", true)
	time.Sleep(75 * time.Millisecond) // immediate + ~3 refreshes
	r.Set("jid1", false)

	// Allow any pending goroutine scheduling to settle.
	time.Sleep(30 * time.Millisecond)

	if got := sends.Load(); got < 3 {
		t.Errorf("sends = %d, want >= 3", got)
	}
	if got := clears.Load(); got != 1 {
		t.Errorf("clears = %d, want 1", got)
	}

	// After stop, no more sends should arrive.
	before := sends.Load()
	time.Sleep(60 * time.Millisecond)
	if after := sends.Load(); after != before {
		t.Errorf("sends continued after stop: before=%d after=%d", before, after)
	}
}

func TestTypingRefresher_MaxTTLCap(t *testing.T) {
	var sends atomic.Int32
	r := NewTypingRefresher(10*time.Millisecond, 50*time.Millisecond,
		func(string) bool { sends.Add(1); return true },
		nil,
	)

	r.Set("jid1", true)
	time.Sleep(200 * time.Millisecond) // well past maxTTL

	// Sends should stop around maxTTL; allow some slack for scheduling.
	got := sends.Load()
	if got < 3 || got > 10 {
		t.Errorf("sends = %d, want 3..10 (capped at maxTTL)", got)
	}

	// Confirm it really stopped.
	before := sends.Load()
	time.Sleep(60 * time.Millisecond)
	if after := sends.Load(); after != before {
		t.Errorf("sends continued past maxTTL: before=%d after=%d", before, after)
	}
}

func TestTypingRefresher_MaxTTLCallsClear(t *testing.T) {
	var clears atomic.Int32
	r := NewTypingRefresher(10*time.Millisecond, 50*time.Millisecond,
		func(string) bool { return true },
		func(string) { clears.Add(1) },
	)

	r.Set("jid1", true)
	time.Sleep(200 * time.Millisecond) // well past maxTTL

	if got := clears.Load(); got != 1 {
		t.Errorf("clears after maxTTL = %d, want 1", got)
	}
}

func TestTypingRefresher_NilClearSafe(t *testing.T) {
	r := NewTypingRefresher(10*time.Millisecond, 50*time.Millisecond,
		func(string) bool { return true },
		nil,
	)
	// Should not panic with nil clear on any path.
	r.Set("jid1", true)
	time.Sleep(100 * time.Millisecond) // maxTTL fires
	r.Set("jid2", true)
	r.Set("jid2", false) // explicit off
}

func TestTypingRefresher_PerJIDIsolation(t *testing.T) {
	var mu sync.Mutex
	sends := map[string]int{}
	r := NewTypingRefresher(15*time.Millisecond, time.Second,
		func(jid string) bool {
			mu.Lock()
			sends[jid]++
			mu.Unlock()
			return true
		},
		nil,
	)

	r.Set("jidA", true)
	r.Set("jidB", true)
	time.Sleep(60 * time.Millisecond)
	r.Set("jidA", false)
	time.Sleep(60 * time.Millisecond)
	r.Set("jidB", false)

	mu.Lock()
	a, b := sends["jidA"], sends["jidB"]
	mu.Unlock()

	if a < 2 || a > 8 {
		t.Errorf("jidA sends = %d, want 2..8", a)
	}
	if b < a {
		t.Errorf("jidB should have >= jidA sends; got a=%d b=%d", a, b)
	}
}

func TestTypingRefresher_ReentrantSetOn(t *testing.T) {
	var sends atomic.Int32
	r := NewTypingRefresher(20*time.Millisecond, time.Second,
		func(string) bool { sends.Add(1); return true },
		nil,
	)

	// Two Set(true) in a row should cancel the first loop and start a fresh one.
	r.Set("jid1", true)
	time.Sleep(30 * time.Millisecond)
	r.Set("jid1", true)
	time.Sleep(30 * time.Millisecond)
	r.Set("jid1", false)

	// Confirm no lingering goroutine is still firing.
	before := sends.Load()
	time.Sleep(60 * time.Millisecond)
	if after := sends.Load(); after != before {
		t.Errorf("sends continued after stop: before=%d after=%d", before, after)
	}
}

func TestTypingRefresher_StopAllOnShutdown(t *testing.T) {
	var sends atomic.Int32
	r := NewTypingRefresher(15*time.Millisecond, time.Second,
		func(string) bool { sends.Add(1); return true },
		nil,
	)

	r.Set("jidA", true)
	r.Set("jidB", true)
	time.Sleep(30 * time.Millisecond)
	r.Stop()

	before := sends.Load()
	time.Sleep(60 * time.Millisecond)
	if after := sends.Load(); after != before {
		t.Errorf("sends continued after Stop: before=%d after=%d", before, after)
	}
}

func TestTypingRefresher_StopsOnSendError(t *testing.T) {
	var sends atomic.Int32
	r := NewTypingRefresher(20*time.Millisecond, time.Second,
		func(string) bool { sends.Add(1); return false },
		nil,
	)

	r.Set("jid1", true)
	time.Sleep(80 * time.Millisecond)

	// Eager send fires once in Set; loop sends once then stops on first error.
	// Total: 2 sends (eager + one loop tick).
	if got := sends.Load(); got < 1 || got > 2 {
		t.Errorf("sends = %d, want 1-2 (loop stops after error)", got)
	}
}

func TestTypingRefresher_EagerSendFailureStartsLoop(t *testing.T) {
	// Eager send in Set fails; loop goroutine starts anyway and succeeds on
	// subsequent ticks. This is the fix for "typing dead when first API call
	// hits a transient error."
	var sends atomic.Int32
	r := NewTypingRefresher(20*time.Millisecond, time.Second,
		func(string) bool {
			n := sends.Add(1)
			return n != 1 // first (eager) call fails; loop ticks succeed
		},
		nil,
	)

	r.Set("jid1", true)
	time.Sleep(100 * time.Millisecond) // several loop ticks
	r.Set("jid1", false)

	if got := sends.Load(); got < 3 {
		t.Errorf("sends = %d, want >= 3 (loop ran despite eager-send failure)", got)
	}
}

// The backstop must land STRICTLY below the run wall. At a flat 20m it equalled
// the RUNED_RUN_TIMEOUT default, so it never fired before the container's own
// SIGKILL and a stalled turn showed "typing" for the full run (BUGS T1).
func TestTypingMaxTTLStaysBelowTheRunWall(t *testing.T) {
	for _, runTTL := range []time.Duration{
		20 * time.Minute, 30 * time.Minute, 5 * time.Minute, time.Hour,
	} {
		got := typingMaxTTL(runTTL)
		if got >= runTTL {
			t.Errorf("typingMaxTTL(%s) = %s — not below the wall, so not a backstop", runTTL, got)
		}
	}
}

// A tiny or absurd RUNED_RUN_TIMEOUT must not produce a backstop that kills the
// indicator before the first refresh tick.
func TestTypingMaxTTLHasAFloor(t *testing.T) {
	if got := typingMaxTTL(time.Second); got < minTypingMaxTTL {
		t.Errorf("typingMaxTTL(1s) = %s, want at least %s", got, minTypingMaxTTL)
	}
}

// The default matches the RUNED_RUN_TIMEOUT default runed and routd use, so an
// unset env leaves the three in agreement.
func TestDefaultTypingMaxTTLTracksTheDefaultWall(t *testing.T) {
	if want := typingMaxTTL(20 * time.Minute); DefaultTypingMaxTTL != want {
		t.Errorf("DefaultTypingMaxTTL = %s, want %s", DefaultTypingMaxTTL, want)
	}
}

func TestEnvDur(t *testing.T) {
	t.Setenv("ARZ_TEST_DUR", "30m")
	if got := EnvDur("ARZ_TEST_DUR", time.Minute); got != 30*time.Minute {
		t.Errorf("EnvDur = %s, want 30m", got)
	}
	t.Setenv("ARZ_TEST_DUR", "nonsense")
	if got := EnvDur("ARZ_TEST_DUR", time.Minute); got != time.Minute {
		t.Errorf("EnvDur on unparseable = %s, want the fallback", got)
	}
	t.Setenv("ARZ_TEST_DUR", "-5m")
	if got := EnvDur("ARZ_TEST_DUR", time.Minute); got != time.Minute {
		t.Errorf("EnvDur on non-positive = %s, want the fallback", got)
	}
}

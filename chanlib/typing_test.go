package chanlib

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTypingRefresher_RefreshesUntilStopped(t *testing.T) {
	var sends int32
	var clears int32
	r := NewTypingRefresher(20*time.Millisecond, time.Second,
		func(string) { atomic.AddInt32(&sends, 1) },
		func(string) { atomic.AddInt32(&clears, 1) },
	)

	r.Set("jid1", true)
	time.Sleep(75 * time.Millisecond) // immediate + ~3 refreshes
	r.Set("jid1", false)

	// Allow any pending goroutine scheduling to settle.
	time.Sleep(30 * time.Millisecond)

	if got := atomic.LoadInt32(&sends); got < 3 {
		t.Errorf("sends = %d, want >= 3", got)
	}
	if got := atomic.LoadInt32(&clears); got != 1 {
		t.Errorf("clears = %d, want 1", got)
	}

	// After stop, no more sends should arrive.
	before := atomic.LoadInt32(&sends)
	time.Sleep(60 * time.Millisecond)
	if after := atomic.LoadInt32(&sends); after != before {
		t.Errorf("sends continued after stop: before=%d after=%d", before, after)
	}
}

func TestTypingRefresher_MaxTTLCap(t *testing.T) {
	var sends int32
	r := NewTypingRefresher(10*time.Millisecond, 50*time.Millisecond,
		func(string) { atomic.AddInt32(&sends, 1) },
		nil,
	)

	r.Set("jid1", true)
	time.Sleep(200 * time.Millisecond) // well past maxTTL

	// Sends should stop around maxTTL; allow some slack for scheduling.
	got := atomic.LoadInt32(&sends)
	if got < 3 || got > 10 {
		t.Errorf("sends = %d, want 3..10 (capped at maxTTL)", got)
	}

	// Confirm it really stopped.
	before := atomic.LoadInt32(&sends)
	time.Sleep(60 * time.Millisecond)
	if after := atomic.LoadInt32(&sends); after != before {
		t.Errorf("sends continued past maxTTL: before=%d after=%d", before, after)
	}
}

func TestTypingRefresher_PerJIDIsolation(t *testing.T) {
	var mu sync.Mutex
	sends := map[string]int{}
	r := NewTypingRefresher(15*time.Millisecond, time.Second,
		func(jid string) {
			mu.Lock()
			sends[jid]++
			mu.Unlock()
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
	var sends int32
	r := NewTypingRefresher(20*time.Millisecond, time.Second,
		func(string) { atomic.AddInt32(&sends, 1) },
		nil,
	)

	// Two Set(true) in a row should cancel the first loop and start a fresh one.
	r.Set("jid1", true)
	time.Sleep(30 * time.Millisecond)
	r.Set("jid1", true)
	time.Sleep(30 * time.Millisecond)
	r.Set("jid1", false)

	// Confirm no lingering goroutine is still firing.
	before := atomic.LoadInt32(&sends)
	time.Sleep(60 * time.Millisecond)
	if after := atomic.LoadInt32(&sends); after != before {
		t.Errorf("sends continued after stop: before=%d after=%d", before, after)
	}
}

func TestTypingRefresher_StopAllOnShutdown(t *testing.T) {
	var sends int32
	r := NewTypingRefresher(15*time.Millisecond, time.Second,
		func(string) { atomic.AddInt32(&sends, 1) },
		nil,
	)

	r.Set("jidA", true)
	r.Set("jidB", true)
	time.Sleep(30 * time.Millisecond)
	r.Stop()

	before := atomic.LoadInt32(&sends)
	time.Sleep(60 * time.Millisecond)
	if after := atomic.LoadInt32(&sends); after != before {
		t.Errorf("sends continued after Stop: before=%d after=%d", before, after)
	}
}

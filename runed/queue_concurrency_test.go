package runed

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/types"
)

// TestDistinctFoldersRunConcurrently: two runs in DIFFERENT folders execute at
// the same time (peak concurrency 2) — the per-folder serialization gate must
// NOT serialize across folders. The cap (5) is well above 2, so the only thing
// that could keep peak at 1 is wrongly treating distinct folders as one.
// (TestConcurrencyCap proves the cap blocks; this proves folders genuinely
// overlap when under the cap — the complementary direction.)
func TestDistinctFoldersRunConcurrently(t *testing.T) {
	var live, peak int32
	bothLive := make(chan struct{})
	var once sync.Once
	rt := FakeRuntime{Fn: func(_ context.Context, _ RunSpec) RunResult {
		n := atomic.AddInt32(&live, 1)
		for {
			p := atomic.LoadInt32(&peak)
			if n <= p || atomic.CompareAndSwapInt32(&peak, p, n) {
				break
			}
		}
		if n == 2 {
			once.Do(func() { close(bothLive) })
		}
		// hold until both are concurrently live (or a timeout safety net).
		select {
		case <-bothLive:
		case <-time.After(2 * time.Second):
		}
		atomic.AddInt32(&live, -1)
		return RunResult{Outcome: runedv1.OutcomeOK, NewSessionID: "s"}
	}}
	_, mgr := newMgr(t, rt, 5)

	var wg sync.WaitGroup
	for _, f := range []string{"alice", "bob"} {
		wg.Add(1)
		go func(folder string) {
			defer wg.Done()
			mgr.Run(context.Background(), runedv1.RunRequest{Folder: types.Folder(folder), MessageBatch: "m"})
		}(f)
	}
	wg.Wait()
	if peak != 2 {
		t.Fatalf("peak concurrency across 2 folders=%d want 2 (folders must not serialize against each other)", peak)
	}
}

// TestCapAdmitsQueuedRunWhenSlotFrees: with cap=1, a second folder's run is
// queued behind the first and admitted the instant the first frees — the FIFO
// admission drain releases it (spec 5/P § waiting queue). Asserts the queued
// run actually runs (not dropped), and never overlaps the first.
func TestCapAdmitsQueuedRunWhenSlotFrees(t *testing.T) {
	var live, peak int32
	var ran sync.Map
	release := make(chan struct{})
	first := make(chan struct{})
	rt := FakeRuntime{Fn: func(_ context.Context, spec RunSpec) RunResult {
		ran.Store(spec.Folder, true)
		n := atomic.AddInt32(&live, 1)
		for {
			p := atomic.LoadInt32(&peak)
			if n <= p || atomic.CompareAndSwapInt32(&peak, p, n) {
				break
			}
		}
		if spec.Folder == "alice" {
			close(first)
			<-release // hold the only slot until released.
		}
		atomic.AddInt32(&live, -1)
		return RunResult{Outcome: runedv1.OutcomeOK, NewSessionID: "s"}
	}}
	_, mgr := newMgr(t, rt, 1)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		mgr.Run(context.Background(), runedv1.RunRequest{Folder: "alice", MessageBatch: "m"})
	}()
	<-first // alice holds the only slot.

	wg.Add(1)
	go func() {
		defer wg.Done()
		mgr.Run(context.Background(), runedv1.RunRequest{Folder: "bob", MessageBatch: "m"})
	}()
	// give bob a window to (wrongly) start over the cap.
	time.Sleep(40 * time.Millisecond)
	if atomic.LoadInt32(&live) > 1 {
		t.Fatalf("live=%d exceeded cap 1 (queued run started early)", atomic.LoadInt32(&live))
	}
	close(release)
	wg.Wait()

	if _, ok := ran.Load("bob"); !ok { // RunSpec.Folder is a plain string
		t.Fatal("queued run (bob) never admitted after the slot freed")
	}
	if peak > 1 {
		t.Fatalf("peak=%d exceeded cap 1", peak)
	}
}

// TestBreakerNotCorruptedByStaleRun: a run whose folder slot was already
// reassigned (e.g. killed + a fresh run took the slot) must NOT mutate the
// breaker counter when it finally returns — endRun guards on fr.runID==runID,
// so a stale run finishing late is a no-op on the breaker. Without the guard a
// late stale failure would increment the NEW run's folder counter and could
// trip the breaker on a healthy folder.
func TestBreakerNotCorruptedByStaleRun(t *testing.T) {
	_, mgr := newMgr(t, FakeRuntime{}, 5)

	// Register a live run for the folder (the "current" owner of the slot).
	mgr.mu.Lock()
	mgr.active["demo"] = &folderRun{runID: "current", sessionID: "s"}
	mgr.activeCount++
	mgr.mu.Unlock()

	// A STALE run (different run_id) reports a failure via endRun. Because it no
	// longer owns the slot, the breaker counter must stay 0.
	tripped := mgr.endRun("demo", "stale-run", true)
	if tripped {
		t.Fatal("stale run tripped the breaker despite not owning the folder slot")
	}
	mgr.mu.Lock()
	got := mgr.failures["demo"]
	stillActive := mgr.active["demo"] != nil && mgr.active["demo"].runID == "current"
	mgr.mu.Unlock()
	if got != 0 {
		t.Fatalf("breaker failures[demo]=%d want 0 (stale run must not corrupt the counter)", got)
	}
	if !stillActive {
		t.Fatal("stale endRun evicted the current live run from the slot")
	}
}

// TestKillFreesSlotForQueuedRun: killing a folder's live run frees its slot and
// drains a waiter — the operator-kill path must not deadlock the queue. A run
// queued behind a stuck folder is admitted once Kill releases the slot.
func TestKillFreesSlotForQueuedRun(t *testing.T) {
	started := make(chan struct{})
	hold := make(chan struct{})
	secondRan := make(chan struct{})
	var which atomic.Int32
	rt := &killRecorder{FakeRuntime: FakeRuntime{Fn: func(_ context.Context, spec RunSpec) RunResult {
		if which.Add(1) == 1 {
			close(started)
			<-hold // first run hangs until killed.
			return RunResult{Outcome: runedv1.OutcomeError, Error: "killed"}
		}
		close(secondRan)
		return RunResult{Outcome: runedv1.OutcomeOK, NewSessionID: "s2"}
	}}}
	_, mgr := newMgr(t, rt, 1)

	go mgr.Run(context.Background(), runedv1.RunRequest{Folder: "demo", MessageBatch: "first"})
	<-started
	runID := mgr.ActiveRunID("demo")

	// second run for a DIFFERENT folder queues behind the cap (cap=1).
	go mgr.Run(context.Background(), runedv1.RunRequest{Folder: "other", MessageBatch: "second"})
	time.Sleep(20 * time.Millisecond) // let it queue.

	// Kill the first → frees the only slot → the queued second is admitted.
	if err := mgr.Kill(runID); err != nil {
		t.Fatalf("kill: %v", err)
	}
	close(hold) // let the first run's goroutine unwind.

	select {
	case <-secondRan:
	case <-time.After(2 * time.Second):
		t.Fatal("queued run never admitted after Kill freed the slot (queue deadlock)")
	}
}

// TestRunCancelledWhileQueuedDropsWaiter: a Run blocked in the admission queue
// whose ctx is cancelled returns ctx.Err() and removes itself from the queue —
// it must not leak a dead waiter that a later drain would (harmlessly but
// wastefully) close, nor block the folder forever.
func TestRunCancelledWhileQueuedDropsWaiter(t *testing.T) {
	hold := make(chan struct{})
	started := make(chan struct{})
	rt := FakeRuntime{Fn: func(_ context.Context, _ RunSpec) RunResult {
		close(started)
		<-hold
		return RunResult{Outcome: runedv1.OutcomeOK, NewSessionID: "s"}
	}}
	_, mgr := newMgr(t, rt, 1)

	go mgr.Run(context.Background(), runedv1.RunRequest{Folder: "alice", MessageBatch: "m"})
	<-started // alice holds the only slot.

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := mgr.Run(ctx, runedv1.RunRequest{Folder: "bob", MessageBatch: "m"})
		errc <- err
	}()
	time.Sleep(20 * time.Millisecond) // bob is queued.
	cancel()

	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("cancelled queued Run returned nil err, want ctx.Canceled")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled queued Run never returned")
	}
	// the waiter was dropped: queue is empty.
	mgr.mu.Lock()
	n := len(mgr.waiting)
	mgr.mu.Unlock()
	if n != 0 {
		t.Fatalf("waiting queue len=%d after cancel, want 0 (dead waiter leaked)", n)
	}
	close(hold)
}

// TestSteerWhenNoLiveRunSpawns: a Run for an idle folder with no live spawn does
// NOT attempt to steer — it spawns fresh (steered:false). The steer branch is
// only taken when m.active[folder] != nil. The complement of TestSteerWhenLive.
func TestSteerWhenNoLiveRunSpawns(t *testing.T) {
	var spawns int32
	rt := FakeRuntime{Fn: func(_ context.Context, _ RunSpec) RunResult {
		atomic.AddInt32(&spawns, 1)
		return RunResult{Outcome: runedv1.OutcomeOK, NewSessionID: "s"}
	}}
	_, mgr := newMgr(t, rt, 5)

	out, _ := mgr.Run(context.Background(), runedv1.RunRequest{Folder: "idle", MessageBatch: "m"})
	if out.Steered {
		t.Fatalf("idle-folder run steered=%v want false (no live container to steer into)", out.Steered)
	}
	if out.Outcome != runedv1.OutcomeOK {
		t.Fatalf("outcome=%q want ok", out.Outcome)
	}
	if atomic.LoadInt32(&spawns) != 1 {
		t.Fatalf("spawns=%d want 1 (fresh spawn, not a steer ack)", spawns)
	}
}

// TestSetSteerIgnoresStaleRunID: SetSteer for a run_id that no longer owns the
// folder's slot is a no-op — a late steer registration from a finished run must
// not overwrite the current live run's steer closure (the slot-reassignment
// guard, fr.runID==runID).
func TestSetSteerIgnoresStaleRunID(t *testing.T) {
	_, mgr := newMgr(t, FakeRuntime{}, 5)
	mgr.mu.Lock()
	mgr.active["demo"] = &folderRun{runID: "current", sessionID: "s"}
	mgr.mu.Unlock()

	called := false
	mgr.SetSteer("demo", "stale", func(string) bool { called = true; return true })
	mgr.mu.Lock()
	fr := mgr.active["demo"]
	mgr.mu.Unlock()
	if fr.steer != nil {
		t.Fatal("SetSteer for a stale run_id overwrote the current run's steer closure")
	}
	_ = called
}

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

// TestBreakerPersistsToDB: a failed run increments the circuit_breaker table,
// a successful run resets it, and the count survives a manager restart.
func TestBreakerPersistsToDB(t *testing.T) {
	db, mgr := newMgr(t, FakeRuntime{Fn: func(_ context.Context, _ RunSpec) RunResult {
		return RunResult{Outcome: runedv1.OutcomeError, Error: "boom"}
	}}, 5)

	// Two failures → count is 2 in DB.
	mgr.Run(context.Background(), runedv1.RunRequest{Folder: "demo", MessageBatch: "m"})
	mgr.Run(context.Background(), runedv1.RunRequest{Folder: "demo", MessageBatch: "m"})

	got, err := db.GetFailures("demo")
	if err != nil {
		t.Fatalf("GetFailures: %v", err)
	}
	if got != 2 {
		t.Fatalf("failure count=%d want 2", got)
	}

	// Simulate restart: create a new manager with the same DB.
	mgr2 := NewManager(db, FakeRuntime{Fn: func(_ context.Context, _ RunSpec) RunResult {
		return RunResult{Outcome: runedv1.OutcomeOK, NewSessionID: "s"}
	}}, NewStaticBroker("jws", "jti"), ManagerConfig{
		Scopes: []types.Scope{"messages:send:own_group"}, Instance: "test", MaxConcurrent: 5,
	})

	// New manager reads existing failure count. A successful run resets it.
	mgr2.Run(context.Background(), runedv1.RunRequest{Folder: "demo", MessageBatch: "m"})
	got, _ = db.GetFailures("demo")
	if got != 0 {
		t.Fatalf("failure count after success=%d want 0 (reset)", got)
	}
}

// TestActiveSpawnsPersistAcrossRestart: spawns in 'running' state are visible
// to a new manager (simulated restart), enabling correct cap enforcement and
// folder exclusivity without in-memory state.
func TestActiveSpawnsPersistAcrossRestart(t *testing.T) {
	db, _ := newMgr(t, FakeRuntime{}, 5)

	// Simulate two active spawns left by a crashed manager.
	_ = db.CreateSpawn(Spawn{RunID: "r1", Folder: "a", ContainerName: "c1", State: "running"})
	_ = db.CreateSpawn(Spawn{RunID: "r2", Folder: "b", ContainerName: "c2", State: "queued"})
	// One terminated spawn (should not count).
	_ = db.CreateSpawn(Spawn{RunID: "r3", Folder: "c", ContainerName: "c3", State: "exited"})

	// New manager reads state from DB.
	mgr2 := NewManager(db, FakeRuntime{}, NewStaticBroker("jws", "jti"), ManagerConfig{
		Scopes: []types.Scope{"messages:send:own_group"}, Instance: "test", MaxConcurrent: 3,
	})

	// ActiveCount should return 2 (the two live spawns).
	if got := mgr2.ActiveCount(); got != 2 {
		t.Fatalf("ActiveCount()=%d want 2 (DB state persisted)", got)
	}

	// ActiveRunID for folder "a" should return "r1".
	if got := mgr2.ActiveRunID("a"); got != "r1" {
		t.Fatalf("ActiveRunID(a)=%q want r1", got)
	}

	// Folder "c" has no active spawn (it exited).
	if got := mgr2.ActiveRunID("c"); got != "" {
		t.Fatalf("ActiveRunID(c)=%q want empty (exited)", got)
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
// guard checks DB state).
func TestSetSteerIgnoresStaleRunID(t *testing.T) {
	db, mgr := newMgr(t, FakeRuntime{}, 5)

	// Create a live spawn in the DB with run_id "current".
	_ = db.CreateSpawn(Spawn{RunID: "current", Folder: "demo", ContainerName: "c", State: "running", SessionID: "s"})

	// Try to set steer for a STALE run_id — should be ignored.
	called := false
	mgr.SetSteer("demo", "stale", func(string) bool { called = true; return true })

	// The steer callback should NOT be set (stale run_id doesn't match DB).
	mgr.mu.Lock()
	steer := mgr.steerFns["demo"]
	mgr.mu.Unlock()
	if steer != nil {
		t.Fatal("SetSteer for a stale run_id set a steer callback")
	}
	_ = called
}

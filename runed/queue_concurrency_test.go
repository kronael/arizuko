package runed

import (
	"context"
	"fmt"
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

// TestConcurrencyCapNoOverAdmit: with cap=K and N>K concurrent Runs on distinct
// folders, exactly K are admitted and every other caller gets a retryable busy
// outcome — never a block, never an over-admit. The atomic count-and-claim under
// the lock is what holds the cap (spec 5/P § Run state — pure claim-or-reject,
// no internal queue). This is the race-hard proof the queue removal is safe.
func TestConcurrencyCapNoOverAdmit(t *testing.T) {
	const cap, callers = 3, 12
	var live, peak, admitted, busy int32
	release := make(chan struct{})
	rt := FakeRuntime{Fn: func(_ context.Context, _ RunSpec) RunResult {
		n := atomic.AddInt32(&live, 1)
		for {
			p := atomic.LoadInt32(&peak)
			if n <= p || atomic.CompareAndSwapInt32(&peak, p, n) {
				break
			}
		}
		<-release // hold every admitted slot so all K are concurrently live.
		atomic.AddInt32(&live, -1)
		return RunResult{Outcome: runedv1.OutcomeOK, NewSessionID: "s"}
	}}
	_, mgr := newMgr(t, rt, cap)

	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			out, _ := mgr.Run(context.Background(),
				runedv1.RunRequest{Folder: types.Folder(fmt.Sprintf("f%d", i)), MessageBatch: "m"})
			if out.Busy {
				atomic.AddInt32(&busy, 1)
			} else {
				atomic.AddInt32(&admitted, 1)
			}
		}(i)
	}

	// Wait until the cap is saturated (all K admitted runs live), then give an
	// over-cap admit a window to (wrongly) start before releasing.
	deadline := time.After(2 * time.Second)
	for atomic.LoadInt32(&live) < cap {
		select {
		case <-deadline:
			t.Fatalf("cap never saturated: live=%d want %d", atomic.LoadInt32(&live), cap)
		default:
			time.Sleep(time.Millisecond)
		}
	}
	time.Sleep(50 * time.Millisecond)
	if l := atomic.LoadInt32(&live); l > cap {
		t.Fatalf("live=%d exceeded cap %d (over-admit)", l, cap)
	}
	close(release)
	wg.Wait()

	if peak > cap {
		t.Fatalf("peak=%d exceeded cap %d", peak, cap)
	}
	if admitted != cap {
		t.Fatalf("admitted=%d want exactly %d (the cap)", admitted, cap)
	}
	if busy != callers-cap {
		t.Fatalf("busy=%d want %d (every non-admitted caller gets a busy reject)", busy, callers-cap)
	}
}

// TestFolderBusySteerFailReturnsBusy: a second Run for a folder with a live
// spawn but NO steer callback wired returns busy — not a block, not an error,
// not a second spawn. Steer-fail is the retryable reject path (spec 5/P § Run
// state — steer or busy, never enqueue).
func TestFolderBusySteerFailReturnsBusy(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	var spawns int32
	rt := FakeRuntime{Fn: func(_ context.Context, _ RunSpec) RunResult {
		atomic.AddInt32(&spawns, 1)
		close(started)
		<-release
		return RunResult{Outcome: runedv1.OutcomeOK, NewSessionID: "s"}
	}}
	_, mgr := newMgr(t, rt, 5)

	go func() {
		mgr.Run(context.Background(), runedv1.RunRequest{Folder: "demo", MessageBatch: "first"})
		close(done)
	}()
	<-started // demo has a live spawn; no steer callback wired.

	out, err := mgr.Run(context.Background(), runedv1.RunRequest{Folder: "demo", MessageBatch: "second"})
	if err != nil {
		t.Fatalf("busy reject returned err=%v want nil (busy is not an error)", err)
	}
	if !out.Busy {
		t.Fatalf("folder busy, steer absent → out=%+v want Busy=true", out)
	}
	if out.Steered {
		t.Fatal("busy reject must not be a steer ack")
	}
	close(release)
	<-done
	if n := atomic.LoadInt32(&spawns); n != 1 {
		t.Fatalf("spawns=%d want 1 (the busy caller must not spawn a second container)", n)
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

// TestKillFreesSlotForNextRun: killing a folder's live run frees the only slot,
// so a RESUBMITTED batch (routd re-feeds on its poll) is admitted where it was
// busy before. With no internal queue, the free-and-readmit is caller-driven —
// the operator kill must not strand the folder.
func TestKillFreesSlotForNextRun(t *testing.T) {
	hold := make(chan struct{})
	firstStarted := make(chan struct{})
	secondRan := make(chan struct{})
	var which atomic.Int32
	rt := &killRecorder{FakeRuntime: FakeRuntime{Fn: func(_ context.Context, _ RunSpec) RunResult {
		if which.Add(1) == 1 {
			close(firstStarted)
			<-hold // first run hangs until killed.
			return RunResult{Outcome: runedv1.OutcomeError, Error: "killed"}
		}
		close(secondRan)
		return RunResult{Outcome: runedv1.OutcomeOK, NewSessionID: "s2"}
	}}}
	_, mgr := newMgr(t, rt, 1)

	go mgr.Run(context.Background(), runedv1.RunRequest{Folder: "demo", MessageBatch: "first"})
	<-firstStarted
	runID := mgr.ActiveRunID("demo")

	// At cap=1 a second folder is rejected busy — runed keeps no queue.
	out, _ := mgr.Run(context.Background(), runedv1.RunRequest{Folder: "other", MessageBatch: "second"})
	if !out.Busy {
		t.Fatalf("at cap, second folder out=%+v want Busy=true (no internal queue)", out)
	}

	// Kill the first → frees the only slot → the resubmitted batch now runs.
	if err := mgr.Kill(runID); err != nil {
		t.Fatalf("kill: %v", err)
	}
	go mgr.Run(context.Background(), runedv1.RunRequest{Folder: "other", MessageBatch: "second"})
	select {
	case <-secondRan:
	case <-time.After(2 * time.Second):
		t.Fatal("resubmitted run never admitted after Kill freed the slot")
	}
	close(hold) // let the first run's goroutine unwind.
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

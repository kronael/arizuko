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
	rt := FakeRuntime{Fn: func(_ context.Context, _ Turn) RunResult {
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
		return RunResult{Outcome: runedv1.OutcomeOK}
	}}
	_, mgr := newMgr(t, rt, 5)

	var wg sync.WaitGroup
	for _, f := range []string{"alice", "bob"} {
		wg.Add(1)
		go func(folder string) {
			defer wg.Done()
			mgr.Run(context.Background(), runedv1.RunRequest{Packages: []string{"core"}, Folder: types.Folder(folder), MessageBatch: "m"})
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
// the lock is what holds the cap (spec 5/P-runed § Run state — pure claim-or-reject,
// no internal queue). This is the race-hard proof the queue removal is safe.
func TestConcurrencyCapNoOverAdmit(t *testing.T) {
	const cap, callers = 3, 12
	var live, peak, admitted, busy int32
	release := make(chan struct{})
	rt := FakeRuntime{Fn: func(_ context.Context, _ Turn) RunResult {
		n := atomic.AddInt32(&live, 1)
		for {
			p := atomic.LoadInt32(&peak)
			if n <= p || atomic.CompareAndSwapInt32(&peak, p, n) {
				break
			}
		}
		<-release // hold every admitted slot so all K are concurrently live.
		atomic.AddInt32(&live, -1)
		return RunResult{Outcome: runedv1.OutcomeOK}
	}}
	_, mgr := newMgr(t, rt, cap)

	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			out, _ := mgr.Run(context.Background(),
				runedv1.RunRequest{Packages: []string{"core"}, Folder: types.Folder(fmt.Sprintf("f%d", i)), MessageBatch: "m"})
			if out.Busy {
				atomic.AddInt32(&busy, 1)
			} else {
				atomic.AddInt32(&admitted, 1)
			}
		}(i)
	}

	// Wait until the cap is saturated (all K admitted runs live), and then until
	// every other caller has actually been rejected. Both waits are on the
	// condition rather than on a clock: a claim is a round trip to the store, so
	// a fixed window can end before the callers past the cap have asked at all,
	// and releasing first hands each of them a free slot.
	saturated := time.Now().Add(10 * time.Second)
	for atomic.LoadInt32(&live) < cap {
		if time.Now().After(saturated) {
			t.Fatalf("cap never saturated: live=%d want %d", atomic.LoadInt32(&live), cap)
		}
		time.Sleep(time.Millisecond)
	}
	rejected := time.Now().Add(10 * time.Second)
	for atomic.LoadInt32(&busy) < callers-cap {
		if time.Now().After(rejected) {
			t.Fatalf("callers past the cap were never rejected: busy=%d want %d (live=%d)",
				atomic.LoadInt32(&busy), callers-cap, atomic.LoadInt32(&live))
		}
		time.Sleep(time.Millisecond)
	}
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

// TestFolderBusyReturnsBusy: a second Run for a folder with a live spawn
// returns busy — not a block, not an error, not a second spawn.
func TestFolderBusyReturnsBusy(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	var spawns atomic.Int32
	rt := FakeRuntime{Fn: func(_ context.Context, _ Turn) RunResult {
		spawns.Add(1)
		close(started)
		<-release
		return RunResult{Outcome: runedv1.OutcomeOK}
	}}
	_, mgr := newMgr(t, rt, 5)

	go func() {
		mgr.Run(context.Background(), runedv1.RunRequest{Packages: []string{"core"}, Folder: "demo", MessageBatch: "first"})
		close(done)
	}()
	<-started

	out, err := mgr.Run(context.Background(), runedv1.RunRequest{Packages: []string{"core"}, Folder: "demo", MessageBatch: "second"})
	if err != nil {
		t.Fatalf("busy reject returned err=%v want nil (busy is not an error)", err)
	}
	if !out.Busy {
		t.Fatalf("folder busy → out=%+v want Busy=true", out)
	}
	close(release)
	<-done
	if n := spawns.Load(); n != 1 {
		t.Fatalf("spawns=%d want 1 (the busy caller must not spawn a second container)", n)
	}
}

// TestActiveSpawnsPersistAcrossRestart: spawns in 'running' state are visible
// to a new manager (simulated restart), enabling correct cap enforcement and
// folder exclusivity without in-memory state.
func TestActiveSpawnsPersistAcrossRestart(t *testing.T) {
	db, _ := newMgr(t, FakeRuntime{}, 5)

	// Simulate two active spawns left by a crashed manager.
	_ = db.CreateSpawn(t.Context(), Spawn{RunID: "r1", Folder: "a", ContainerName: "c1", State: "running"})
	_ = db.CreateSpawn(t.Context(), Spawn{RunID: "r2", Folder: "b", ContainerName: "c2", State: "queued"})
	// One terminated spawn (should not count).
	_ = db.CreateSpawn(t.Context(), Spawn{RunID: "r3", Folder: "c", ContainerName: "c3", State: "exited"})

	// New manager reads state from DB.
	cfg, folders := renderWiring(t)
	mgr2 := NewManager(db, FakeRuntime{}, ManagerConfig{
		Instance: "test", MaxAgents: 3,
		Cfg: cfg, Folders: folders,
	})

	// ActiveCount should return 2 (the two live spawns).
	if got := mgr2.ActiveCount(t.Context()); got != 2 {
		t.Fatalf("ActiveCount()=%d want 2 (DB state persisted)", got)
	}

	// ActiveRunID for folder "a" should return "r1".
	if got, _ := mgr2.ActiveRunID(t.Context(), "a"); got != "r1" {
		t.Fatalf("ActiveRunID(a)=%q want r1", got)
	}

	// Folder "c" has no active spawn (it exited).
	if got, _ := mgr2.ActiveRunID(t.Context(), "c"); got != "" {
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
	rt := &killRecorder{FakeRuntime: FakeRuntime{Fn: func(_ context.Context, _ Turn) RunResult {
		if which.Add(1) == 1 {
			close(firstStarted)
			<-hold // first run hangs until killed.
			return RunResult{Outcome: runedv1.OutcomeError, Error: "killed"}
		}
		close(secondRan)
		return RunResult{Outcome: runedv1.OutcomeOK}
	}}}
	_, mgr := newMgr(t, rt, 1)

	go mgr.Run(context.Background(), runedv1.RunRequest{Packages: []string{"core"}, Folder: "demo", MessageBatch: "first"})
	<-firstStarted
	runID, _ := mgr.ActiveRunID(t.Context(), "demo")

	// At cap=1 a second folder is rejected busy — runed keeps no queue.
	out, _ := mgr.Run(context.Background(), runedv1.RunRequest{Packages: []string{"core"}, Folder: "other", MessageBatch: "second"})
	if !out.Busy {
		t.Fatalf("at cap, second folder out=%+v want Busy=true (no internal queue)", out)
	}

	// Kill the first → frees the only slot → the resubmitted batch now runs.
	if err := mgr.Kill(t.Context(), runID); err != nil {
		t.Fatalf("kill: %v", err)
	}
	go mgr.Run(context.Background(), runedv1.RunRequest{Packages: []string{"core"}, Folder: "other", MessageBatch: "second"})
	select {
	case <-secondRan:
	case <-time.After(2 * time.Second):
		t.Fatal("resubmitted run never admitted after Kill freed the slot")
	}
	close(hold) // let the first run's goroutine unwind.
}

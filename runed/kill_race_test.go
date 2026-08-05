package runed

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

// TestSpawnAbortsWhenKilledBeforeStart: a DELETE landing between admit's claim
// and StartSpawn must stop the LAUNCH, not just fix the row.
//
// The row guard alone made this worse, not better. Before it, StartSpawn had no
// WHERE and flipped 'killed' back to 'running' — DB-wrong, but the folder kept
// reading busy (ActiveSpawnForFolder counts queued/running), so nothing else
// could claim it. With the guard the row correctly stays 'killed', the folder
// reads FREE, and an unguarded exec.Run then launches a container onto a folder
// a concurrent POST /v1/runs is free to claim: two containers, one mount.
//
// Driven through Manager.spawn — the gap that let this ship is that
// TestKillSpawnDoesNotOverwriteTerminal covers only the DB layer.
func TestSpawnAbortsWhenKilledBeforeStart(t *testing.T) {
	var launches atomic.Int32
	rt := FakeRuntime{Fn: func(_ context.Context, _ RunSpec) RunResult {
		launches.Add(1)
		return RunResult{Outcome: runedv1.OutcomeOK, NewSessionID: "s"}
	}}
	db, mgr := newMgr(t, rt, 5)

	req := runedv1.RunRequest{Folder: "demo", MessageBatch: "m"}
	c, _, ok, err := mgr.admit(req, KindAgent)
	if err != nil || !ok {
		t.Fatalf("admit: ok=%v err=%v", ok, err)
	}
	if err := mgr.Kill(c.runID); err != nil {
		t.Fatalf("kill: %v", err)
	}

	// The kill is what frees the folder — the premise the launch must respect.
	if live := mgr.ActiveRunID("demo"); live != "" {
		t.Fatalf("folder still holds %q after kill; the race this guards is unreachable", live)
	}

	out := mgr.spawn(context.Background(), req, c.runID, c.sessionID, c.containerName)

	if n := launches.Load(); n != 0 {
		t.Errorf("executor ran %d time(s) for a killed run on a free folder — exclusivity violated", n)
	}
	if !out.Busy {
		t.Errorf("aborted launch outcome=%+v want Busy=true (nothing ran; routd must re-feed, not advance)", out)
	}
	// Busy's pinned contract: no run happened, so no run identity rides back.
	if out.RunID != "" || out.Outcome != "" || out.SessionID != "" {
		t.Errorf("aborted launch outcome=%+v want empty run_id/outcome/session_id", out)
	}
	if sp, _ := db.GetSpawn(c.runID); sp.State != "killed" {
		t.Errorf("spawn state=%q want killed (row resurrected)", sp.State)
	}
	// An aborted launch opened no session, so it must leave no session_log row
	// for a session nothing will ever close.
	if rows, _ := db.RecentSessions("demo", 10); len(rows) != 0 {
		t.Errorf("aborted launch left %d session_log row(s), want 0", len(rows))
	}
}

// TestKilledClaimDoesNotDoubleSpawnFolder: the consequence the abort exists to
// prevent. Killing the claim frees the folder, so a fresh POST /v1/runs is
// admitted and its container goes live — and the killed claim's spawn() then
// runs WHILE it is live. Exactly one container may exist for the folder.
//
// Asserted on total executor invocations, not a peak-concurrency gauge: a
// gauge reads 1 whenever the second launch happens to land after the first
// released, so it can pass while the invariant is broken.
func TestKilledClaimDoesNotDoubleSpawnFolder(t *testing.T) {
	var launches atomic.Int32
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	rt := FakeRuntime{Fn: func(context.Context, RunSpec) RunResult {
		launches.Add(1)
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		return RunResult{Outcome: runedv1.OutcomeOK, NewSessionID: "s"}
	}}
	_, mgr := newMgr(t, rt, 5)

	req := runedv1.RunRequest{Folder: "demo", MessageBatch: "m"}
	c, _, ok, err := mgr.admit(req, KindAgent)
	if err != nil || !ok {
		t.Fatalf("admit: ok=%v err=%v", ok, err)
	}
	if err := mgr.Kill(c.runID); err != nil {
		t.Fatalf("kill: %v", err)
	}

	// The freed folder admits a fresh run; block in its executor so its
	// container is unambiguously live for the rest of the test.
	var wg sync.WaitGroup
	var fresh runedv1.RunOutcome
	wg.Go(func() { fresh, _ = mgr.Run(context.Background(), req) })
	<-entered

	// The killed claim's launch attempt, while that container is live. Released
	// unconditionally afterwards so a launch that DOES fire returns rather than
	// deadlocking on the barrier — it is still counted, on entry.
	aborted := make(chan runedv1.RunOutcome, 1)
	wg.Go(func() {
		aborted <- mgr.spawn(context.Background(), req, c.runID, c.sessionID, c.containerName)
	})
	close(release)
	wg.Wait()

	if out := <-aborted; !out.Busy {
		t.Errorf("killed claim launched onto a folder with a live container: outcome=%+v", out)
	}

	if n := launches.Load(); n != 1 {
		t.Errorf("executor invocations for one folder=%d want 1 (folder-exclusivity)", n)
	}
	if fresh.Busy {
		t.Errorf("fresh run after kill outcome=%+v want admitted — the race needs a free folder", fresh)
	}
}

// TestStartSpawnReportsWhetherItFired: the rows-affected report spawn() gates
// on. A queued row is claimable exactly once; a killed one never is.
func TestStartSpawnReportsWhetherItFired(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_ = db.CreateSpawn(Spawn{RunID: "r1", Folder: "demo", ContainerName: "c1", State: "queued"})
	started, err := db.StartSpawn("r1", "s")
	if err != nil || !started {
		t.Fatalf("StartSpawn on queued = (%v, %v) want (true, nil)", started, err)
	}
	// Already running: the claim was taken, so a second start must not re-fire.
	if started, err := db.StartSpawn("r1", "s"); err != nil || started {
		t.Errorf("StartSpawn on running = (%v, %v) want (false, nil)", started, err)
	}

	_ = db.CreateSpawn(Spawn{RunID: "r2", Folder: "demo2", ContainerName: "c2", State: "queued"})
	if err := db.KillSpawn("r2"); err != nil {
		t.Fatalf("kill spawn: %v", err)
	}
	if started, err := db.StartSpawn("r2", "s"); err != nil || started {
		t.Errorf("StartSpawn on killed = (%v, %v) want (false, nil)", started, err)
	}
}

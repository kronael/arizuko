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

func newMgr(t *testing.T, rt Runtime, max int) (*DB, *Manager) {
	t.Helper()
	db, err := OpenMem()
	if err != nil {
		t.Fatalf("open mem db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	mgr := NewManager(db, rt, NewStaticBroker("jws", "jti"), ManagerConfig{
		Scopes:        []types.Scope{"messages:send:own_group", "chats:read:own_group"},
		Instance:      "test",
		MaxConcurrent: max,
	})
	return db, mgr
}

// TestSerializationNoConcurrentDoubleSpawn: many concurrent Run for ONE idle
// folder never run two containers at once in the shared workspace — the
// steer-check + live-run registration are one locked critical section
// (spec 5/P § per-folder serialization, folder-exclusivity). Without steer
// wired, the losers queue and run serially (never concurrently).
func TestSerializationNoConcurrentDoubleSpawn(t *testing.T) {
	var live, peak int32
	var mu sync.Mutex
	rt := FakeRuntime{Fn: func(ctx context.Context, spec RunSpec) RunResult {
		n := atomic.AddInt32(&live, 1)
		mu.Lock()
		if n > peak {
			peak = n
		}
		mu.Unlock()
		time.Sleep(5 * time.Millisecond) // hold the slot to expose any overlap
		atomic.AddInt32(&live, -1)
		return RunResult{Outcome: runedv1.OutcomeOK, NewSessionID: "s"}
	}}
	_, mgr := newMgr(t, rt, 5)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mgr.Run(context.Background(), runedv1.RunRequest{Folder: "demo", MessageBatch: "m"})
		}()
	}
	wg.Wait()
	if peak != 1 {
		t.Fatalf("peak concurrent containers for one folder=%d want 1 (folder-exclusivity)", peak)
	}
	if c := mgr.ActiveCount(); c != 0 {
		t.Fatalf("active count after drain=%d want 0", c)
	}
}

// TestSteerWhenLive: a second Run for a folder that is already running, with
// the steer callback wired (as the prod Runtime does), steers into the live
// container and returns steered:true — no second spawn.
func TestSteerWhenLive(t *testing.T) {
	var spawns int32
	wired := make(chan struct{})
	release := make(chan struct{})
	rt := FakeRuntime{Fn: func(ctx context.Context, spec RunSpec) RunResult {
		atomic.AddInt32(&spawns, 1)
		spec.RegisterSteer(func(string) bool { return true })
		close(wired)
		<-release
		return RunResult{Outcome: runedv1.OutcomeOK, NewSessionID: "s1"}
	}}
	_, mgr := newMgr(t, rt, 5)

	done := make(chan struct{})
	go func() {
		mgr.Run(context.Background(), runedv1.RunRequest{Folder: "demo", MessageBatch: "first"})
		close(done)
	}()
	<-wired // first run is live and its steer callback is registered.

	out, _ := mgr.Run(context.Background(), runedv1.RunRequest{Folder: "demo", MessageBatch: "second"})
	if !out.Steered {
		t.Fatalf("second Run steered=%v want true (out=%+v)", out.Steered, out)
	}
	close(release)
	<-done
	if n := atomic.LoadInt32(&spawns); n != 1 {
		t.Fatalf("spawns=%d want exactly 1 container for one busy folder", n)
	}
}

// TestFreshRunSteerSeesResolvedSessionID: for a FRESH run (empty
// req.SessionID), a concurrent Run that steers into the live spawn must see
// the resolved (minted) session id on the ack — not the empty req.SessionID
// (Bug 6 — the slot used to store req.SessionID, registered BEFORE spawn
// minted the id, so a racing steer reported steered:true with an empty id).
func TestFreshRunSteerSeesResolvedSessionID(t *testing.T) {
	wired := make(chan struct{})
	release := make(chan struct{})
	rt := FakeRuntime{Fn: func(ctx context.Context, spec RunSpec) RunResult {
		spec.RegisterSteer(func(string) bool { return true })
		close(wired)
		<-release
		return RunResult{Outcome: runedv1.OutcomeOK, NewSessionID: "minted-by-runtime"}
	}}
	_, mgr := newMgr(t, rt, 5)

	done := make(chan struct{})
	go func() {
		// fresh run: no SessionID — the Manager must mint one before registering.
		mgr.Run(context.Background(), runedv1.RunRequest{Folder: "demo", MessageBatch: "first"})
		close(done)
	}()
	<-wired

	out, _ := mgr.Run(context.Background(), runedv1.RunRequest{Folder: "demo", MessageBatch: "second"})
	if !out.Steered {
		t.Fatalf("second Run steered=%v want true", out.Steered)
	}
	if out.SessionID == "" {
		t.Fatalf("steered ack carries empty session id — slot registered before spawn resolved one (Bug 6)")
	}
	close(release)
	<-done
}

// TestConcurrencyCap: with MaxConcurrent=2 and 3 distinct busy folders, at
// most 2 containers run at once; the 3rd waits for a slot (spec 5/P §
// MAX_CONCURRENT cap + waiting queue).
func TestConcurrencyCap(t *testing.T) {
	var live, peak int32
	release := make(chan struct{})
	var mu sync.Mutex
	rt := FakeRuntime{Fn: func(ctx context.Context, spec RunSpec) RunResult {
		n := atomic.AddInt32(&live, 1)
		mu.Lock()
		if n > peak {
			peak = n
		}
		mu.Unlock()
		<-release
		atomic.AddInt32(&live, -1)
		return RunResult{Outcome: runedv1.OutcomeOK}
	}}
	_, mgr := newMgr(t, rt, 2)

	var wg sync.WaitGroup
	for _, f := range []string{"a", "b", "c"} {
		wg.Add(1)
		go func(folder string) {
			defer wg.Done()
			mgr.Run(context.Background(), runedv1.RunRequest{Folder: types.Folder(folder), MessageBatch: "m"})
		}(f)
	}
	// let the cap fill, then confirm the third is blocked (live never > cap).
	deadline := time.After(2 * time.Second)
	for atomic.LoadInt32(&live) < 2 {
		select {
		case <-deadline:
			t.Fatal("cap never reached 2")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	time.Sleep(50 * time.Millisecond) // window for an over-cap 3rd to (wrongly) start
	if l := atomic.LoadInt32(&live); l > 2 {
		t.Fatalf("live=%d exceeded cap 2", l)
	}
	close(release)
	wg.Wait()
	if peak > 2 {
		t.Fatalf("peak concurrency=%d exceeded cap 2", peak)
	}
}

// TestCircuitBreakerTripAndReset: 3 consecutive failures trip the breaker on
// the 3rd run (which DID run); a new inbound resets and retries successfully
// (spec 5/P § circuit breaker).
func TestCircuitBreakerTripAndReset(t *testing.T) {
	var calls int32
	failNext := atomic.Bool{}
	failNext.Store(true)
	rt := FakeRuntime{Fn: func(ctx context.Context, spec RunSpec) RunResult {
		atomic.AddInt32(&calls, 1)
		if failNext.Load() {
			return RunResult{Outcome: runedv1.OutcomeError, Error: "boom"}
		}
		return RunResult{Outcome: runedv1.OutcomeOK, NewSessionID: "ok"}
	}}
	_, mgr := newMgr(t, rt, 5)

	for i := 1; i <= 3; i++ {
		out, _ := mgr.Run(context.Background(), runedv1.RunRequest{Folder: "demo", MessageBatch: "m"})
		if out.Outcome != runedv1.OutcomeError {
			t.Fatalf("run %d outcome=%q want error", i, out.Outcome)
		}
		if want := i == 3; out.BreakerOpen != want {
			t.Fatalf("run %d breaker_open=%v want %v (trips only on the 3rd)", i, out.BreakerOpen, want)
		}
	}
	if atomic.LoadInt32(&calls) != 3 {
		t.Fatalf("calls=%d want 3 (the tripping run actually ran)", calls)
	}

	// new inbound resets the breaker and retries; this one succeeds.
	failNext.Store(false)
	out, _ := mgr.Run(context.Background(), runedv1.RunRequest{Folder: "demo", MessageBatch: "m"})
	if out.Outcome != runedv1.OutcomeOK || out.BreakerOpen {
		t.Fatalf("post-reset run outcome=%q breaker_open=%v want ok,false", out.Outcome, out.BreakerOpen)
	}
	if atomic.LoadInt32(&calls) != 4 {
		t.Fatalf("calls=%d want 4 (reset retried the run)", calls)
	}
}

// TestIntersectFailClosed: empty or disjoint requested scope yields the
// EMPTY brokered scope, never the full ceiling (spec 5/P § brokering;
// bugs.md should-fix fail-open).
func TestIntersectFailClosed(t *testing.T) {
	ceiling := []types.Scope{"messages:send:own_group", "chats:read:own_group"}
	if got := intersect(ceiling, nil); len(got) != 0 {
		t.Fatalf("empty want → %v, want empty (fail closed, not full ceiling)", got)
	}
	if got := intersect(ceiling, []types.Scope{"admin:everything"}); len(got) != 0 {
		t.Fatalf("disjoint want → %v, want empty", got)
	}
	got := intersect(ceiling, []types.Scope{"chats:read:own_group", "admin:everything"})
	if len(got) != 1 || got[0] != "chats:read:own_group" {
		t.Fatalf("overlap want → %v, want [chats:read:own_group]", got)
	}
}

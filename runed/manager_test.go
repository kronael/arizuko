package runed

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
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
	cfg, folders := renderWiring(t)
	mgr := NewManager(db, rt, ManagerConfig{
		Instance:      "test",
		MaxConcurrent: max,
		Cfg:           cfg, Folders: folders,
	})
	return db, mgr
}

// TestSerializationNoConcurrentDoubleSpawn: many concurrent Run for ONE idle
// folder never run two containers at once in the shared workspace — the
// steer-check + spawn-row claim are one locked critical section (spec 5/P-runed §
// per-folder serialization, folder-exclusivity). Without steer wired, the
// losers are rejected busy (no internal queue); at most one runs at a time.
func TestSerializationNoConcurrentDoubleSpawn(t *testing.T) {
	var live, peak int32
	var mu sync.Mutex
	rt := FakeRuntime{Fn: func(ctx context.Context, turn Turn) RunResult {
		n := atomic.AddInt32(&live, 1)
		mu.Lock()
		if n > peak {
			peak = n
		}
		mu.Unlock()
		time.Sleep(5 * time.Millisecond) // hold the slot to expose any overlap
		atomic.AddInt32(&live, -1)
		return RunResult{Outcome: runedv1.OutcomeOK}
	}}
	_, mgr := newMgr(t, rt, 5)

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			mgr.Run(context.Background(), runedv1.RunRequest{Packages: []string{"core"}, Folder: "demo", MessageBatch: "m"})
		})
	}
	wg.Wait()
	if peak != 1 {
		t.Fatalf("peak concurrent containers for one folder=%d want 1 (folder-exclusivity)", peak)
	}
	if c := mgr.ActiveCount(t.Context()); c != 0 {
		t.Fatalf("active count after drain=%d want 0", c)
	}
}

// A fresh turn waits for the active run, then starts under its own identity.
func TestFollowupWaitsForActiveRun(t *testing.T) {
	var spawns atomic.Int32
	var steers atomic.Int32
	wired := make(chan struct{})
	release := make(chan struct{})
	rt := FakeRuntime{Fn: func(ctx context.Context, turn Turn) RunResult {
		if spawns.Add(1) == 1 {
			turn.RegisterSteer(func(string) bool { steers.Add(1); return true })
			close(wired)
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
		return RunResult{Outcome: runedv1.OutcomeOK}
	}}
	_, mgr := newMgr(t, rt, 5)

	done := make(chan struct{})
	go func() {
		mgr.Run(t.Context(), runedv1.RunRequest{Packages: []string{"core"}, Folder: "demo", TurnID: "first", MessageBatch: "first"})
		close(done)
	}()
	<-wired

	out, err := mgr.Run(t.Context(), runedv1.RunRequest{Packages: []string{"core"}, Folder: "demo", TurnID: "second", MessageBatch: "second"})
	close(release)
	<-done
	if err != nil || !out.Busy || out.Steered || out.RunID != "" || steers.Load() != 0 {
		t.Fatalf("fresh turn entered the active run: out=%+v err=%v steers=%d", out, err, steers.Load())
	}
	out, err = mgr.Run(t.Context(), runedv1.RunRequest{Packages: []string{"core"}, Folder: "demo", TurnID: "second", MessageBatch: "second"})
	if err != nil || out.Busy || out.Steered || out.RunID == "" || spawns.Load() != 2 {
		t.Fatalf("follow-up did not start after release: out=%+v err=%v spawns=%d", out, err, spawns.Load())
	}
}

// TestConcurrencyCap: with MaxConcurrent=2 and 3 distinct busy folders, at
// most 2 containers run at once; the 3rd is rejected busy (spec 5/P-runed §
// MAX_CONCURRENT cap — no wait, no internal queue).
func TestConcurrencyCap(t *testing.T) {
	var live, peak int32
	release := make(chan struct{})
	var mu sync.Mutex
	rt := FakeRuntime{Fn: func(ctx context.Context, turn Turn) RunResult {
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
			mgr.Run(context.Background(), runedv1.RunRequest{Packages: []string{"core"}, Folder: types.Folder(folder), MessageBatch: "m"})
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
// (spec 5/P-runed § circuit breaker).
func TestCircuitBreakerTripAndReset(t *testing.T) {
	var calls int32
	failNext := atomic.Bool{}
	failNext.Store(true)
	rt := FakeRuntime{Fn: func(ctx context.Context, turn Turn) RunResult {
		atomic.AddInt32(&calls, 1)
		if failNext.Load() {
			return RunResult{Outcome: runedv1.OutcomeError, Error: "boom"}
		}
		return RunResult{Outcome: runedv1.OutcomeOK}
	}}
	_, mgr := newMgr(t, rt, 5)

	for i := 1; i <= 3; i++ {
		out, _ := mgr.Run(context.Background(), runedv1.RunRequest{Packages: []string{"core"}, Folder: "demo", MessageBatch: "m"})
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
	out, _ := mgr.Run(context.Background(), runedv1.RunRequest{Packages: []string{"core"}, Folder: "demo", MessageBatch: "m"})
	if out.Outcome != runedv1.OutcomeOK || out.BreakerOpen {
		t.Fatalf("post-reset run outcome=%q breaker_open=%v want ok,false", out.Outcome, out.BreakerOpen)
	}
	if atomic.LoadInt32(&calls) != 4 {
		t.Fatalf("calls=%d want 4 (reset retried the run)", calls)
	}
}

// TestRunTTLBecomesTheCtxDeadline: m.runTTL is the run ceiling and it reaches
// the executor as ctx's deadline — the ONE deadline of spec 6/41 §"The seam".
// A substrate with a timer of its own reads it from there, so nothing on the
// port carries a duration. TestManagerAppliesRunTTLDeadline covers the kill the
// elapsed deadline causes; TestDockerCtxDeadlineKills covers the substrate's
// side of honouring it.
func TestRunTTLBecomesTheCtxDeadline(t *testing.T) {
	var gotTTL atomic.Int64
	rt := FakeRuntime{Fn: func(ctx context.Context, turn Turn) RunResult {
		if dl, ok := ctx.Deadline(); ok {
			gotTTL.Store(int64(time.Until(dl)))
		}
		return RunResult{Outcome: runedv1.OutcomeOK}
	}}
	db, err := OpenMem()
	if err != nil {
		t.Fatalf("open mem db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	cfg, folders := renderWiring(t)
	mgr := NewManager(db, rt, ManagerConfig{
		Instance: "test", MaxConcurrent: 5, RunTTL: 20 * time.Millisecond,
		Cfg: cfg, Folders: folders,
	})

	mgr.Run(context.Background(), runedv1.RunRequest{Packages: []string{"core"}, Folder: "demo", MessageBatch: "m"})

	got := time.Duration(gotTTL.Load())
	if got <= 0 || got > 20*time.Millisecond {
		t.Fatalf("ctx deadline left %s, want just under the 20ms ceiling", got)
	}
}

// TestTurnTTLFoldsTheGuestCeiling pins the one deadline's arithmetic: the run
// ceiling, lowered by the group's container_config Timeout over the instance
// default, and floored at the idle timeout plus its grace so the deadline can
// never cut in before the idle timer has given up (spec 6/41 §"The seam" —
// container_config Timeout is an input to the deadline, never a second timer).
func TestTurnTTLFoldsTheGuestCeiling(t *testing.T) {
	cases := []struct {
		name         string
		runTTL       time.Duration
		cfgTimeout   time.Duration
		idle         time.Duration
		groupTimeout time.Duration
		want         time.Duration
	}{
		{"instance ceiling above the run ceiling", 20 * time.Minute, 60 * time.Minute, 60 * time.Minute, 0, 20 * time.Minute},
		{"instance ceiling below the run ceiling", 20 * time.Minute, 10 * time.Minute, time.Minute, 0, 10 * time.Minute},
		{"the group's own ceiling wins", 20 * time.Minute, 10 * time.Minute, time.Minute, 5 * time.Minute, 5 * time.Minute},
		{"floored at idle + grace", 20 * time.Minute, 10 * time.Minute, 10 * time.Minute, 5 * time.Minute, 10*time.Minute + 30*time.Second},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := &Manager{runTTL: c.runTTL, cfg: &core.Config{Timeout: c.cfgTimeout, IdleTimeout: c.idle}}
			req := runedv1.RunRequest{Packages: []string{"core"}, Folder: "demo"}
			if c.groupTimeout > 0 {
				req.ContainerConfig = map[string]any{"Timeout": int64(c.groupTimeout)}
			}
			if got := m.turnTTL(req); got != c.want {
				t.Fatalf("turnTTL=%s want %s", got, c.want)
			}
		})
	}
}

// TestManagerAppliesRunTTLDeadline: spec 5/8 "wedge protection... uniform
// across kinds" — Manager.spawn arms the RunTTL kill-deadline by wrapping
// ctx with context.WithTimeout at the shared dispatch site, not inside any
// one executor. This exercises a minimal Runtime double that (like
// dockerRuntime's armCancel) does nothing but honor ctx.Done(), proving the
// deadline actually reaches the executor through Manager — not just that
// Turn.RunTTL carries the raw value (TestRunTTLThreadedToRuntime).
func TestManagerAppliesRunTTLDeadline(t *testing.T) {
	rt := ctxWatchingRuntime{}
	db, err := OpenMem()
	if err != nil {
		t.Fatalf("open mem db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	cfg, folders := renderWiring(t)
	mgr := NewManager(db, rt, ManagerConfig{
		Instance: "test", MaxConcurrent: 5, RunTTL: 20 * time.Millisecond,
		Cfg: cfg, Folders: folders,
	})

	out, _ := mgr.Run(context.Background(), runedv1.RunRequest{Packages: []string{"core"}, Folder: "demo", MessageBatch: "m"})
	if out.Outcome != runedv1.OutcomeError {
		t.Fatalf("outcome=%q want error (ctx deadline elapsed mid-run)", out.Outcome)
	}
}

// TestTerminalOnWire: Manager.spawn copies RunResult.Terminal onto the
// pinned RunOutcome, so routd receives the retry decision, not the raw
// exit code (spec 5/12, BUGS F73).
func TestTerminalOnWire(t *testing.T) {
	rt := FakeRuntime{Fn: func(context.Context, Turn) RunResult {
		return RunResult{
			Outcome: runedv1.OutcomeError, Terminal: true,
			Error: "Container exited with code 125: pull access denied",
		}
	}}
	_, mgr := newMgr(t, rt, 5)

	out, err := mgr.Run(context.Background(), runedv1.RunRequest{Packages: []string{"core"}, Folder: "demo", MessageBatch: "m"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !out.Terminal {
		t.Fatalf("RunOutcome.Terminal=false want true (out=%+v)", out)
	}
}

// ctxWatchingRuntime blocks until ctx.Done(), mirroring dockerRuntime's
// armCancel contract (kill/abort on ctx death) without any docker/container
// machinery — used to prove Manager wires the RunTTL deadline onto ctx.
type ctxWatchingRuntime struct{}

func (ctxWatchingRuntime) Run(ctx context.Context, _ Turn) RunResult {
	<-ctx.Done()
	return RunResult{Outcome: runedv1.OutcomeError, Error: ctx.Err().Error()}
}
func (ctxWatchingRuntime) Kill(string) error { return nil }

// TestBreakerResetsOnSilent: gated resets the breaker on ANY clean exit
// (runForGroup success=true), silent included. A folder alternating
// error/silent must never creep to the threshold (gap 2 — runed used to
// reset only on OutcomeOK, so silent left failures>0 and the next error
// could trip).
func TestBreakerResetsOnSilent(t *testing.T) {
	var outcome atomic.Value
	outcome.Store(runedv1.OutcomeError)
	rt := FakeRuntime{Fn: func(ctx context.Context, turn Turn) RunResult {
		return RunResult{Outcome: outcome.Load().(string)}
	}}
	_, mgr := newMgr(t, rt, 5)

	// Two errors, then a silent run, then a third error: without the silent
	// reset this would be the 3rd consecutive failure and trip.
	for _, oc := range []string{runedv1.OutcomeError, runedv1.OutcomeError, runedv1.OutcomeSilent} {
		outcome.Store(oc)
		out, _ := mgr.Run(context.Background(), runedv1.RunRequest{Packages: []string{"core"}, Folder: "demo", MessageBatch: "m"})
		if out.BreakerOpen {
			t.Fatalf("outcome=%q tripped breaker early (silent must reset)", oc)
		}
	}
	outcome.Store(runedv1.OutcomeError)
	out, _ := mgr.Run(context.Background(), runedv1.RunRequest{Packages: []string{"core"}, Folder: "demo", MessageBatch: "m"})
	if out.BreakerOpen {
		t.Fatalf("breaker tripped after a silent reset cleared the failure count")
	}
}

func TestKillDoesNotRelabelCompletedRun(t *testing.T) {
	db, mgr := newMgr(t, FakeRuntime{}, 5)
	_ = db.CreateSpawn(t.Context(), Spawn{RunID: "r", Folder: "demo", ContainerName: "c", State: "queued"})
	_, _ = db.StartSpawn(t.Context(), "r", "s")
	if err := db.EndSpawn(t.Context(), "r", "exited", "ok", 0); err != nil {
		t.Fatalf("end spawn: %v", err)
	}
	if err := mgr.Kill(t.Context(), "r"); err != nil {
		t.Fatalf("kill: %v", err)
	}
	if sp, _ := db.SpawnByRunID(t.Context(), "r"); sp.State != "exited" || sp.Outcome != "ok" {
		t.Fatalf("Kill relabeled a completed run: state=%q outcome=%q want exited/ok", sp.State, sp.Outcome)
	}
}

// TestIsolatedRunNoSessionLineage: isolated (timed-isolated:*) runs are
// one-off containers — runed must NOT persist session lineage (no session_log
// row), matching gated's `if !isolated` guards. A non-isolated run DOES record
// one (gap 4).
func TestIsolatedRunNoSessionLineage(t *testing.T) {
	rt := FakeRuntime{Fn: func(context.Context, Turn) RunResult {
		return RunResult{Outcome: runedv1.OutcomeOK}
	}}
	db, mgr := newMgr(t, rt, 5)

	out, _ := mgr.Run(context.Background(), runedv1.RunRequest{Packages: []string{"core"}, Folder: "demo", MessageBatch: "m", Isolated: true})
	if out.Outcome != runedv1.OutcomeOK {
		t.Fatalf("isolated run outcome=%q want ok", out.Outcome)
	}
	if rows, _ := db.RecentSessions(t.Context(), "demo", 10); len(rows) != 0 {
		t.Fatalf("isolated run wrote %d session_log rows, want 0 (no lineage)", len(rows))
	}
	// the spawns row still exists for GET/DELETE, with no session_log link.
	sp, err := db.SpawnByRunID(t.Context(), out.RunID)
	if err != nil {
		t.Fatalf("GET isolated run: %v", err)
	}
	if sp.SessionLogID != "" {
		t.Fatalf("isolated spawn session_log_id=%q want empty", sp.SessionLogID)
	}

	// a NON-isolated run does record a session_log row.
	mgr.Run(context.Background(), runedv1.RunRequest{Packages: []string{"core"}, Folder: "demo", MessageBatch: "m"})
	if rows, _ := db.RecentSessions(t.Context(), "demo", 10); len(rows) != 1 {
		t.Fatalf("non-isolated run wrote %d session_log rows, want 1", len(rows))
	}
}

// TestTrippingRunEmitsBreakerOpen: the run that pushes a folder to the failure
// threshold returns BreakerOpen=true on its synchronous response (gap 5 —
// routd keys the user-notification + session-delete on this signal).
func TestTrippingRunEmitsBreakerOpen(t *testing.T) {
	rt := FakeRuntime{Fn: func(context.Context, Turn) RunResult {
		return RunResult{Outcome: runedv1.OutcomeError, Error: "boom"}
	}}
	_, mgr := newMgr(t, rt, 5)

	for i := 1; i <= circuitBreakerThreshold; i++ {
		out, _ := mgr.Run(context.Background(), runedv1.RunRequest{Packages: []string{"core"}, Folder: "demo", MessageBatch: "m"})
		want := i == circuitBreakerThreshold
		if out.BreakerOpen != want {
			t.Fatalf("run %d breaker_open=%v want %v (trips only on the threshold run)", i, out.BreakerOpen, want)
		}
		if want && out.Error == "" {
			t.Fatalf("tripping run carries empty error; want a breaker message")
		}
	}
}

// TestGatewaySpawnWithoutAKeyIsRefused: a gateway instance withholds the model
// credential from the guest (spawnSecrets), so a turn with no minted virtual key
// would reach no model at all and read as a harness fault. The refusal is
// terminal — the only way here is a routd that did not mint, which a retry
// cannot repair — and nothing is started.
func TestGatewaySpawnWithoutAKeyIsRefused(t *testing.T) {
	var ran atomic.Bool
	rt := FakeRuntime{Fn: func(context.Context, Turn) RunResult {
		ran.Store(true)
		return RunResult{Outcome: runedv1.OutcomeOK}
	}}
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	cfg, folders := renderWiring(t)
	cfg.ModelGatewayURL = "http://litellm:4000"
	mgr := NewManager(db, rt, ManagerConfig{
		Instance: "test", MaxConcurrent: 5,
		Cfg: cfg, Folders: folders,
	})

	out, _ := mgr.Run(context.Background(), runedv1.RunRequest{Packages: []string{"core"}, Folder: "demo", MessageBatch: "m"})
	if out.Outcome != runedv1.OutcomeError || !out.Terminal {
		t.Fatalf("outcome=%q terminal=%v want a terminal error", out.Outcome, out.Terminal)
	}
	if !strings.Contains(out.Error, "no virtual key") {
		t.Errorf("error=%q — it must name the missing minted key", out.Error)
	}
	if ran.Load() {
		t.Error("the runtime ran on a turn that can reach no model")
	}
	// With the key, the same request runs.
	out, _ = mgr.Run(context.Background(), runedv1.RunRequest{Packages: []string{"core"}, Folder: "demo", MessageBatch: "m", ModelGatewayKey: "sk-virtual"})
	if out.Outcome != runedv1.OutcomeOK || !ran.Load() {
		t.Fatalf("a minted key must run: outcome=%q ran=%v", out.Outcome, ran.Load())
	}
}

// TestAgentRunWithoutARendererIsRefused: with no renderer wiring there is no
// envelope, and a guest started without one would run with no mounts and no
// prompt. The refusal is loud and terminal rather than a bare Turn.
func TestAgentRunWithoutARendererIsRefused(t *testing.T) {
	var ran atomic.Bool
	rt := FakeRuntime{Fn: func(context.Context, Turn) RunResult {
		ran.Store(true)
		return RunResult{Outcome: runedv1.OutcomeOK}
	}}
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	mgr := NewManager(db, rt, ManagerConfig{Instance: "test", MaxConcurrent: 5})

	out, _ := mgr.Run(context.Background(), runedv1.RunRequest{Packages: []string{"core"}, Folder: "demo", MessageBatch: "m"})
	if out.Outcome != runedv1.OutcomeError || !out.Terminal {
		t.Fatalf("outcome=%q terminal=%v want a terminal error", out.Outcome, out.Terminal)
	}
	if ran.Load() {
		t.Error("the runtime ran with no envelope to deliver")
	}
}

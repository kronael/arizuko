package runed

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/types"
)

// newHoldMgr is newMgr with the KindHold executor registered — the exact
// wiring runed/cmd/runed/main.go does.
func newHoldMgr(t *testing.T, rt Runtime, max int, ttl time.Duration) (*DB, *Manager) {
	t.Helper()
	db, err := OpenMem()
	if err != nil {
		t.Fatalf("open mem db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	mgr := NewManager(db, rt, ManagerConfig{
		Instance: "test", MaxConcurrent: max, RunTTL: ttl,
	})
	mgr.RegisterExecutor(KindHold, NewHoldRuntime())
	return db, mgr
}

// waitSpawnState polls until run's spawns row reaches a terminal state, so a
// test never races the detached goroutine Hold() launches.
func waitSpawnState(t *testing.T, db *DB, runID string, want ...string) Spawn {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var sp Spawn
	for time.Now().Before(deadline) {
		var err error
		sp, err = db.GetSpawn(runID)
		if err != nil {
			t.Fatalf("get spawn %s: %v", runID, err)
		}
		for _, w := range want {
			if sp.State == w {
				return sp
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("spawn %s state=%q never reached %v", runID, sp.State, want)
	return sp
}

// TestHoldBlocksAgentTurn: the whole point — while a hold stands, an agent
// turn for that folder is NOT admitted. It gets Busy (routd re-feeds from its
// own queue) rather than being steered, because a hold registers no steer
// callback: nothing can be written into a run that is not a container.
func TestHoldBlocksAgentTurn(t *testing.T) {
	var ran bool
	rt := FakeRuntime{Fn: func(context.Context, RunSpec) RunResult {
		ran = true
		return RunResult{Outcome: runedv1.OutcomeOK}
	}}
	_, mgr := newHoldMgr(t, rt, 5, time.Minute)

	held, err := mgr.Hold(context.Background(), types.Folder("demo"), "restore")
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	if held.RunID == "" || held.Busy {
		t.Fatalf("hold outcome=%+v want a run_id and busy=false", held)
	}

	out, err := mgr.Run(context.Background(), runedv1.RunRequest{Folder: "demo", MessageBatch: "hi"})
	if err != nil {
		t.Fatalf("run during hold: %v", err)
	}
	if !out.Busy {
		t.Errorf("agent turn outcome=%+v want busy=true (folder is held)", out)
	}
	if out.Steered {
		t.Error("agent turn was steered into a hold — a hold has no container to steer")
	}
	if ran {
		t.Error("agent runtime ran while the folder was held")
	}
}

// TestHoldReleaseUnblocks: releasing frees the slot, and the next agent turn
// is admitted normally. Release is DELETE /v1/runs/{run_id} → Manager.Kill,
// which dispatches to the hold executor by the spawn's recorded kind — no
// release-specific path.
func TestHoldReleaseUnblocks(t *testing.T) {
	ran := make(chan struct{}, 1)
	rt := FakeRuntime{Fn: func(context.Context, RunSpec) RunResult {
		ran <- struct{}{}
		return RunResult{Outcome: runedv1.OutcomeOK}
	}}
	db, mgr := newHoldMgr(t, rt, 5, time.Minute)

	held, err := mgr.Hold(context.Background(), types.Folder("demo"), "restore")
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	if err := mgr.Kill(held.RunID); err != nil {
		t.Fatalf("release: %v", err)
	}
	waitSpawnState(t, db, held.RunID, "killed", "exited")

	out, err := mgr.Run(context.Background(), runedv1.RunRequest{Folder: "demo", MessageBatch: "hi"})
	if err != nil {
		t.Fatalf("run after release: %v", err)
	}
	if out.Busy {
		t.Fatalf("agent turn still busy after release: %+v", out)
	}
	select {
	case <-ran:
	default:
		t.Error("agent runtime never ran after the hold was released")
	}
}

// TestHoldExpiresOnRunTTL: wedge protection. A holder that dies without
// releasing must not hold the folder forever — RunTTL expires the hold and
// the folder frees itself. This is the property that made a lease table
// unnecessary, so it is asserted end-to-end (the slot is actually reusable),
// not just by reading the spawn state.
func TestHoldExpiresOnRunTTL(t *testing.T) {
	db, mgr := newHoldMgr(t, FakeRuntime{}, 5, 30*time.Millisecond)

	held, err := mgr.Hold(context.Background(), types.Folder("demo"), "vanishing holder")
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	// Holder vanishes: no Kill, no release — only the TTL is left.
	sp := waitSpawnState(t, db, held.RunID, "error", "exited")
	if sp.State != "error" {
		t.Errorf("expired hold state=%q want error (an unreleased hold is a failure, and must be visible as one)", sp.State)
	}

	out, err := mgr.Run(context.Background(), runedv1.RunRequest{Folder: "demo", MessageBatch: "hi"})
	if err != nil {
		t.Fatalf("run after expiry: %v", err)
	}
	if out.Busy {
		t.Fatal("folder still wedged after the hold's RunTTL expired")
	}
}

// TestSecondHoldRefused: per-folder exclusion applies between holds too, not
// only hold-vs-agent. The second caller gets Busy and must not proceed.
func TestSecondHoldRefused(t *testing.T) {
	_, mgr := newHoldMgr(t, FakeRuntime{}, 5, time.Minute)

	first, err := mgr.Hold(context.Background(), types.Folder("demo"), "restore")
	if err != nil {
		t.Fatalf("first hold: %v", err)
	}
	second, err := mgr.Hold(context.Background(), types.Folder("demo"), "vacuum")
	if err != nil {
		t.Fatalf("second hold: %v", err)
	}
	if !second.Busy {
		t.Errorf("second hold outcome=%+v want busy=true", second)
	}
	if second.RunID != "" {
		t.Errorf("busy hold returned run_id %q — a rejected caller must get no handle", second.RunID)
	}
	if second.RunID == first.RunID && first.RunID != "" {
		t.Error("second hold handed back the first hold's handle")
	}

	// A hold on a DIFFERENT folder is unaffected — exclusion is per folder.
	other, err := mgr.Hold(context.Background(), types.Folder("other"), "restore")
	if err != nil {
		t.Fatalf("hold on other folder: %v", err)
	}
	if other.Busy || other.RunID == "" {
		t.Errorf("hold on a different folder outcome=%+v want admitted", other)
	}
}

// TestHoldDoesNotTripBreaker: the circuit breaker is agent-only accounting.
// A hold that ends in outcome=error (an expired one) must not add to the
// folder's failure streak — three expired holds would otherwise open the
// breaker and stop the agent spawning for a folder that never failed a turn.
func TestHoldDoesNotTripBreaker(t *testing.T) {
	db, mgr := newHoldMgr(t, FakeRuntime{}, 5, 20*time.Millisecond)

	for i := 0; i < circuitBreakerThreshold; i++ {
		held, err := mgr.Hold(context.Background(), types.Folder("demo"), "expiring hold")
		if err != nil {
			t.Fatalf("hold %d: %v", i, err)
		}
		waitSpawnState(t, db, held.RunID, "error", "exited")
	}

	if failures, err := db.GetFailures("demo"); err != nil || failures != 0 {
		t.Fatalf("failures=%d err=%v after %d expired holds; want 0 (breaker is agent-only)",
			failures, err, circuitBreakerThreshold)
	}
	out, err := mgr.Run(context.Background(), runedv1.RunRequest{Folder: "demo", MessageBatch: "hi"})
	if err != nil {
		t.Fatalf("run after expired holds: %v", err)
	}
	if out.BreakerOpen {
		t.Error("expired holds opened the agent's circuit breaker")
	}
}

// TestHoldDoesNotConsumeContainerCap: MAX_CONCURRENT_CONTAINERS bounds
// container/host resources, which a containerless hold does not consume.
// Holds on other folders must never make an agent turn 503-busy on capacity
// it is not using. maxRun=1 makes the accounting unambiguous.
func TestHoldDoesNotConsumeContainerCap(t *testing.T) {
	rt := FakeRuntime{Fn: func(context.Context, RunSpec) RunResult {
		return RunResult{Outcome: runedv1.OutcomeOK}
	}}
	db, mgr := newHoldMgr(t, rt, 1, time.Minute)

	for _, f := range []string{"held-a", "held-b", "held-c"} {
		out, err := mgr.Hold(context.Background(), types.Folder(f), "restore")
		if err != nil {
			t.Fatalf("hold %s: %v", f, err)
		}
		if out.Busy {
			t.Fatalf("hold on %s rejected busy — holds are not capped by MaxConcurrent", f)
		}
	}

	// Three live spawns, but zero live AGENT spawns: the cap's budget is free.
	if n, err := db.ActiveCount(); err != nil || n != 3 {
		t.Fatalf("ActiveCount=%d err=%v want 3 (every kind counts here)", n, err)
	}
	if n, err := db.ActiveAgentCount(); err != nil || n != 0 {
		t.Fatalf("ActiveAgentCount=%d err=%v want 0 (a hold spawns no container)", n, err)
	}

	out, err := mgr.Run(context.Background(), runedv1.RunRequest{Folder: "free", MessageBatch: "hi"})
	if err != nil {
		t.Fatalf("run with 3 holds live: %v", err)
	}
	if out.Busy {
		t.Fatal("agent turn rejected busy at MaxConcurrent=1 while only holds were live")
	}
}

// TestHoldUnknownKindFailsLoud: without RegisterExecutor, Hold must error
// rather than claim a folder it has nothing to dispatch to — a claim with no
// executor would take the slot and never give it back.
func TestHoldUnknownKindFailsLoud(t *testing.T) {
	db, mgr := newMgr(t, FakeRuntime{}, 5) // no KindHold executor registered

	if _, err := mgr.Hold(context.Background(), types.Folder("demo"), "restore"); err == nil {
		t.Fatal("Hold with no KindHold executor returned nil error")
	}
	if n, err := db.ActiveCount(); err != nil || n != 0 {
		t.Fatalf("ActiveCount=%d err=%v want 0 — a failed Hold must claim nothing", n, err)
	}
}

// TestHoldRecordsKindAndReason: the hold is visible as an ordinary spawns row
// — that IS its observability story (dashd's runed page reads this table), so
// kind and reason must actually land.
func TestHoldRecordsKindAndReason(t *testing.T) {
	db, mgr := newHoldMgr(t, FakeRuntime{}, 5, time.Minute)

	held, err := mgr.Hold(context.Background(), types.Folder("demo"), "archive apply: filesystem restore")
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	sp := waitSpawnState(t, db, held.RunID, "running")
	if sp.Kind != KindHold {
		t.Errorf("spawn kind=%q want %q", sp.Kind, KindHold)
	}
	if sp.Topic != "archive apply: filesystem restore" {
		t.Errorf("spawn topic=%q want the hold reason", sp.Topic)
	}
	if sp.Folder != "demo" {
		t.Errorf("spawn folder=%q want demo", sp.Folder)
	}
}

// TestHoldReleaseIsIdempotent: releasing twice, or releasing a hold the TTL
// already expired, is a no-op — the CLI releases from a defer and cannot know
// which happened first.
func TestHoldReleaseIsIdempotent(t *testing.T) {
	db, mgr := newHoldMgr(t, FakeRuntime{}, 5, time.Minute)

	held, err := mgr.Hold(context.Background(), types.Folder("demo"), "restore")
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	if err := mgr.Kill(held.RunID); err != nil {
		t.Fatalf("first release: %v", err)
	}
	waitSpawnState(t, db, held.RunID, "killed", "exited")
	if err := mgr.Kill(held.RunID); err != nil {
		t.Fatalf("second release: %v", err)
	}
}

// holdServer is serverWith plus the KindHold executor — the wiring
// runed/cmd/runed/main.go does.
func holdServer(t *testing.T, v Verifier) (*DB, *Server) {
	t.Helper()
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	mgr := NewManager(db, FakeRuntime{}, ManagerConfig{
		Instance: "test", MaxConcurrent: 5, RunTTL: time.Minute,
	})
	mgr.RegisterExecutor(KindHold, NewHoldRuntime())
	return db, NewServer(mgr, db, v)
}

// TestHoldEndpointRequiresScope: POST /v1/holds pausing a folder is a
// privileged mutation — a token without runs:run is 403. Same gate as POST
// /v1/runs, because a hold IS a run.
func TestHoldEndpointRequiresScope(t *testing.T) {
	db, srv := holdServer(t, fakeVerifier{scope: []string{"sessions:read"}})
	got := postJSON(t, srv.Handler(), "/v1/holds", `{"folder":"demo"}`)
	if got.Code != 403 {
		t.Fatalf("hold without runs:run = %d want 403", got.Code)
	}
	if n, err := db.ActiveCount(); err != nil || n != 0 {
		t.Fatalf("ActiveCount=%d err=%v — a rejected hold must claim nothing", n, err)
	}
}

// TestHoldEndpointFolderBound: a folder-scoped token must not pause another
// tenant's folder — pausing a folder denies service to it, so it gets the
// same containment as spawning into it (403 before mgr.Hold).
func TestHoldEndpointFolderBound(t *testing.T) {
	db, srv := holdServer(t, fakeVerifier{scope: []string{"runs:run"}, folder: "alice"})
	got := postJSON(t, srv.Handler(), "/v1/holds", `{"folder":"bob"}`)
	if got.Code != 403 {
		t.Fatalf("cross-folder hold = %d want 403", got.Code)
	}
	if n, err := db.ActiveCount(); err != nil || n != 0 {
		t.Fatalf("ActiveCount=%d err=%v — the 403 must fire before the claim", n, err)
	}

	// Its own subtree is fine.
	if got := postJSON(t, srv.Handler(), "/v1/holds", `{"folder":"alice/eng"}`); got.Code != 200 {
		t.Fatalf("own-subtree hold = %d want 200", got.Code)
	}
}

// TestHoldEndpointRoundTrip: claim over HTTP, observe the folder is closed to
// agent turns, then release over the EXISTING DELETE /v1/runs/{run_id} route
// — the wire shape arizuko archive apply uses.
func TestHoldEndpointRoundTrip(t *testing.T) {
	_, srv := holdServer(t, fakeVerifier{scope: []string{"runs:run", "runs:kill"}})
	h := srv.Handler()

	rec := postJSON(t, h, "/v1/holds", `{"folder":"demo","reason":"archive apply"}`)
	if rec.Code != 200 {
		t.Fatalf("hold = %d body=%s", rec.Code, rec.Body.String())
	}
	var held runedv1.HoldOutcome
	if err := json.Unmarshal(rec.Body.Bytes(), &held); err != nil {
		t.Fatalf("decode hold outcome: %v", err)
	}
	if held.RunID == "" || held.Busy {
		t.Fatalf("hold outcome=%+v want a run_id and busy=false", held)
	}

	// A turn for the held folder is refused while the hold stands.
	rec = postJSON(t, h, "/v1/runs", `{"folder":"demo","message_batch":"hi"}`)
	if rec.Code != 200 {
		t.Fatalf("run during hold = %d body=%s", rec.Code, rec.Body.String())
	}
	var out runedv1.RunOutcome
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode run outcome: %v", err)
	}
	if !out.Busy {
		t.Errorf("run during hold outcome=%+v want busy=true", out)
	}

	// Release through the existing kill route — no hold-specific DELETE.
	if got := req(t, h, "DELETE", "/v1/runs/"+held.RunID); got.Code != 200 {
		t.Fatalf("release = %d body=%s", got.Code, got.Body.String())
	}
	rec = postJSON(t, h, "/v1/runs", `{"folder":"demo","message_batch":"hi"}`)
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode run outcome after release: %v", err)
	}
	if out.Busy {
		t.Errorf("run after release outcome=%+v want admitted", out)
	}
}

// TestHoldEndpointRequiresFolder: no folder is a 400, not a hold on "".
func TestHoldEndpointRequiresFolder(t *testing.T) {
	_, srv := holdServer(t, fakeVerifier{scope: []string{"runs:run"}})
	if got := postJSON(t, srv.Handler(), "/v1/holds", `{"reason":"restore"}`); got.Code != 400 {
		t.Fatalf("hold without folder = %d want 400", got.Code)
	}
}

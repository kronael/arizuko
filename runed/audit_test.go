package runed

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/kronael/arizuko/audit"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

// auditServer is serverWith plus the KindHold executor — the exact wiring
// runed/cmd/runed/main.go does, so the hold route is reachable over HTTP.
// audit.Init is deliberately NOT called: the handlers record through EmitTo on
// runed's own store, so a row landing here proves the write path, not the
// package-state one.
func auditServer(t *testing.T, rt Runtime, v Verifier) (*DB, *Server) {
	t.Helper()
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	cfg, folders := renderWiring(t)
	mgr := NewManager(db, rt, ManagerConfig{
		Instance:      "test",
		MaxConcurrent: 5, RunTTL: time.Minute,
		Cfg: cfg, Folders: folders,
	})
	mgr.RegisterExecutor(KindHold, NewHoldRuntime())
	return db, NewServer(mgr, db, v)
}

// allAuditRows reads everything runed recorded, oldest first, through the method
// /v1/audit answers with — the assertion is about what an operator can read
// back, which is the only thing a stored row is for.
func allAuditRows(t *testing.T, db *DB) []audit.Row {
	t.Helper()
	newest, err := audit.Query(t.Context(), db.Obj(), audit.Filter{Limit: audit.MaxLimit})
	if err != nil {
		t.Fatalf("read the audit trail: %v", err)
	}
	out := make([]audit.Row, 0, len(newest))
	for i := len(newest) - 1; i >= 0; i-- {
		out = append(out, newest[i])
	}
	return out
}

// auditRows reads every recorded row for an action, oldest first.
func auditRows(t *testing.T, db *DB, action string) []audit.Row {
	t.Helper()
	var out []audit.Row
	for _, r := range allAuditRows(t, db) {
		if r.Action == action {
			out = append(out, r)
		}
	}
	return out
}

// oneRow asserts EXACTLY one row exists for an action and returns it. Every
// content assertion goes through this: an empty table must fail the test
// rather than vacuously satisfy "no row contains a secret".
func oneRow(t *testing.T, db *DB, action string) audit.Row {
	t.Helper()
	got := auditRows(t, db, action)
	if len(got) != 1 {
		t.Fatalf("recorded rows for action=%q: %d, want exactly 1 (%+v)", action, len(got), got)
	}
	return got[0]
}

// countAudit returns the total recorded row count, whatever the action.
func countAudit(t *testing.T, db *DB) int {
	t.Helper()
	return len(allAuditRows(t, db))
}

// TestHoldEmitsAuditRow: POST /v1/holds records WHO paused the folder. The
// spawns row a claimed hold leaves carries the reason (as topic) but no
// caller — the audit row is the only place the claimant's identity lands.
func TestHoldEmitsAuditRow(t *testing.T) {
	db, srv := auditServer(t, FakeRuntime{}, fakeVerifier{
		sub: "user:google:114alice", scope: []string{"runs:run"}, folder: "demo",
	})
	rec := postJSON(t, srv.Handler(), "/v1/holds", `{"folder":"demo","reason":"restore"}`)
	if rec.Code != 200 {
		t.Fatalf("hold = %d want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var out runedv1.HoldOutcome
	json.Unmarshal(rec.Body.Bytes(), &out)
	if out.RunID == "" {
		t.Fatalf("hold outcome=%+v want a run_id", out)
	}

	row := oneRow(t, db, "run.hold")
	if row.Category != audit.CategoryAgent {
		t.Errorf("category=%q want %q", row.Category, audit.CategoryAgent)
	}
	if row.Actor != "user:google:114alice" {
		t.Errorf("actor=%q want the bearer sub verbatim", row.Actor)
	}
	if row.ActorSub != "google:114alice" {
		t.Errorf("actor_sub=%q want the bare principal (auth.BareSub)", row.ActorSub)
	}
	if row.Resource != "runs/"+out.RunID {
		t.Errorf("resource=%q want runs/%s", row.Resource, out.RunID)
	}
	if row.Folder != "demo" || row.Surface != audit.SurfaceREST || row.Outcome != audit.OutcomeOK {
		t.Errorf("folder=%q surface=%q outcome=%q want demo/rest/ok", row.Folder, row.Surface, row.Outcome)
	}
	if row.ParamsSummary != `{"reason":"restore"}` {
		t.Errorf("params_summary=%q want the hold reason", row.ParamsSummary)
	}

	// release so the detached hold goroutine exits before the DB closes.
	req(t, srv.Handler(), "DELETE", "/v1/runs/"+out.RunID)
}

// TestHoldBusyEmitsAuditRow: a hold on an already-held folder claims nothing,
// so it writes NO spawns row — the audit row is the entire record that the
// call happened. Losing it loses the fact that someone tried to pause a folder
// that was already paused.
func TestHoldBusyEmitsAuditRow(t *testing.T) {
	db, srv := auditServer(t, FakeRuntime{}, fakeVerifier{
		sub: "service:backupd", scope: []string{"runs:run"},
	})
	h := srv.Handler()
	first := postJSON(t, h, "/v1/holds", `{"folder":"demo","reason":"restore"}`)
	var held runedv1.HoldOutcome
	json.Unmarshal(first.Body.Bytes(), &held)
	if held.RunID == "" {
		t.Fatalf("first hold outcome=%+v want a run_id", held)
	}
	spawnsBefore := countSpawns(t, db)

	second := postJSON(t, h, "/v1/holds", `{"folder":"demo","reason":"vacuum"}`)
	if second.Code != 200 {
		t.Fatalf("second hold = %d want 200", second.Code)
	}
	var busy runedv1.HoldOutcome
	json.Unmarshal(second.Body.Bytes(), &busy)
	if !busy.Busy {
		t.Fatalf("second hold outcome=%+v want busy=true", busy)
	}
	if got := countSpawns(t, db); got != spawnsBefore {
		t.Fatalf("busy hold wrote %d spawns rows — premise of this test is that it writes none", got-spawnsBefore)
	}

	rows := auditRows(t, db, "run.hold")
	if len(rows) != 2 {
		t.Fatalf("run.hold rows=%d want 2 (the claim and the busy reject): %+v", len(rows), rows)
	}
	row := rows[1]
	if row.Resource != "" {
		t.Errorf("resource=%q want empty — a busy hold claimed no run", row.Resource)
	}
	if row.Actor != "service:backupd" || row.ActorSub != "backupd" {
		t.Errorf("actor=%q actor_sub=%q want service:backupd/backupd", row.Actor, row.ActorSub)
	}
	if row.ParamsSummary != `{"busy":true,"reason":"vacuum"}` {
		t.Errorf("params_summary=%q want busy + the rejected reason", row.ParamsSummary)
	}

	req(t, h, "DELETE", "/v1/runs/"+held.RunID)
}

// TestKillEmitsAuditRow: DELETE /v1/runs/{id} records WHO killed the run.
// spawns records state='killed' but never the killer, and a deliberate kill is
// pointedly not an outcome=error, so the spawns row alone cannot distinguish
// an operator stop from a clean finish.
func TestKillEmitsAuditRow(t *testing.T) {
	db, srv := auditServer(t, &killRecorder{}, fakeVerifier{
		sub: "user:google:114alice", scope: []string{"runs:kill"}, folder: "demo",
	})
	_ = db.CreateSpawn(t.Context(), Spawn{RunID: "run_k", Folder: "demo", ContainerName: "c1", State: "running"})

	if got := req(t, srv.Handler(), "DELETE", "/v1/runs/run_k"); got.Code != 200 {
		t.Fatalf("kill = %d want 200", got.Code)
	}

	row := oneRow(t, db, "run.kill")
	if row.Category != audit.CategoryAgent || row.Surface != audit.SurfaceREST {
		t.Errorf("category=%q surface=%q want agent/rest", row.Category, row.Surface)
	}
	if row.Actor != "user:google:114alice" || row.ActorSub != "google:114alice" {
		t.Errorf("actor=%q actor_sub=%q want the bearer sub + bare principal", row.Actor, row.ActorSub)
	}
	if row.Resource != "runs/run_k" || row.Folder != "demo" {
		t.Errorf("resource=%q folder=%q want runs/run_k + demo", row.Resource, row.Folder)
	}
	if row.ParamsSummary != `{"killed":true}` {
		t.Errorf("params_summary=%q want killed:true", row.ParamsSummary)
	}
}

// TestKillOfTerminalRunEmitsAuditRow: killing an already-exited run is an
// idempotent 200 that touches NOTHING — no container stopped, no spawns column
// changed (KillSpawn's WHERE guards terminal states). The audit row is the only
// evidence the operator reached for it, and it must say killed=false.
func TestKillOfTerminalRunEmitsAuditRow(t *testing.T) {
	rec := &killRecorder{}
	db, srv := auditServer(t, rec, fakeVerifier{
		sub: "user:google:114alice", scope: []string{"runs:kill"}, folder: "demo",
	})
	_ = db.CreateSpawn(t.Context(), Spawn{RunID: "run_done", Folder: "demo", ContainerName: "c1", State: "queued"})
	_ = db.EndSpawn(t.Context(), "run_done", "exited", runedv1.OutcomeOK, 0)

	if got := req(t, srv.Handler(), "DELETE", "/v1/runs/run_done"); got.Code != 200 {
		t.Fatalf("kill of terminal run = %d want 200", got.Code)
	}
	sp, _ := db.SpawnByRunID(t.Context(), "run_done")
	if sp.State != "exited" {
		t.Fatalf("spawns state=%q — premise is that a terminal run is untouched", sp.State)
	}

	row := oneRow(t, db, "run.kill")
	if row.ParamsSummary != `{"killed":false}` {
		t.Errorf("params_summary=%q want killed:false — nothing was live to stop", row.ParamsSummary)
	}
	if row.Resource != "runs/run_done" || row.Outcome != audit.OutcomeOK {
		t.Errorf("resource=%q outcome=%q want runs/run_done + ok", row.Resource, row.Outcome)
	}
}

// TestStopIdleFolderEmitsAuditRow: POST /v1/runs/stop on a folder with no live
// spawn resolves no run at all — no row anywhere records the attempt. Driven
// by service:routd (folder=""), the token behind every operator /stop.
func TestStopIdleFolderEmitsAuditRow(t *testing.T) {
	db, srv := auditServer(t, &killRecorder{}, fakeVerifier{
		sub: "service:routd", scope: []string{"runs:kill"},
	})
	if got := postJSON(t, srv.Handler(), "/v1/runs/stop", `{"folder":"idle"}`); got.Code != 200 {
		t.Fatalf("stop = %d want 200", got.Code)
	}

	row := oneRow(t, db, "run.kill")
	if row.Actor != "service:routd" || row.ActorSub != "routd" {
		t.Errorf("actor=%q actor_sub=%q want service:routd/routd", row.Actor, row.ActorSub)
	}
	if row.Folder != "idle" {
		t.Errorf("folder=%q want idle — the only identifier an idle stop has", row.Folder)
	}
	if row.Resource != "" {
		t.Errorf("resource=%q want empty — no run was resolved", row.Resource)
	}
	if row.ParamsSummary != `{"killed":false}` {
		t.Errorf("params_summary=%q want killed:false", row.ParamsSummary)
	}
}

// TestRunEmitsNoAuditRow pins the judgement call in runed/audit.go: a turn
// dispatch writes NO audit row, because the spawns row it creates already
// carries kind/state/outcome/exit_code/timings and dashd renders it. A row per
// turn would be the same facts at turn volume with less detail.
//
// Not vacuous: the same server then kills the run and that row DOES land, so
// an empty audit_log would fail here — the test can only pass when the table
// exists and the emitter works.
func TestRunEmitsNoAuditRow(t *testing.T) {
	db, srv := auditServer(t, &killRecorder{}, fakeVerifier{
		sub: "service:routd", scope: []string{"runs:run", "runs:kill"},
	})
	h := srv.Handler()

	if got := doRun(t, h, runedv1.RunRequest{Folder: "demo", ChatJID: "j", TurnID: "t1", MessageBatch: "m"}); got.Code != 200 {
		t.Fatalf("run = %d want 200 (body=%s)", got.Code, got.Body.String())
	}
	if got := countSpawns(t, db); got != 1 {
		t.Fatalf("spawns rows=%d want 1 — the run must be recorded THERE", got)
	}
	if got := countAudit(t, db); got != 0 {
		t.Fatalf("audit_log rows=%d after a turn, want 0: %+v", got, auditRows(t, db, "run.hold"))
	}

	// Control: the emitter and the table are live in this very DB.
	_ = db.CreateSpawn(t.Context(), Spawn{RunID: "run_k", Folder: "demo", ContainerName: "c1", State: "running"})
	if got := req(t, h, "DELETE", "/v1/runs/run_k"); got.Code != 200 {
		t.Fatalf("control kill = %d want 200", got.Code)
	}
	if got := countAudit(t, db); got != 1 {
		t.Fatalf("audit_log rows=%d after the control kill, want 1 — "+
			"the no-row-per-turn assertion above is only meaningful if writes work", got)
	}
}

// countSpawns returns how many spawns the database holds, of any state.
//
// It reads through the store rather than counting the entity's rows, so this
// fixture cannot be the one place that still knows runed's storage is SQL — a
// COUNT(*) works against a three-column entity table and would keep working
// against the wrong thing.
func countSpawns(t *testing.T, db *DB) int {
	t.Helper()
	rows, err := db.RecentSpawns(t.Context(), "", "", 500)
	if err != nil {
		t.Fatalf("count spawns: %v", err)
	}
	return len(rows)
}

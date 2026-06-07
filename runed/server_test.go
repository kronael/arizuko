package runed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/types"
)

// fakeVerifier returns a fixed (sub, scope, folder) for any request — stands
// in for offline JWKs verification in the auth tests.
type fakeVerifier struct {
	sub    string
	scope  []string
	folder string
}

func (v fakeVerifier) Verify(*http.Request) (string, []string, string, error) {
	return v.sub, v.scope, v.folder, nil
}

// killRecorder is a Runtime that records Kill calls.
type killRecorder struct {
	FakeRuntime
	killed int32
}

func (k *killRecorder) Kill(string) error { atomic.AddInt32(&k.killed, 1); return nil }

func serverWith(t *testing.T, rt Runtime, v Verifier) (*DB, *Server) {
	t.Helper()
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	mgr := NewManager(db, rt, NewStaticBroker("jws", "jti"), ManagerConfig{
		Scopes: []types.Scope{"messages:send:own_group"}, Instance: "test", MaxConcurrent: 5,
	})
	return db, NewServer(mgr, db, v)
}

func req(t *testing.T, h http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

// TestKillRequiresScope: DELETE /v1/runs/{id} demands runs:kill — a token
// without it is 403 (spec 5/P § DELETE; bugs.md should-fix server.go:43).
func TestKillRequiresScope(t *testing.T) {
	rec := &killRecorder{}
	db, srv := serverWith(t, rec, fakeVerifier{scope: []string{"sessions:read"}}) // wrong scope
	_ = db.CreateSpawn(Spawn{RunID: "run_k", Folder: "demo", ContainerName: "c", State: "running"})
	h := srv.Handler()

	if got := req(t, h, "DELETE", "/v1/runs/run_k"); got.Code != 403 {
		t.Fatalf("kill without runs:kill = %d want 403", got.Code)
	}
	if atomic.LoadInt32(&rec.killed) != 0 {
		t.Fatal("container killed despite missing scope")
	}
}

// TestKillStopsContainer: a kill with runs:kill stops the container (Runtime.
// Kill) and records state=killed WITHOUT outcome=error (deliberate kill).
func TestKillStopsContainer(t *testing.T) {
	rec := &killRecorder{}
	db, srv := serverWith(t, rec, fakeVerifier{scope: []string{"runs:kill"}, folder: "demo"})
	_ = db.CreateSpawn(Spawn{RunID: "run_k", Folder: "demo", ContainerName: "c1", State: "running"})
	h := srv.Handler()

	if got := req(t, h, "DELETE", "/v1/runs/run_k"); got.Code != 200 {
		t.Fatalf("kill = %d want 200", got.Code)
	}
	if atomic.LoadInt32(&rec.killed) != 1 {
		t.Fatalf("Runtime.Kill called %d times, want 1", rec.killed)
	}
	sp, _ := db.GetSpawn("run_k")
	if sp.State != "killed" {
		t.Fatalf("state=%q want killed", sp.State)
	}
	if sp.Outcome == runedv1.OutcomeError {
		t.Fatalf("outcome=%q — a deliberate kill must NOT be error", sp.Outcome)
	}
}

// TestStopFolderKillsActiveRun: POST /v1/runs/stop maps a folder to its live
// spawn and kills it (the operator-kill path behind routd's /stop), returning
// killed:true + the run_id.
func TestStopFolderKillsActiveRun(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	rec := &killRecorder{FakeRuntime: FakeRuntime{Fn: func(_ context.Context, _ RunSpec) RunResult {
		close(started)
		<-release
		return RunResult{Outcome: runedv1.OutcomeOK, NewSessionID: "s1"}
	}}}
	db, srv := serverWith(t, rec, fakeVerifier{scope: []string{"runs:run", "runs:kill"}, folder: "demo"})
	h := srv.Handler()

	// stand up a live run so the folder has an active spawn to stop.
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- doRun(t, h, runedv1.RunRequest{Folder: "demo", ChatJID: "j", TurnID: "t1", MessageBatch: "m"}) }()
	<-started
	runID := srv.mgr.ActiveRunID("demo")
	if runID == "" {
		t.Fatal("no active run registered for demo")
	}

	got := postJSON(t, h, "/v1/runs/stop", `{"folder":"demo"}`)
	if got.Code != 200 {
		t.Fatalf("stop = %d want 200 (body=%s)", got.Code, got.Body.String())
	}
	var out runedv1.StopRunResponse
	json.Unmarshal(got.Body.Bytes(), &out)
	if !out.Killed || out.RunID != runID {
		t.Fatalf("stop response killed=%v run_id=%q want killed=true run_id=%q", out.Killed, out.RunID, runID)
	}
	if atomic.LoadInt32(&rec.killed) != 1 {
		t.Fatalf("Runtime.Kill called %d times, want 1", rec.killed)
	}
	sp, _ := db.GetSpawn(runID)
	if sp.State != "killed" {
		t.Fatalf("state=%q want killed", sp.State)
	}
	close(release)
	<-done
}

// TestStopFolderNoActiveRun: POST /v1/runs/stop for an idle folder is a no-op —
// killed:false (routd renders gated's "No active container" text). The operator
// /stop path runs as service:routd (folder=""), which may target any folder.
func TestStopFolderNoActiveRun(t *testing.T) {
	rec := &killRecorder{}
	_, srv := serverWith(t, rec, fakeVerifier{scope: []string{"runs:kill"}})
	got := postJSON(t, srv.Handler(), "/v1/runs/stop", `{"folder":"idle"}`)
	if got.Code != 200 {
		t.Fatalf("stop = %d want 200", got.Code)
	}
	var out runedv1.StopRunResponse
	json.Unmarshal(got.Body.Bytes(), &out)
	if out.Killed {
		t.Fatalf("idle folder stop killed=%v want false", out.Killed)
	}
	if atomic.LoadInt32(&rec.killed) != 0 {
		t.Fatal("Runtime.Kill called for an idle folder")
	}
}

// TestStopFolderRequiresScope: POST /v1/runs/stop demands runs:kill.
func TestStopFolderRequiresScope(t *testing.T) {
	_, srv := serverWith(t, &killRecorder{}, fakeVerifier{scope: []string{"sessions:read"}, folder: "demo"})
	if got := postJSON(t, srv.Handler(), "/v1/runs/stop", `{"folder":"demo"}`); got.Code != 403 {
		t.Fatalf("stop without runs:kill = %d want 403", got.Code)
	}
}

func postJSON(t *testing.T, h http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

// TestSessionsFolderBound: GET /v1/sessions is bounded by the token's
// arz/folder, NOT the ?folder= query param — a token cannot read another
// folder's history (bugs.md should-fix server.go:43).
func TestSessionsFolderBound(t *testing.T) {
	db, srv := serverWith(t, FakeRuntime{}, fakeVerifier{scope: []string{"sessions:read"}, folder: "alice"})
	// rows for two folders.
	a, _ := db.RecordSession("alice", "sa")
	db.EndSession(a, "sa", "ok", "", 1)
	b, _ := db.RecordSession("bob", "sb")
	db.EndSession(b, "sb", "ok", "", 1)
	h := srv.Handler()

	// even asking for bob, the token's folder (alice) wins.
	rec := req(t, h, "GET", "/v1/sessions?folder=bob")
	if rec.Code != 200 {
		t.Fatalf("sessions = %d want 200", rec.Code)
	}
	var out runedv1.SessionsResponse
	json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Sessions) != 1 || out.Sessions[0].SessionID != "sa" {
		t.Fatalf("cross-folder read leaked: %+v (token folder=alice, asked bob)", out.Sessions)
	}
}

// TestSessionsRequiresScope: GET /v1/sessions without sessions:read is 403.
func TestSessionsRequiresScope(t *testing.T) {
	db, srv := serverWith(t, FakeRuntime{}, fakeVerifier{scope: []string{"runs:kill"}, folder: "alice"})
	_ = db
	if got := req(t, srv.Handler(), "GET", "/v1/sessions?folder=alice"); got.Code != 403 {
		t.Fatalf("sessions without sessions:read = %d want 403", got.Code)
	}
}

// TestRecentSessionsReturnsRecords: GET /v1/sessions/recent returns the seeded
// session_log rows for a folder, full-fielded (group_folder + error), newest
// first — the federated read routd uses for its new_session hint.
func TestRecentSessionsReturnsRecords(t *testing.T) {
	db, srv := serverWith(t, FakeRuntime{}, fakeVerifier{scope: []string{"sessions:read"}, folder: "alice"})
	id, _ := db.RecordSession("alice", "sa")
	db.EndSession(id, "sa", "ok", "boom", 7)
	h := srv.Handler()

	rec := req(t, h, "GET", "/v1/sessions/recent?n=5")
	if rec.Code != 200 {
		t.Fatalf("recent = %d want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var out runedv1.RecentSessionsResponse
	json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Sessions) != 1 {
		t.Fatalf("recent rows=%d want 1: %+v", len(out.Sessions), out.Sessions)
	}
	s := out.Sessions[0]
	if s.GroupFolder != "alice" || s.SessionID != "sa" || s.Result != "ok" ||
		s.Error != "boom" || s.MessageCount != 7 {
		t.Fatalf("recent row fields wrong: %+v", s)
	}
}

// TestRecentSessionsFolderBound: GET /v1/sessions/recent is bounded by the
// token's arz/folder, NOT ?folder= — a token cannot read another folder's
// history.
func TestRecentSessionsFolderBound(t *testing.T) {
	db, srv := serverWith(t, FakeRuntime{}, fakeVerifier{scope: []string{"sessions:read"}, folder: "alice"})
	a, _ := db.RecordSession("alice", "sa")
	db.EndSession(a, "sa", "ok", "", 1)
	b, _ := db.RecordSession("bob", "sb")
	db.EndSession(b, "sb", "ok", "", 1)

	rec := req(t, srv.Handler(), "GET", "/v1/sessions/recent?folder=bob")
	if rec.Code != 200 {
		t.Fatalf("recent = %d want 200", rec.Code)
	}
	var out runedv1.RecentSessionsResponse
	json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Sessions) != 1 || out.Sessions[0].SessionID != "sa" {
		t.Fatalf("cross-folder read leaked: %+v (token folder=alice, asked bob)", out.Sessions)
	}
}

// TestRecentSessionsRequiresScope: GET /v1/sessions/recent without sessions:read
// is 403.
func TestRecentSessionsRequiresScope(t *testing.T) {
	_, srv := serverWith(t, FakeRuntime{}, fakeVerifier{scope: []string{"runs:kill"}, folder: "alice"})
	if got := req(t, srv.Handler(), "GET", "/v1/sessions/recent?folder=alice"); got.Code != 403 {
		t.Fatalf("recent without sessions:read = %d want 403", got.Code)
	}
}

// TestRunRequiresScope: POST /v1/runs demands runs:run — a token without it is
// 403; a wildcard runs:* grant satisfies it (wildcard scope match, not exact).
func TestRunRequiresScope(t *testing.T) {
	post := func(scope []string) int {
		_, srv := serverWith(t, FakeRuntime{}, fakeVerifier{scope: scope, folder: "demo"})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/runs",
			strings.NewReader(`{"folder":"demo","message_batch":"m"}`)))
		return rec.Code
	}
	if got := post([]string{"sessions:read"}); got != 403 {
		t.Fatalf("run without runs:run = %d want 403", got)
	}
	if got := post([]string{"runs:*"}); got == 403 {
		t.Fatalf("run with runs:* wildcard = 403, want accepted (wildcard scope match broken)")
	}
}

// TestFreshSpawnResolvesSessionID: a fresh run (no session_id) must pass the
// RESOLVED session id into Runtime.Run, not the empty req.SessionID — else the
// DB/slot advertise one session while the container starts another.
func TestFreshSpawnResolvesSessionID(t *testing.T) {
	var got string
	rt := FakeRuntime{Fn: func(_ context.Context, spec RunSpec) RunResult {
		got = spec.SessionID
		return RunResult{Outcome: runedv1.OutcomeOK, NewSessionID: spec.SessionID}
	}}
	_, mgr := newMgr(t, rt, 5)
	mgr.Run(context.Background(), runedv1.RunRequest{Folder: "demo", MessageBatch: "m"})
	if got == "" {
		t.Fatal("fresh spawn passed empty SessionID to Runtime.Run — resolved id must reach the container")
	}
}

// TestRunStatusStoreErrorReturns500: GET /v1/runs/{id} must surface a real
// store failure as 500, NOT mask it as 404 unknown_run. Before the fix every
// GetSpawn error collapsed to 404; a dropped/locked spawns table would lie
// "no such run" instead of reporting the failure (matches handleRunKill's
// ErrNotFound→404 / other→500 split).
func TestRunStatusStoreErrorReturns500(t *testing.T) {
	db, srv := serverWith(t, FakeRuntime{}, fakeVerifier{scope: []string{"runs:run"}})
	h := srv.Handler()
	// A genuinely-absent run is 404.
	if got := req(t, h, "GET", "/v1/runs/nope"); got.Code != 404 {
		t.Fatalf("absent run = %d want 404", got.Code)
	}
	// Force a real store error: drop the spawns table so GetSpawn fails hard.
	if _, err := db.SQL().Exec("DROP TABLE spawns"); err != nil {
		t.Fatal(err)
	}
	if got := req(t, h, "GET", "/v1/runs/anything"); got.Code != 500 {
		t.Fatalf("store failure = %d want 500", got.Code)
	}
}

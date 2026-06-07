package runed

import (
	"sync/atomic"
	"testing"
)

// A folder-scoped token may only inspect/kill/stop runs in its own subtree;
// another folder's run is invisible (404) and unkillable (codex split #4). The
// MCP path never reaches runed, so the REST gate is the only boundary here.
func TestRunControlFolderBound(t *testing.T) {
	rec := &killRecorder{}
	db, srv := serverWith(t, rec, fakeVerifier{scope: []string{"runs:kill"}, folder: "alice"})
	_ = db.CreateSpawn(Spawn{RunID: "run_bob", Folder: "bob", ContainerName: "c", State: "running"})
	h := srv.Handler()

	if got := req(t, h, "GET", "/v1/runs/run_bob"); got.Code != 404 {
		t.Fatalf("cross-folder status = %d want 404 (no leak)", got.Code)
	}
	if got := req(t, h, "DELETE", "/v1/runs/run_bob"); got.Code != 404 {
		t.Fatalf("cross-folder kill = %d want 404", got.Code)
	}
	if atomic.LoadInt32(&rec.killed) != 0 {
		t.Fatal("cross-folder kill actually stopped the container")
	}
	if got := postJSON(t, h, "/v1/runs/stop", `{"folder":"bob"}`); got.Code != 404 {
		t.Fatalf("cross-folder stop = %d want 404 (uniform with status/kill)", got.Code)
	}
}

// Root / service:routd (empty folder) drives every folder's runs — unrestricted.
func TestRunControlRootUnrestricted(t *testing.T) {
	rec := &killRecorder{}
	db, srv := serverWith(t, rec, fakeVerifier{scope: []string{"runs:kill"}}) // folder=""
	_ = db.CreateSpawn(Spawn{RunID: "run_bob", Folder: "bob", ContainerName: "c", State: "running"})
	h := srv.Handler()

	if got := req(t, h, "GET", "/v1/runs/run_bob"); got.Code != 200 {
		t.Fatalf("root status = %d want 200", got.Code)
	}
	if got := req(t, h, "DELETE", "/v1/runs/run_bob"); got.Code != 200 {
		t.Fatalf("root kill = %d want 200", got.Code)
	}
	if atomic.LoadInt32(&rec.killed) != 1 {
		t.Fatalf("root kill called Runtime.Kill %d times, want 1", rec.killed)
	}
}

// A folder-scoped runs:run token may not spawn a run in another folder — that
// would execute the agent with the victim folder's secrets + network. The 403
// must fire before mgr.Run (the lone run-control verb left unbound by the #21
// hardening, found in the 2026-06-07 bug sweep).
func TestRunSpawnFolderBound(t *testing.T) {
	rec := &killRecorder{}
	_, srv := serverWith(t, rec, fakeVerifier{scope: []string{"runs:run"}, folder: "alice"})
	h := srv.Handler()
	if got := postJSON(t, h, "/v1/runs", `{"folder":"bob"}`); got.Code != 403 {
		t.Fatalf("cross-folder spawn = %d want 403", got.Code)
	}
}

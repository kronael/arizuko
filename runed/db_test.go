package runed

import (
	"testing"
	"time"

	"github.com/kronael/arizuko/objstore/dynstore/dyntest"
)

func TestMain(m *testing.M) { dyntest.Main(m) }

// TestDBSpawnLifecycle exercises the spawns + session_log + mcp_tokens
// round-trip: create → start → end, with the brokered token ref and
// session_log close.
func TestDBSpawnLifecycle(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	logID, err := db.OpenSession(t.Context(), "demo", "sess0")
	if err != nil {
		t.Fatalf("record session: %v", err)
	}
	if err := db.CreateSpawn(t.Context(), Spawn{
		RunID: "run_1", Folder: "demo", ContainerName: "c1",
		SessionLogID: logID, SessionID: "sess0", State: "queued",
	}); err != nil {
		t.Fatalf("create spawn: %v", err)
	}
	if _, err := db.StartSpawn(t.Context(), "run_1", "sess0"); err != nil {
		t.Fatalf("start spawn: %v", err)
	}
	if err := db.EndSpawn(t.Context(), "run_1", "exited", "ok", 0); err != nil {
		t.Fatalf("end spawn: %v", err)
	}
	if err := db.CloseSession(t.Context(), "demo", logID, "sess1-new", "ok", "", 3); err != nil {
		t.Fatalf("end session: %v", err)
	}

	sp, err := db.SpawnByRunID(t.Context(), "run_1")
	if err != nil {
		t.Fatalf("get spawn: %v", err)
	}
	if sp.State != "exited" || sp.Outcome != "ok" {
		t.Fatalf("spawn=%+v", sp)
	}
	sessions, _ := db.RecentSessions(t.Context(), "demo", 10)
	if len(sessions) != 1 || sessions[0].SessionID != "sess1-new" || sessions[0].MessageCount != 3 {
		t.Fatalf("sessions=%+v", sessions)
	}
}

// TestKillSpawnDoesNotOverwriteTerminal: KillSpawn transitions only a still-
// active run. A run that already completed normally (state='exited') keeps its
// terminal state — a kill that loses the TOCTOU race must not relabel it
// 'killed'. A queued/running run IS transitioned.
func TestKillSpawnDoesNotOverwriteTerminal(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// run that completed normally before the kill lands.
	_ = db.CreateSpawn(t.Context(), Spawn{RunID: "done", Folder: "demo", ContainerName: "c1", State: "queued"})
	_, _ = db.StartSpawn(t.Context(), "done", "s")
	if err := db.EndSpawn(t.Context(), "done", "exited", "ok", 0); err != nil {
		t.Fatalf("end spawn: %v", err)
	}
	if err := db.KillSpawn(t.Context(), "done"); err != nil {
		t.Fatalf("kill spawn: %v", err)
	}
	if sp, _ := db.SpawnByRunID(t.Context(), "done"); sp.State != "exited" || sp.Outcome != "ok" {
		t.Fatalf("completed run got clobbered: state=%q outcome=%q want exited/ok", sp.State, sp.Outcome)
	}

	// a still-running run IS killed.
	_ = db.CreateSpawn(t.Context(), Spawn{RunID: "live", Folder: "demo2", ContainerName: "c2", State: "queued"})
	_, _ = db.StartSpawn(t.Context(), "live", "s")
	if err := db.KillSpawn(t.Context(), "live"); err != nil {
		t.Fatalf("kill spawn: %v", err)
	}
	if sp, _ := db.SpawnByRunID(t.Context(), "live"); sp.State != "killed" {
		t.Fatalf("live run not killed: state=%q want killed", sp.State)
	}
}

// TestExpireOrphans marks running/queued spawns as exited on startup.
func TestExpireOrphans(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_ = db.CreateSpawn(t.Context(), Spawn{RunID: "run_orphan1", Folder: "demo", ContainerName: "c1", State: "running"})
	_ = db.CreateSpawn(t.Context(), Spawn{RunID: "run_orphan2", Folder: "demo", ContainerName: "c2", State: "queued"})
	_ = db.CreateSpawn(t.Context(), Spawn{RunID: "run_done", Folder: "demo", ContainerName: "c3", State: "exited"})

	n, err := db.ExpireOrphans(t.Context())
	if err != nil {
		t.Fatalf("ExpireOrphans: %v", err)
	}
	if n != 2 {
		t.Errorf("expired %d, want 2", n)
	}
	// running/queued are gone from active count
	active, _ := db.LiveSpawnCount(t.Context())
	if active != 0 {
		t.Errorf("active count = %d after expire, want 0", active)
	}
	// already-terminal row untouched
	sp, _ := db.SpawnByRunID(t.Context(), "run_done")
	if sp.State != "exited" {
		t.Errorf("terminal spawn state = %q, want exited", sp.State)
	}
}

// TestSweepExpired drops aged spawns and leaves the ones inside the window. The
// cutoff is the caller's, so the whole retention window is observable without
// waiting seven days for it.
func TestSweepExpired(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_ = db.CreateSpawn(t.Context(), Spawn{RunID: "run_old", Folder: "demo", ContainerName: "c", State: "exited"})
	// A cutoff in the future puts the just-created row (created_at = now)
	// strictly before it, so the sweep takes it.
	n, err := db.DropSpawnsBefore(t.Context(), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 1 {
		t.Errorf("swept %d spawns, want 1", n)
	}
	if _, err := db.SpawnByRunID(t.Context(), "run_old"); err != ErrNotFound {
		t.Fatalf("aged spawn not swept (err=%v)", err)
	}

	// A cutoff in the past leaves it: the sweep bounds the partition, it does
	// not empty it, and a comparison running the wrong way would pass the check
	// above and fail here.
	_ = db.CreateSpawn(t.Context(), Spawn{RunID: "run_new", Folder: "demo", ContainerName: "c", State: "exited"})
	if n, err := db.DropSpawnsBefore(t.Context(), time.Now().Add(-time.Hour)); err != nil || n != 0 {
		t.Fatalf("sweep before the window = (%d, %v) want (0, nil)", n, err)
	}
	if _, err := db.SpawnByRunID(t.Context(), "run_new"); err != nil {
		t.Fatalf("a spawn inside the window was swept: %v", err)
	}
}

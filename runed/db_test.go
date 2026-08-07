package runed

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Open never creates runed.db. A runed pointed at the wrong store/<owner>/
// would otherwise migrate a fresh file and lose every spawn and session_log
// row while reporting healthy (spec 5/16 step 7).
func TestOpenRefusesToManufactureAnEmptyDB(t *testing.T) {
	dir := t.TempDir()
	if _, err := Open(dir); err == nil {
		t.Fatal("Open created runed.db on an empty dir")
	}
	if _, err := os.Stat(filepath.Join(dir, "runed", "runed.db")); !os.IsNotExist(err) {
		t.Errorf("Open left a runed.db behind (err=%v)", err)
	}
	db, err := Create(dir)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	db.Close()
	db2, err := Open(dir)
	if err != nil {
		t.Fatalf("Open after Create: %v", err)
	}
	db2.Close()
}

// TestDBSpawnLifecycle exercises the spawns + session_log + mcp_tokens
// round-trip: create → start → end, with the brokered token ref and
// session_log close.
func TestDBSpawnLifecycle(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	logID, err := db.RecordSession("demo", "sess0")
	if err != nil {
		t.Fatalf("record session: %v", err)
	}
	if err := db.CreateSpawn(Spawn{
		RunID: "run_1", Folder: "demo", ContainerName: "c1",
		SessionLogID: logID, SessionID: "sess0", State: "queued",
	}); err != nil {
		t.Fatalf("create spawn: %v", err)
	}
	if _, err := db.StartSpawn("run_1", "sess0"); err != nil {
		t.Fatalf("start spawn: %v", err)
	}
	if err := db.EndSpawn("run_1", "exited", "ok", 0); err != nil {
		t.Fatalf("end spawn: %v", err)
	}
	if err := db.EndSession(logID, "sess1-new", "ok", "", 3); err != nil {
		t.Fatalf("end session: %v", err)
	}

	sp, err := db.GetSpawn("run_1")
	if err != nil {
		t.Fatalf("get spawn: %v", err)
	}
	if sp.State != "exited" || sp.Outcome != "ok" {
		t.Fatalf("spawn=%+v", sp)
	}
	sessions, _ := db.RecentSessions("demo", 10)
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
	_ = db.CreateSpawn(Spawn{RunID: "done", Folder: "demo", ContainerName: "c1", State: "queued"})
	_, _ = db.StartSpawn("done", "s")
	if err := db.EndSpawn("done", "exited", "ok", 0); err != nil {
		t.Fatalf("end spawn: %v", err)
	}
	if err := db.KillSpawn("done"); err != nil {
		t.Fatalf("kill spawn: %v", err)
	}
	if sp, _ := db.GetSpawn("done"); sp.State != "exited" || sp.Outcome != "ok" {
		t.Fatalf("completed run got clobbered: state=%q outcome=%q want exited/ok", sp.State, sp.Outcome)
	}

	// a still-running run IS killed.
	_ = db.CreateSpawn(Spawn{RunID: "live", Folder: "demo2", ContainerName: "c2", State: "queued"})
	_, _ = db.StartSpawn("live", "s")
	if err := db.KillSpawn("live"); err != nil {
		t.Fatalf("kill spawn: %v", err)
	}
	if sp, _ := db.GetSpawn("live"); sp.State != "killed" {
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
	_ = db.CreateSpawn(Spawn{RunID: "run_orphan1", Folder: "demo", ContainerName: "c1", State: "running"})
	_ = db.CreateSpawn(Spawn{RunID: "run_orphan2", Folder: "demo", ContainerName: "c2", State: "queued"})
	_ = db.CreateSpawn(Spawn{RunID: "run_done", Folder: "demo", ContainerName: "c3", State: "exited"})

	n, err := db.ExpireOrphans()
	if err != nil {
		t.Fatalf("ExpireOrphans: %v", err)
	}
	if n != 2 {
		t.Errorf("expired %d, want 2", n)
	}
	// running/queued are gone from active count
	active, _ := db.ActiveCount()
	if active != 0 {
		t.Errorf("active count = %d after expire, want 0", active)
	}
	// already-terminal row untouched
	sp, _ := db.GetSpawn("run_done")
	if sp.State != "exited" {
		t.Errorf("terminal spawn state = %q, want exited", sp.State)
	}
}

// TestSweepExpired drops aged spawns + expired tokens.
func TestSweepExpired(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_ = db.CreateSpawn(Spawn{RunID: "run_old", Folder: "demo", ContainerName: "c", State: "exited"})
	// negative retention → cutoff in the future, so the just-created row
	// (created_at = now) is strictly older than cutoff and swept.
	if err := db.SweepExpired(-time.Hour); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if _, err := db.GetSpawn("run_old"); err != ErrNotFound {
		t.Fatalf("aged spawn not swept (err=%v)", err)
	}
}

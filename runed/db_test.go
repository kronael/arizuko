package runed

import (
	"testing"
	"time"
)

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
		SessionLogID: logID, MCPTokenJTI: "jti1", SessionID: "sess0", State: "queued",
	}); err != nil {
		t.Fatalf("create spawn: %v", err)
	}
	if err := db.RecordToken("jti1", "run_1", "service:runed", "demo", `["messages:send:own_group"]`,
		time.Now().Add(time.Hour).UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("record token: %v", err)
	}
	if err := db.StartSpawn("run_1", "sess0"); err != nil {
		t.Fatalf("start spawn: %v", err)
	}
	if got := db.ActiveSpawnForFolder("demo"); got != "run_1" {
		t.Fatalf("active spawn=%q want run_1", got)
	}
	if err := db.EndSpawn("run_1", "exited", "ok", 0); err != nil {
		t.Fatalf("end spawn: %v", err)
	}
	if got := db.ActiveSpawnForFolder("demo"); got != "" {
		t.Fatalf("active spawn after end=%q want empty", got)
	}
	if err := db.EndSession(logID, "sess1-new", "ok", "", 3); err != nil {
		t.Fatalf("end session: %v", err)
	}

	sp, err := db.GetSpawn("run_1")
	if err != nil {
		t.Fatalf("get spawn: %v", err)
	}
	if sp.State != "exited" || sp.Outcome != "ok" || sp.MCPTokenJTI != "jti1" {
		t.Fatalf("spawn=%+v", sp)
	}
	sessions, _ := db.RecentSessions("demo", 10)
	if len(sessions) != 1 || sessions[0].SessionID != "sess1-new" || sessions[0].MessageCount != 3 {
		t.Fatalf("sessions=%+v", sessions)
	}
}

// TestTokenUniquePerSpawn enforces one brokered token per spawn (the
// mcp_tokens UNIQUE(run_id) invariant, spec 5/P § brokering).
func TestTokenUniquePerSpawn(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_ = db.CreateSpawn(Spawn{RunID: "run_2", Folder: "demo", ContainerName: "c", State: "queued"})
	exp := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if err := db.RecordToken("jtiA", "run_2", "p", "demo", "[]", exp); err != nil {
		t.Fatalf("first token: %v", err)
	}
	if err := db.RecordToken("jtiB", "run_2", "p", "demo", "[]", exp); err == nil {
		t.Fatal("second token for the same run was accepted (want UNIQUE violation)")
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

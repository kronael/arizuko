package routd

import (
	"os"
	"path/filepath"
	"testing"
)

// newForkLoop builds a Loop on an in-memory DB with a temp groups dir so
// l.folders.GroupPath resolves under it (mirrors gateway_test's testGateway
// for the fork/session-copy path).
func newForkLoop(t *testing.T) (*DB, *Loop) {
	t.Helper()
	db, err := OpenMem()
	if err != nil {
		t.Fatalf("open mem db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	loop := NewLoop(db, nopRunner{}, LoopConfig{GroupsDir: t.TempDir()})
	loop.StopQueue()
	return db, loop
}

// seedParentSession writes a parent topic's session row + its jsonl under the
// group's container-mounted projects dir, returning the projects dir.
func seedParentSession(t *testing.T, l *Loop, db *DB, folder, parentTopic, uuid string, body []byte) string {
	t.Helper()
	if err := db.PutSession(folder, parentTopic, uuid); err != nil {
		t.Fatalf("put parent session: %v", err)
	}
	groupDir, err := l.folders.GroupPath(folder)
	if err != nil {
		t.Fatalf("group path: %v", err)
	}
	projDir := filepath.Join(groupDir, ".claude", "projects", "-home-node")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projDir, uuid+".jsonl"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	return projDir
}

// Spec 6/F (G7): fork_topic copies the parent's Claude Code session jsonl so
// the child resumes with full history. routd's ForkTopic MCP fn wraps
// db.ForkTopic with the cp step (mirrors gateway.forkTopic).
func TestForkTopic_CopiesParentSession(t *testing.T) {
	db, loop := newForkLoop(t)
	folder := "main"

	parentUUID := "parent-uuid"
	parentBody := []byte(`{"type":"summary"}` + "\n")
	projDir := seedParentSession(t, loop, db, folder, "", parentUUID, parentBody)

	// Drive the same logic the MCP ForkTopic fn does: fork the row with a
	// fresh child uuid, then copy the parent session under it.
	childUUID := "child-uuid"
	if err := db.ForkTopic(folder, "", "#deploy", childUUID, false); err != nil {
		t.Fatalf("ForkTopic: %v", err)
	}
	loop.copyParentSession(folder, "", childUUID)

	got, err := os.ReadFile(filepath.Join(projDir, childUUID+".jsonl"))
	if err != nil {
		t.Fatalf("child file not created: %v", err)
	}
	if string(got) != string(parentBody) {
		t.Errorf("child file content drift\nwant: %q\n got: %q", parentBody, got)
	}
}

// Default-fork-from-main path: the first turn on a previously-unseen topic
// creates the lineage row AND copies the parent topic's session, so the child
// resumes from the parent's tail instead of cold (mirrors
// gateway.ensureTopicWithFork). A second call is a no-op.
func TestEnsureTopicWithFork_CopiesParentSession(t *testing.T) {
	db, loop := newForkLoop(t)
	folder := "main"

	parentUUID := "main-uuid"
	projDir := seedParentSession(t, loop, db, folder, "", parentUUID, []byte("parent\n"))

	loop.ensureTopicWithFork(folder, "#deploy", "")

	childUUID := db.SessionID(folder, "#deploy")
	if childUUID == "" {
		t.Fatal("child session row missing")
	}
	got, err := os.ReadFile(filepath.Join(projDir, childUUID+".jsonl"))
	if err != nil {
		t.Fatalf("child file not copied: %v", err)
	}
	if string(got) != "parent\n" {
		t.Errorf("child content drift\nwant: %q\n got: %q", "parent\n", got)
	}

	// Second call must be a no-op: same child uuid, no re-fork (lineage row
	// already exists so EnsureTopicLineage returns inserted=false).
	loop.ensureTopicWithFork(folder, "#deploy", "")
	if got2 := db.SessionID(folder, "#deploy"); got2 != childUUID {
		t.Errorf("second call clobbered session id: %q -> %q", childUUID, got2)
	}
}

// First turn on a fresh folder whose parent (main) has no session yet: the
// lineage row is still created with a fresh uuid, but no jsonl is copied
// (child starts cold without erroring). Guards the brand-new-folder case.
func TestEnsureTopicWithFork_NoParentSessionIsNoOp(t *testing.T) {
	db, loop := newForkLoop(t)
	folder := "main"

	loop.ensureTopicWithFork(folder, "#deploy", "")

	childUUID := db.SessionID(folder, "#deploy")
	if childUUID == "" {
		t.Fatal("child lineage row should still be created")
	}
	groupDir, _ := loop.folders.GroupPath(folder)
	dst := filepath.Join(groupDir, ".claude", "projects", "-home-node", childUUID+".jsonl")
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Errorf("child jsonl created with no parent session: %v", err)
	}
}

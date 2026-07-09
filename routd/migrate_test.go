package routd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kronael/arizuko/core"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

// nopRunner is a Runner that records nothing; checkMigrationVersion only
// appends + enqueues, so the test never drives a turn.
type nopRunner struct{}

func (nopRunner) Run(context.Context, runedv1.RunRequest) (runedv1.RunOutcome, error) {
	return runedv1.RunOutcome{}, nil
}

// newMigrateLoop builds a Loop wired to a temp groups dir + app-src dir with
// the upstream MIGRATION_VERSION set, mirroring gateway_test's setup.
func newMigrateLoop(t *testing.T, upstream string) (*DB, *Loop) {
	t.Helper()
	db, err := OpenMem()
	if err != nil {
		t.Fatalf("open mem db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	appSrc := t.TempDir()
	groups := t.TempDir()
	srcDir := filepath.Join(appSrc, "ant", "skills", "self")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "MIGRATION_VERSION"), []byte(upstream), 0o644); err != nil {
		t.Fatal(err)
	}
	loop := NewLoop(db, nopRunner{}, LoopConfig{GroupsDir: groups, AppSrcDir: appSrc})
	loop.StopQueue() // checkMigrationVersion only writes + enqueues; no dispatch
	return db, loop
}

func writeGroupVersion(t *testing.T, l *Loop, folder, ver string) {
	t.Helper()
	dir := filepath.Join(l.groupsDir, folder, ".claude", "skills", "self")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "MIGRATION_VERSION"), []byte(ver), 0o644); err != nil {
		t.Fatal(err)
	}
}

func hasSystemUpdate(t *testing.T, db *DB, folder string) bool {
	t.Helper()
	msgs, err := db.MessagesSince(folder, "")
	if err != nil {
		t.Fatalf("messages since: %v", err)
	}
	for _, m := range msgs {
		if m.Sender == "system" && strings.HasPrefix(m.Content, "/migrate") &&
			strings.Contains(m.Content, "System update") {
			return true
		}
	}
	return false
}

// TestCheckMigrationVersion: /migrate is now a per-group self-migrate, so EVERY
// group behind upstream is enqueued — the top-level world AND its behind child
// (no version file → reads 0). No root fan-out.
func TestCheckMigrationVersion(t *testing.T) {
	db, loop := newMigrateLoop(t, "55\n")

	_ = db.PutGroup(core.Group{Folder: "myworld"})
	writeGroupVersion(t, loop, "myworld", "54\n")
	_ = db.PutGroup(core.Group{Folder: "myworld/child"}) // no version file → 0, behind

	loop.checkMigrationVersion()

	if !hasSystemUpdate(t, db, "myworld") {
		t.Error("expected auto-migration /migrate message in myworld")
	}
	// The child is behind too → it migrates ITSELF (per-group), no longer skipped.
	if !hasSystemUpdate(t, db, "myworld/child") {
		t.Error("expected per-group /migrate message in the behind child")
	}
}

// TestCheckMigrationVersion_UpToDateChildSkipped: a child already at the upstream
// version gets no /migrate even when its parent is behind — per-group, each
// group is judged on its OWN on-disk version.
func TestCheckMigrationVersion_UpToDateChildSkipped(t *testing.T) {
	db, loop := newMigrateLoop(t, "55\n")

	_ = db.PutGroup(core.Group{Folder: "myworld"})
	writeGroupVersion(t, loop, "myworld", "54\n")
	_ = db.PutGroup(core.Group{Folder: "myworld/child"})
	writeGroupVersion(t, loop, "myworld/child", "55\n") // current → skipped

	loop.checkMigrationVersion()

	if !hasSystemUpdate(t, db, "myworld") {
		t.Error("expected /migrate in the behind parent")
	}
	if hasSystemUpdate(t, db, "myworld/child") {
		t.Error("current child should NOT be enqueued")
	}
}

// TestCheckMigrationVersion_UpToDate: a group at the upstream version gets no
// /migrate. Mirrors gateway_test.TestCheckMigrationVersion_UpToDate.
func TestCheckMigrationVersion_UpToDate(t *testing.T) {
	db, loop := newMigrateLoop(t, "55\n")

	_ = db.PutGroup(core.Group{Folder: "uptodate"})
	writeGroupVersion(t, loop, "uptodate", "55\n")

	loop.checkMigrationVersion()

	if hasSystemUpdate(t, db, "uptodate") {
		t.Error("should not trigger /migrate when up to date")
	}
}

// TestCheckMigrationVersion_NoVersionFile: a group with no on-disk version
// file reads as 0 and is behind → it gets a /migrate. Mirrors
// gateway_test.TestCheckMigrationVersion_NoVersionFile.
func TestCheckMigrationVersion_NoVersionFile(t *testing.T) {
	db, loop := newMigrateLoop(t, "10\n")

	_ = db.PutGroup(core.Group{Folder: "fresh"})

	loop.checkMigrationVersion()

	if !hasSystemUpdate(t, db, "fresh") {
		t.Error("expected /migrate for a group with no version file")
	}
}

// TestCheckMigrationVersion_NoAppSrc: empty AppSrcDir disables the check.
func TestCheckMigrationVersion_NoAppSrc(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatalf("open mem db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	loop := NewLoop(db, nopRunner{}, LoopConfig{GroupsDir: t.TempDir()})
	loop.StopQueue()
	_ = db.PutGroup(core.Group{Folder: "myworld"})

	loop.checkMigrationVersion()

	if hasSystemUpdate(t, db, "myworld") {
		t.Error("no AppSrcDir should disable the auto-migrate check")
	}
}

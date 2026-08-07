package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kronael/arizuko/store"
	_ "modernc.org/sqlite"
)

// TestCreateSeedsRoutdDBNotMonolith proves `arizuko create` births a split
// instance: the default `main` group and its scheduled_tasks land in routd.db
// (where routd reads them) and NO messages.db is created at all. create used to
// seed the frozen monolith, so a fresh instance had `main` in a DB no daemon
// opens — and shipped a retired messages.db in every new store/ (BUGS.md Q1).
func TestCreateSeedsRoutdDBNotMonolith(t *testing.T) {
	base := t.TempDir()
	t.Setenv("ARIZUKO_DATA_DIR", base)
	t.Setenv("DATA_DIR", "")

	cmdCreate([]string{"freshinst"})

	storeDir := filepath.Join(base, "arizuko_freshinst", "store")
	if _, err := os.Stat(filepath.Join(storeDir, "messages.db")); err == nil {
		t.Fatalf("messages.db exists at %s — create must not birth the retired monolith", storeDir)
	}

	s, err := store.OpenRoutd(storeDir)
	if err != nil {
		t.Fatalf("OpenRoutd (create must migrate routd.db): %v", err)
	}
	defer s.Close()

	if _, ok := s.AllGroups()["main"]; !ok {
		t.Errorf("default group `main` missing from routd.db; got %v", keysOf(s.AllGroups()))
	}
	if n := countRows(t, storeDir, store.OwnerRoutd,
		"SELECT COUNT(*) FROM scheduled_tasks WHERE owner='main'"); n == 0 {
		t.Error("routd.db scheduled_tasks for `main` = 0, want the seeded defaults")
	}
}

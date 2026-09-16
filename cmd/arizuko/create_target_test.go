package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/store"
)

// A fresh instance is born with the `main` group and its scheduled tasks. The
// two halves have two writers: `arizuko create` makes the instance's FILES, and
// routd's own boot writes the rows through store.SeedDefaultGroup — the store
// server is brought up AFTER create, so create has no endpoint to dial (BUGS
// L300). This drives both in that order and asserts the instance they leave.
func TestAFreshInstanceIsBornWithMain(t *testing.T) {
	t.Cleanup(func() { dropInstance(t) })
	base := t.TempDir()
	t.Setenv("ARIZUKO_DATA_DIR", base)
	t.Setenv("DATA_DIR", "")

	cmdCreate([]string{"freshinst"})

	// create's half: the instance directory, and its store/ for the server's
	// own volumes.
	storeDir := filepath.Join(base, "arizuko_freshinst", "store")
	if _, err := os.Stat(storeDir); err != nil {
		t.Fatalf("create left no store dir for the server's volumes: %v", err)
	}

	// routd's half: its own boot writes the rows.
	db, err := routd.Open()
	if err != nil {
		t.Fatalf("routd.Open (the owner daemon makes the rows): %v", err)
	}
	defer db.Close() //nolint:errcheck // the test is ending
	s := store.NewOver(db.Obj())

	if _, ok := s.AllGroups()["main"]; !ok {
		t.Errorf("default group `main` missing; got %v", keysOf(s.AllGroups()))
	}
	if n := len(s.ListTasks("main", false)); n == 0 {
		t.Error("scheduled_tasks for `main` = 0, want the seeded defaults")
	}
}

package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// openOwnedDB never creates onbod.db. sql.Open is lazy — it validates the
// driver and returns without touching the file — so onbod's "a failure to open
// is fatal (no silent empty-DB cross-read)" branch could not fire, and a wrong
// ONBOD_DB_PATH produced a migrated DB with zero invites and zero gates on a
// green boot (BUGS F52, spec 5/16 step 7).
func TestOpenOwnedDBRefusesToManufactureAnEmptyDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store", "onbod.db")
	db, err := openOwnedDB(path)
	if err == nil {
		db.Close()
		t.Fatal("openOwnedDB created onbod.db; every invite and gate would silently be gone")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error must name the missing path, got: %v", err)
	}

	// Seeded (as `arizuko create` does) it opens and migrates normally.
	mustSeedDB(t, path)
	db, err = openOwnedDB(path)
	if err != nil {
		t.Fatalf("openOwnedDB after seed: %v", err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(
		"SELECT 1 FROM sqlite_master WHERE type='table' AND name='invites'").Scan(&n); err != nil {
		t.Errorf("invites table not migrated into the seeded file: %v", err)
	}
}

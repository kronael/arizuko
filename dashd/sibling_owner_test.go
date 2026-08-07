package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kronael/arizuko/db_utils"
	"github.com/kronael/arizuko/store"
)

// dashd reads onbod's and runed's DBs from CONFIGURED env, defaulting to this
// instance's own store/<owner>/ layout. It used to derive them as siblings of
// its own DSN — under per-owner subdirectories a sibling is not a sibling, and
// the derivation is what CLAUDE.md bans (spec 5/16 step 7).
func TestOpenSiblingOwnerDefaultsToTheOwnerSubdir(t *testing.T) {
	dataDir := t.TempDir()
	want := store.OwnerDBPath(filepath.Join(dataDir, "store"), store.OwnerOnbod)
	if err := db_utils.CreateDBFile(want); err != nil {
		t.Fatal(err)
	}
	// The flat path the derivation used to produce must NOT be what it opens.
	flat := filepath.Join(dataDir, "store", "onbod.db")
	if _, err := os.Stat(flat); !os.IsNotExist(err) {
		t.Fatalf("fixture leaked a flat onbod.db (err=%v)", err)
	}

	db := openSiblingOwner("ONBOD_DB_PATH", dataDir, store.OwnerOnbod)
	if db == nil {
		t.Fatal("openSiblingOwner returned nil for a seeded store/onbod/onbod.db")
	}
	db.Close()
}

func TestOpenSiblingOwnerHonoursTheEnvOverride(t *testing.T) {
	elsewhere := filepath.Join(t.TempDir(), "somewhere", "runed.db")
	if err := db_utils.CreateDBFile(elsewhere); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RUNED_DB_PATH", elsewhere)
	// dataDir points at a tree with no runed.db at all: only the env can win.
	db := openSiblingOwner("RUNED_DB_PATH", t.TempDir(), store.OwnerRuned)
	if db == nil {
		t.Fatal("RUNED_DB_PATH was ignored")
	}
	db.Close()
}

// An absent owner DB means that daemon's profile is off — /dash/runed/ and the
// invites page already banner "store unavailable" — so nil is the honest
// answer rather than a boot failure that takes the whole console down.
func TestOpenSiblingOwnerReturnsNilWhenTheProfileIsOff(t *testing.T) {
	if db := openSiblingOwner("ONBOD_DB_PATH", t.TempDir(), store.OwnerOnbod); db != nil {
		db.Close()
		t.Error("openSiblingOwner invented a handle for a database that does not exist")
	}
}

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/kronael/arizuko/db_utils"
	"github.com/kronael/arizuko/objstore/sqlstore"
	"github.com/kronael/arizuko/store"
)

// seedOnbodFile makes onbod's database at path and builds what onbod serves
// there, through store.PrepareOwner — the one declaration of onbod's entities,
// so the fixture cannot hold a set the daemon does not.
func seedOnbodFile(t *testing.T, path string) {
	t.Helper()
	if err := db_utils.CreateDBFile(path); err != nil {
		t.Fatal(err)
	}
	db, err := db_utils.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.PrepareOwner(context.Background(), sqlstore.New(db), store.OwnerOnbod); err != nil {
		t.Fatalf("build what onbod serves at %s: %v", path, err)
	}
}

// dashd reads onbod's store from CONFIGURED env, defaulting to this instance's
// own store/<owner>/ layout. It used to derive it as a sibling of its own DSN —
// under per-owner subdirectories a sibling is not a sibling, and the derivation
// is what CLAUDE.md bans (spec 5/16 step 7).
func TestOpenSiblingOwnerDefaultsToTheOwnerSubdir(t *testing.T) {
	dataDir := t.TempDir()
	want := store.OwnerDBPath(filepath.Join(dataDir, "store"), store.OwnerOnbod)
	seedOnbodFile(t, want)
	// The flat path the derivation used to produce must NOT be what it opens.
	flat := filepath.Join(dataDir, "store", "onbod.db")
	if _, err := os.Stat(flat); !os.IsNotExist(err) {
		t.Fatalf("fixture leaked a flat onbod.db (err=%v)", err)
	}

	s := openSiblingOwner("ONBOD_DB_PATH", dataDir, store.OwnerOnbod)
	if s == nil {
		t.Fatal("openSiblingOwner returned nil for a seeded store/onbod/onbod.db")
	}
	s.Close()
}

func TestOpenSiblingOwnerHonoursTheEnvOverride(t *testing.T) {
	elsewhere := filepath.Join(t.TempDir(), "somewhere", "onbod.db")
	seedOnbodFile(t, elsewhere)
	t.Setenv("ONBOD_DB_PATH", elsewhere)
	// dataDir points at a tree with no onbod.db at all: only the env can win.
	s := openSiblingOwner("ONBOD_DB_PATH", t.TempDir(), store.OwnerOnbod)
	if s == nil {
		t.Fatal("ONBOD_DB_PATH was ignored")
	}
	s.Close()
}

// An absent owner store means that daemon's profile is off — the invites page
// already banners "store unavailable" — so nil is the honest answer rather than
// a boot failure that takes the whole console down.
func TestOpenSiblingOwnerReturnsNilWhenTheProfileIsOff(t *testing.T) {
	if s := openSiblingOwner("ONBOD_DB_PATH", t.TempDir(), store.OwnerOnbod); s != nil {
		s.Close()
		t.Error("openSiblingOwner invented a store for a database that does not exist")
	}
}

// A database that is THERE but holds none of onbod's tables is the same answer
// and not a crash: an empty file is what a wrong ONBOD_DB_PATH points at, and
// serving an empty invites page off it would read as "no invites".
func TestOpenSiblingOwnerRefusesADatabaseThatIsNotTheOwners(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store", store.OwnerOnbod, "onbod.db")
	if err := db_utils.CreateDBFile(path); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ONBOD_DB_PATH", path)
	if s := openSiblingOwner("ONBOD_DB_PATH", "", store.OwnerOnbod); s != nil {
		s.Close()
		t.Error("openSiblingOwner answered for a database holding none of onbod's tables")
	}
}

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kronael/arizuko/db_utils"
	"github.com/kronael/arizuko/store"
)

// bootstrapOnbodDB creates a migrated onbod.db in dir through bootstrapOnbod —
// the same copy-target bootstrap migrate-split uses — so mustOpenOnbod's split
// branch (OpenOnbod) finds the invites entity.
func bootstrapOnbodDB(t *testing.T, dir string) {
	t.Helper()
	path := store.OwnerDBPath(dir, store.OwnerOnbod)
	if err := db_utils.CreateDBFile(path); err != nil {
		t.Fatalf("seed onbod.db: %v", err)
	}
	db, err := db_utils.Open(path)
	if err != nil {
		t.Fatalf("open onbod.db: %v", err)
	}
	defer db.Close()
	if err := bootstrapOnbod(db); err != nil {
		t.Fatalf("onbod.db schema: %v", err)
	}
}

// TestInviteLandsInOnbodDB proves the CLI invite writer (mustOpenOnbod →
// OpenOnbod) lands in onbod.db when it exists (split) and leaves messages.db's
// invites table empty.
func TestInviteLandsInOnbodDB(t *testing.T) {
	dir := t.TempDir()
	// messages.db migrated (the monolith we must NOT write).
	ms, err := store.Open(dir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	ms.Close()
	bootstrapOnbodDB(t, dir)

	s := mustOpenOnbodDir(t, dir)
	if _, _, err := s.CreateInvite("main/", "cli", 3, nil); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if err := s.PutGate("*", 10); err != nil {
		t.Fatalf("PutGate: %v", err)
	}
	s.Close()

	if n := countInvites(t, dir, store.OwnerOnbod, "main/"); n != 1 {
		t.Errorf("onbod.db invites = %d, want 1", n)
	}
	if n := countGates(t, dir, store.OwnerOnbod, "*"); n != 1 {
		t.Errorf("onbod.db gates = %d, want 1", n)
	}
	// messages.db must be untouched by the CLI invite path.
	if n := countInvites(t, dir, "messages.db", ""); n != 0 {
		t.Errorf("messages.db invites = %d, want 0 (CLI must not write the monolith)", n)
	}
	if n := countGates(t, dir, "messages.db", ""); n != 0 {
		t.Errorf("messages.db gates = %d, want 0", n)
	}
}

// countInvites and countGates answer what ONE database holds, through the
// methods the daemons themselves call.
//
// They replace a COUNT(*) with a WHERE on a column. Both tables are entities, so
// there is no column to filter on and the item's own attributes are inside a
// document — but the point of these tests is WHICH FILE the write landed in, and
// a read through the real reader answers that and also proves the row came back
// as the row the caller wrote.
func countInvites(t *testing.T, dir, owner, targetGlob string) int {
	t.Helper()
	s := openOnbodFile(t, dir, owner)
	defer s.Close() //nolint:errcheck // the test ends either way
	rows, err := s.ListInvites("")
	if err != nil {
		t.Fatalf("ListInvites: %v", err)
	}
	n := 0
	for _, r := range rows {
		if targetGlob == "" || r.TargetGlob == targetGlob {
			n++
		}
	}
	return n
}

func countGates(t *testing.T, dir, owner, gate string) int {
	t.Helper()
	s := openOnbodFile(t, dir, owner)
	defer s.Close() //nolint:errcheck // the test ends either way
	rows, err := s.ListGates()
	if err != nil {
		t.Fatalf("ListGates: %v", err)
	}
	n := 0
	for _, r := range rows {
		if gate == "" || r.Gate == gate {
			n++
		}
	}
	return n
}

// openOnbodFile opens ONE file by name — an owner key or a bare filename — so a
// test can assert about the monolith and the split file separately.
func openOnbodFile(t *testing.T, dir, owner string) *store.Store {
	t.Helper()
	file := filepath.Join(dir, owner)
	if _, ok := store.OwnerDBs[owner]; ok {
		file = store.OwnerDBPath(dir, owner)
	}
	db, err := db_utils.Open(file)
	if err != nil {
		t.Fatalf("open %s: %v", file, err)
	}
	return store.New(db)
}

// TestInviteMonolithFallsBackToMessages proves the dual-path: with no onbod.db
// present, the CLI invite writer falls back to messages.db (monolith).
func TestInviteMonolithFallsBackToMessages(t *testing.T) {
	dir := t.TempDir()
	ms, err := store.Open(dir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	ms.Close()
	// no onbod.db → fallback path.

	s := mustOpenOnbodDir(t, dir)
	if _, _, err := s.CreateInvite("main/", "cli", 1, nil); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	s.Close()

	if n := countInvites(t, dir, "messages.db", "main/"); n != 1 {
		t.Errorf("messages.db invites = %d, want 1 (monolith fallback)", n)
	}
}

// mustOpenOnbodDir applies mustOpenOnbod's dual-path predicate directly (onbod.db
// present → OpenOnbod, else store.Open). mustOpenOnbod itself appends "store" to a
// dataDir and dies on failure; here the test dir IS the store dir, so we mirror
// the same branch with a *testing.T fatal instead.
func mustOpenOnbodDir(t *testing.T, storeDir string) *store.Store {
	t.Helper()
	if _, err := os.Stat(store.OwnerDBPath(storeDir, store.OwnerOnbod)); err == nil {
		s, oerr := store.OpenOnbod(storeDir)
		if oerr != nil {
			t.Fatalf("OpenOnbod: %v", oerr)
		}
		return s
	}
	s, err := store.Open(storeDir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	return s
}

package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/kronael/arizuko/store"
	_ "modernc.org/sqlite"
)

// bootstrapOnbodDB creates a migrated onbod.db in dir by running onbodSchema
// (the same copy-target bootstrap migrate-split uses), so mustOpenOnbod's split
// branch (OpenOnbod) finds the invites table.
func bootstrapOnbodDB(t *testing.T, dir string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, "onbod.db"))
	if err != nil {
		t.Fatalf("open onbod.db: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(onbodSchema); err != nil {
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
	if _, err := s.CreateInvite("main/", "cli", 3, nil); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if err := s.PutGate("*", 10); err != nil {
		t.Fatalf("PutGate: %v", err)
	}
	s.Close()

	if n := countRows(t, dir, "onbod.db",
		"SELECT COUNT(*) FROM invites WHERE target_glob='main/'"); n != 1 {
		t.Errorf("onbod.db invites = %d, want 1", n)
	}
	if n := countRows(t, dir, "onbod.db",
		"SELECT COUNT(*) FROM onboarding_gates WHERE gate='*'"); n != 1 {
		t.Errorf("onbod.db gates = %d, want 1", n)
	}
	// messages.db must be untouched by the CLI invite path.
	if n := countRows(t, dir, "messages.db",
		"SELECT COUNT(*) FROM invites"); n != 0 {
		t.Errorf("messages.db invites = %d, want 0 (CLI must not write the monolith)", n)
	}
	if n := countRows(t, dir, "messages.db",
		"SELECT COUNT(*) FROM onboarding_gates"); n != 0 {
		t.Errorf("messages.db gates = %d, want 0", n)
	}
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
	if _, err := s.CreateInvite("main/", "cli", 1, nil); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	s.Close()

	if n := countRows(t, dir, "messages.db",
		"SELECT COUNT(*) FROM invites WHERE target_glob='main/'"); n != 1 {
		t.Errorf("messages.db invites = %d, want 1 (monolith fallback)", n)
	}
}

// mustOpenOnbodDir applies mustOpenOnbod's dual-path predicate directly (onbod.db
// present → OpenOnbod, else store.Open). mustOpenOnbod itself appends "store" to a
// dataDir and dies on failure; here the test dir IS the store dir, so we mirror
// the same branch with a *testing.T fatal instead.
func mustOpenOnbodDir(t *testing.T, storeDir string) *store.Store {
	t.Helper()
	if _, err := os.Stat(filepath.Join(storeDir, "onbod.db")); err == nil {
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

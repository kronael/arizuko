package main

import (
	"bytes"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/store"
	_ "modernc.org/sqlite"
)

// setupSplitStore creates BOTH a migrated routd.db (routd owns acl/secrets in the
// split) and a migrated messages.db in dir, returning the dir. The CLI grant/
// secret writers must land in routd.db; messages.db must stay untouched.
func setupSplitStore(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// routd.Open migrates routd.db (creates acl/secrets/... tables).
	rd, err := routd.Open(dir)
	if err != nil {
		t.Fatalf("routd.Open: %v", err)
	}
	rd.Close()
	// store.Open migrates messages.db (the monolith we must NOT write).
	ms, err := store.Open(dir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	ms.Close()
	return dir
}

// countRows opens a raw handle on dir/<file> (the store dir where routd.Open /
// store.Open placed the db files) and returns the COUNT(*) of q.
func countRows(t *testing.T, dir, file, q string, args ...any) int {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, file))
	if err != nil {
		t.Fatalf("open %s: %v", file, err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("count %s (%s): %v", file, q, err)
	}
	return n
}

// TestGrantLandsInRoutdDB proves `arizuko grant` (runGrant via mustOpenACL/
// OpenRoutd) writes the acl row into routd.db and leaves messages.db empty.
func TestGrantLandsInRoutdDB(t *testing.T) {
	dir := setupSplitStore(t)

	s, err := store.OpenRoutd(dir)
	if err != nil {
		t.Fatalf("OpenRoutd: %v", err)
	}
	var out bytes.Buffer
	if err := runGrant(s, "github:42", "main", &out); err != nil {
		t.Fatalf("runGrant: %v", err)
	}
	if err := runGrant(s, "github:42", "**", &out); err != nil {
		t.Fatalf("operator grant: %v", err)
	}
	s.Close()

	if n := countRows(t, dir, "routd.db",
		"SELECT COUNT(*) FROM acl WHERE principal='github:42'"); n != 1 {
		t.Errorf("routd.db acl rows = %d, want 1", n)
	}
	if n := countRows(t, dir, "routd.db",
		"SELECT COUNT(*) FROM acl_membership WHERE child='github:42' AND parent='role:operator'"); n != 1 {
		t.Errorf("routd.db membership rows = %d, want 1", n)
	}
	// messages.db must be untouched by the CLI grant path.
	if n := countRows(t, dir, "messages.db",
		"SELECT COUNT(*) FROM acl WHERE principal='github:42'"); n != 0 {
		t.Errorf("messages.db acl rows = %d, want 0 (CLI must not write the monolith)", n)
	}
	if n := countRows(t, dir, "messages.db",
		"SELECT COUNT(*) FROM acl_membership WHERE child='github:42'"); n != 0 {
		t.Errorf("messages.db membership rows = %d, want 0", n)
	}
}

// TestUngrantRemovesFromRoutdDB proves the CLI revoke path deletes from routd.db.
func TestUngrantRemovesFromRoutdDB(t *testing.T) {
	dir := setupSplitStore(t)

	s, err := store.OpenRoutd(dir)
	if err != nil {
		t.Fatalf("OpenRoutd: %v", err)
	}
	var out bytes.Buffer
	runGrant(s, "u1", "main", &out)
	runGrant(s, "u1", "**", &out)
	if err := runUngrant(s, "u1", "main", &out); err != nil {
		t.Fatalf("runUngrant: %v", err)
	}
	if err := runUngrant(s, "u1", "**", &out); err != nil {
		t.Fatalf("runUngrant operator: %v", err)
	}
	s.Close()

	if n := countRows(t, dir, "routd.db",
		"SELECT COUNT(*) FROM acl WHERE principal='u1'"); n != 0 {
		t.Errorf("routd.db acl rows after ungrant = %d, want 0", n)
	}
	if n := countRows(t, dir, "routd.db",
		"SELECT COUNT(*) FROM acl_membership WHERE child='u1'"); n != 0 {
		t.Errorf("routd.db membership rows after ungrant = %d, want 0", n)
	}
}

// TestSecretLandsInRoutdDB proves `arizuko secret set` (runSecretSet via
// PutSecretRow) writes into routd.db, not messages.db.
func TestSecretLandsInRoutdDB(t *testing.T) {
	dir := setupSplitStore(t)

	s, err := store.OpenRoutd(dir)
	if err != nil {
		t.Fatalf("OpenRoutd: %v", err)
	}
	var out bytes.Buffer
	if err := runSecretSet(s, store.ScopeFolder, "main", "GITHUB_TOKEN", "ghp-xyz", &out); err != nil {
		t.Fatalf("runSecretSet: %v", err)
	}
	s.Close()

	if n := countRows(t, dir, "routd.db",
		"SELECT COUNT(*) FROM secrets WHERE scope_kind='folder' AND scope_id='main' AND key='GITHUB_TOKEN'"); n != 1 {
		t.Errorf("routd.db secret rows = %d, want 1", n)
	}
	if n := countRows(t, dir, "messages.db",
		"SELECT COUNT(*) FROM secrets WHERE key='GITHUB_TOKEN'"); n != 0 {
		t.Errorf("messages.db secret rows = %d, want 0 (CLI must not write the monolith)", n)
	}

	// Delete path also targets routd.db.
	s2, err := store.OpenRoutd(dir)
	if err != nil {
		t.Fatalf("OpenRoutd: %v", err)
	}
	if err := runSecretDelete(s2, store.ScopeFolder, "main", "GITHUB_TOKEN", &out); err != nil {
		t.Fatalf("runSecretDelete: %v", err)
	}
	s2.Close()
	if n := countRows(t, dir, "routd.db",
		"SELECT COUNT(*) FROM secrets WHERE key='GITHUB_TOKEN'"); n != 0 {
		t.Errorf("routd.db secret rows after delete = %d, want 0", n)
	}
}

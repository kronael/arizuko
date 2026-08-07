package routd

import (
	"os"
	"path/filepath"
	"testing"
)

// Open never creates routd.db. Without this, a routd pointed at the wrong
// store/<owner>/ — a typo'd mount, an unfinished file move — migrates a fresh
// file and serves a fully-formed instance with zero groups, zero acl and zero
// secrets, reporting healthy the whole time (spec 5/16 step 7).
func TestOpenRefusesToManufactureAnEmptyInstance(t *testing.T) {
	dir := t.TempDir()
	if _, err := Open(dir); err == nil {
		t.Fatal("Open created routd.db on an empty dir; that is a green boot on an empty instance")
	}
	if _, err := os.Stat(filepath.Join(dir, "routd.db")); !os.IsNotExist(err) {
		t.Errorf("Open left a routd.db behind (err=%v)", err)
	}

	// Create is the one entry point that may make it, and Open takes over after.
	db, err := Create(dir)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	db.Close()
	db2, err := Open(dir)
	if err != nil {
		t.Fatalf("Open after Create: %v", err)
	}
	db2.Close()
}

// TestOpenNoSiblingDB asserts routd.Create makes ONLY routd.db in the store dir
// and opens NO sibling messages.db — the last sibling-read (pane_sessions) moved
// to routd's own DB (migration 0010). routd is now self-contained: every table
// it reads is in routd.db; cross-daemon data arrives over HTTP (authd/runed).
func TestOpenNoSiblingDB(t *testing.T) {
	dir := t.TempDir()
	db, err := Create(dir)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer db.Close()

	// routd.db exists; messages.db must NOT (routd never opens or creates it).
	if _, err := os.Stat(filepath.Join(dir, "routd.db")); err != nil {
		t.Fatalf("routd.db not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "messages.db")); !os.IsNotExist(err) {
		t.Errorf("messages.db must not exist — routd opens NO sibling DB (err=%v)", err)
	}

	// pane_sessions lives in routd.db now (migration 0010), readable by the
	// owner's own handle.
	var name string
	if err := db.SQL().QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='pane_sessions'`).Scan(&name); err != nil {
		t.Fatalf("pane_sessions table missing from routd.db: %v", err)
	}
}

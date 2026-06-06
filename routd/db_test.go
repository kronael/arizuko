package routd

import (
	"os"
	"path/filepath"
	"testing"
)

// TestOpenNoSiblingDB asserts routd.Open creates ONLY routd.db in the store dir
// and opens NO sibling messages.db — the last sibling-read (pane_sessions) moved
// to routd's own DB (migration 0010). routd is now self-contained: every table
// it reads is in routd.db; cross-daemon data arrives over HTTP (authd/runed).
func TestOpenNoSiblingDB(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
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

package db_utils

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The precondition the store/<owner>/ split rests on. SQLite creates a missing
// file silently and Migrate then fills it with a complete empty schema, so an
// owner daemon pointed at the wrong path — a typo'd mount, a store/<owner>/
// move that never happened — would boot GREEN on a fully-migrated instance
// holding none of the operator's data (spec 5/16 step 7).
func TestRequireDBFileRefusesAMissingDatabase(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "routd", "routd.db")
	err := RequireDBFile(missing)
	if err == nil {
		t.Fatal("RequireDBFile accepted a path with no database; a daemon would migrate an empty one there")
	}
	// The operator reads this in journalctl and has to know which path to fix.
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("error must name the missing path, got: %v", err)
	}
	if _, statErr := os.Stat(missing); !os.IsNotExist(statErr) {
		t.Error("RequireDBFile created the file it was supposed to refuse")
	}
}

func TestRequireDBFileAcceptsAnExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routd", "routd.db")
	if err := CreateDBFile(path); err != nil {
		t.Fatalf("CreateDBFile: %v", err)
	}
	if err := RequireDBFile(path); err != nil {
		t.Errorf("RequireDBFile rejected a seeded database: %v", err)
	}
}

// CreateDBFile is the seam `arizuko create` uses instead of linking authd's and
// onbod's migration sets (both are package main): a zero-byte file IS a valid
// empty SQLite database, so the owner daemon migrates its own schema into it.
func TestCreateDBFileSeedsAnEmptyFileAndKeepsExistingBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "onbod", "onbod.db")
	if err := CreateDBFile(path); err != nil {
		t.Fatalf("CreateDBFile: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after create: %v", err)
	}
	if fi.Size() != 0 {
		t.Errorf("seeded file should be empty, got %d bytes", fi.Size())
	}

	// Idempotent: `arizuko create` re-run, or a seed step racing a live DB, must
	// never truncate an instance's data.
	if err := os.WriteFile(path, []byte("not-actually-empty"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CreateDBFile(path); err != nil {
		t.Fatalf("CreateDBFile on existing: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "not-actually-empty" {
		t.Errorf("CreateDBFile clobbered an existing database: %q", got)
	}
}

package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/kronael/arizuko/db_utils"
)

// auth.db lives under store/ (alongside routd.db/runed.db/messages.db) so a
// single store/ chown to the container uid makes every daemon's DB writable on
// a fresh root-owned data dir. A regression that put auth.db back at the data
// dir root would reintroduce the SQLITE_CANTOPEN first-boot failure.
func TestResolveDSN_UnderStore(t *testing.T) {
	dir := t.TempDir()
	want := filepath.Join(dir, "store", "auth.db")
	if err := db_utils.CreateDBFile(want); err != nil {
		t.Fatal(err)
	}
	dsn, err := resolveDSN("", dir)
	if err != nil {
		t.Fatalf("resolveDSN: %v", err)
	}
	if dsn != want {
		t.Errorf("dsn = %q, want %q", dsn, want)
	}
}

// authd must never create auth.db: a wrong DATA_DIR (an unmounted store/authd/,
// an unfinished move) would otherwise migrate a fresh file, mint NEW signing
// keys, and invalidate every live session while /health stayed green
// (spec 5/16 step 7).
func TestResolveDSN_MissingAuthDBFailsLoud(t *testing.T) {
	_, err := resolveDSN("", t.TempDir())
	if err == nil {
		t.Fatal("resolveDSN accepted a data dir with no auth.db; it must fail loud")
	}
	if !strings.Contains(err.Error(), "auth.db") {
		t.Errorf("error must name the missing file, got: %v", err)
	}
}

func TestResolveDSN_ExplicitDatabaseWins(t *testing.T) {
	dsn, err := resolveDSN("/custom/auth.db", "/ignored")
	if err != nil {
		t.Fatalf("resolveDSN: %v", err)
	}
	if dsn != "/custom/auth.db" {
		t.Errorf("dsn = %q, want explicit DATABASE", dsn)
	}
}

func TestResolveDSN_RequiresPath(t *testing.T) {
	if _, err := resolveDSN("", ""); err == nil {
		t.Error("expected error when both DATABASE and DATA_DIR are empty")
	}
}

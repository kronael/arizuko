package main

import (
	"os"
	"path/filepath"
	"testing"
)

// auth.db lives under store/ (alongside routd.db/runed.db/messages.db) so a
// single store/ chown to the container uid makes every daemon's DB writable on
// a fresh root-owned data dir. A regression that put auth.db back at the data
// dir root would reintroduce the SQLITE_CANTOPEN first-boot failure.
func TestResolveDSN_UnderStore(t *testing.T) {
	dir := t.TempDir()
	dsn, err := resolveDSN("", dir)
	if err != nil {
		t.Fatalf("resolveDSN: %v", err)
	}
	want := filepath.Join(dir, "store", "auth.db")
	if dsn != want {
		t.Errorf("dsn = %q, want %q", dsn, want)
	}
	if fi, err := os.Stat(filepath.Join(dir, "store")); err != nil || !fi.IsDir() {
		t.Errorf("store/ not created: stat err=%v", err)
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

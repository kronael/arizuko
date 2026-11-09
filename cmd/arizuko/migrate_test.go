package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMigrateInstanceEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DATA_DIR", dir)
	t.Setenv("MAX_CONCURRENT_CONTAINERS", "")
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("MAX_CONCURRENT_CONTAINERS=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmdMigrate([]string{""})
	body, err := os.ReadFile(path)
	if err != nil || string(body) != "MAX_CONCURRENT_AGENTS=1\n" {
		t.Fatalf("migrated env=%q err=%v", body, err)
	}
}

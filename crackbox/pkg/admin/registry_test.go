package admin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPersistentRegistryRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	r1, err := NewPersistentRegistry(path)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	r1.Set("10.99.0.5", "acme/eng", []string{"github.com", "anthropic.com"})
	r1.Set("10.99.0.6", "solo/inbox", []string{"npmjs.org"})
	r1.Remove("10.99.0.5")

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("state file not written: %v", err)
	}

	r2, err := NewPersistentRegistry(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if id, list, ok := r2.Lookup("10.99.0.6"); !ok || id != "solo/inbox" || len(list) != 1 || list[0] != "npmjs.org" {
		t.Errorf("reloaded entry mismatch: id=%q list=%v ok=%v", id, list, ok)
	}
	if _, _, ok := r2.Lookup("10.99.0.5"); ok {
		t.Errorf("removed entry should not reload")
	}
}

func TestPersistentRegistryMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist.json")

	r, err := NewPersistentRegistry(path)
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if _, _, ok := r.Lookup("anything"); ok {
		t.Errorf("expected empty registry")
	}
}

func TestPersistentRegistryCorrupt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	r, err := NewPersistentRegistry(path)
	if err != nil {
		t.Fatalf("corrupt should warn-and-continue, not fail: %v", err)
	}
	// Write should succeed, replacing the bad file.
	r.Set("10.99.0.7", "x", []string{"a.com"})
	r2, err := NewPersistentRegistry(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, _, ok := r2.Lookup("10.99.0.7"); !ok {
		t.Errorf("post-corrupt write did not persist")
	}
}

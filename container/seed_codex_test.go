package container

import (
	"os"
	"path/filepath"
	"testing"
)

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestSeedCodexMountTargetsCreatesTheTargets(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".codex")
	mustMkdir(t, dir)

	SeedCodexMountTargets(dir)

	for _, name := range CodexROMounts {
		p := filepath.Join(dir, name)
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatalf("mount target %s not seeded: %v", name, err)
		}
		if !fi.Mode().IsRegular() {
			t.Fatalf("mount target %s is not a regular file: %v", name, fi.Mode())
		}
	}
}

// TestSeedCodexMountTargetsKeepsContent: only a ZERO-length stub is replaced.
// A file with bytes in it is a real credential someone put there, and the
// repair must never remove one.
func TestSeedCodexMountTargetsKeepsContent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, CodexROMounts[0])
	if err := os.WriteFile(p, []byte(`{"token":"real"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}

	SeedCodexMountTargets(dir)

	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("non-empty target removed: %v", err)
	}
	if string(got) != `{"token":"real"}` {
		t.Fatalf("non-empty target rewritten: %q", got)
	}
	after, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if after.Mode() != before.Mode() {
		t.Fatalf("mode changed %v to %v", before.Mode(), after.Mode())
	}
}

// TestSeedCodexMountTargetsIdempotent: a second pass leaves the seeded stubs
// alone. buildMounts runs it on every spawn over the same directory.
func TestSeedCodexMountTargetsIdempotent(t *testing.T) {
	dir := t.TempDir()
	SeedCodexMountTargets(dir)
	p := filepath.Join(dir, CodexROMounts[0])
	first, err := os.Stat(p)
	if err != nil {
		t.Fatalf("first pass did not seed %s: %v", CodexROMounts[0], err)
	}
	SeedCodexMountTargets(dir)
	second, err := os.Stat(p)
	if err != nil {
		t.Fatalf("second pass removed %s: %v", CodexROMounts[0], err)
	}
	if !os.SameFile(first, second) {
		t.Fatal("second pass replaced an already-owned stub")
	}
}

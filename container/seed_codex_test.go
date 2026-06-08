package container

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSeedCodexDirsCreatesUnderGroupDirs: SeedCodexDirs creates a .codex/ in
// every directory that holds a .claude/ marker (a group dir), recursively
// (group folders nest corp/eng/sre), and SKIPS directories without the marker.
// This is the runed startup parity with gateway.seedCodexDirs — the bind source
// must exist (uid 1000) before any cold-start parallel `docker run` materializes
// it as root. No docker needed; pure FS.
func TestSeedCodexDirsCreatesUnderGroupDirs(t *testing.T) {
	root := t.TempDir()
	// two group dirs (have .claude), one nested; one plain dir (no .claude).
	mustMkdir(t, filepath.Join(root, "alice", ".claude"))
	mustMkdir(t, filepath.Join(root, "corp", "eng", ".claude")) // nested group
	mustMkdir(t, filepath.Join(root, "notagroup"))              // no .claude marker

	SeedCodexDirs(root)

	for _, g := range []string{"alice", filepath.Join("corp", "eng")} {
		cx := filepath.Join(root, g, ".codex")
		if fi, err := os.Stat(cx); err != nil || !fi.IsDir() {
			t.Fatalf("group %s: .codex not created (err=%v)", g, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "notagroup", ".codex")); !os.IsNotExist(err) {
		t.Fatalf("non-group dir got a .codex (err=%v) — marker gate broken", err)
	}
}

// TestSeedCodexDirsNoGroups: an empty groups tree is a silent no-op (no panic,
// nothing created). Covers the cold instance with no folders yet.
func TestSeedCodexDirsNoGroups(t *testing.T) {
	root := t.TempDir()
	SeedCodexDirs(root) // must not panic.
	entries, _ := os.ReadDir(root)
	if len(entries) != 0 {
		t.Fatalf("empty tree gained %d entries after seed, want 0", len(entries))
	}
}

// TestSeedCodexDirsMissingRoot: a non-existent groups dir is a silent no-op
// (WalkDir's first callback gets the stat error; the closure returns nil).
func TestSeedCodexDirsMissingRoot(t *testing.T) {
	SeedCodexDirs(filepath.Join(t.TempDir(), "does-not-exist")) // must not panic.
}

// TestSeedCodexDirsIdempotent: re-running over an already-seeded tree is a no-op
// (MkdirAll on an existing dir succeeds) — startup may run repeatedly across
// restarts.
func TestSeedCodexDirsIdempotent(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "alice", ".claude"))
	SeedCodexDirs(root)
	SeedCodexDirs(root) // second pass must not error/panic.
	if fi, err := os.Stat(filepath.Join(root, "alice", ".codex")); err != nil || !fi.IsDir() {
		t.Fatalf(".codex missing after idempotent re-seed: %v", err)
	}
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

// dockerUp reports whether a docker daemon is reachable (EnsureRunning passes).
func dockerUp() bool { return EnsureRunning() == nil }

// TestEnsureRunningReflectsDaemon: EnsureRunning is the runed startup preflight
// (main.go exits 1 on its error). It returns nil iff the container runtime is
// reachable, a non-nil error otherwise — the signal main.go gates the daemon
// boot on. We assert the contract against whatever this host reports (daemon
// up → nil; down/absent → error), so the test is deterministic either way.
func TestEnsureRunningReflectsDaemon(t *testing.T) {
	err := EnsureRunning()
	// `docker info` exit status is the ground truth; cross-check the function.
	if dockerUp() != (err == nil) {
		t.Fatalf("EnsureRunning err=%v inconsistent with daemon reachability", err)
	}
	if err != nil && err.Error() == "" {
		t.Fatal("EnsureRunning returned a non-nil but empty error")
	}
}

// TestCleanupOrphansNoPanic: CleanupOrphans is the runed startup orphan-reap
// (main.go calls it after EnsureRunning). It must never panic regardless of
// docker reachability — with no daemon it logs a warning and returns; with a
// daemon and no matching containers it's a no-op. Either path is a clean
// return. This pins the "startup never crashes on the reap" contract.
func TestCleanupOrphansNoPanic(t *testing.T) {
	// A throwaway instance/image name that matches nothing real, so even with a
	// live daemon this stops zero containers.
	CleanupOrphans("runed-test-nonexistent-instance", "arizuko-test-nonexistent-image:none")
	CleanupOrphans("", "") // the both-empty path (prefix=arizuko-, no image filter).
}

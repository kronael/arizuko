package container

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

// The RO overmount targets exist after the seed, so the docker daemon never
// has to materialize them. The daemon creates them root-owned inside the
// group's bind-mounted .codex/, which is the EPERM runed then reports on every
// restart (BUGS L156).
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

// The RO overmount hands the container the HOST file's owner and mode, so an
// operator's own ~/.codex at 0600 is unopenable by the agent's uid and codex
// answers "not logged in" — a permission fault wearing an expired-login face.
// The named set is the certain one: a group-readable file may be reachable
// through a supplementary group, so it is not accused.
func TestUnreadableCodexCredsNamesOnlyTheCertainCase(t *testing.T) {
	me := os.Getuid()
	other := me + 1

	for _, tc := range []struct {
		name string
		mode os.FileMode
		uid  int
		want bool
	}{
		{"operator 0600, another uid", 0o600, other, true},
		{"operator 0600, the owner", 0o600, me, false},
		{"0644 world-readable", 0o644, other, false},
		{"0640 group-readable, unknowable", 0o640, other, false},
		{"0000 unopenable by its owner too", 0o000, me, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, n := range CodexROMounts {
				if err := os.WriteFile(filepath.Join(dir, n), []byte("{}"), tc.mode); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(filepath.Join(dir, n), tc.mode); err != nil {
					t.Fatal(err)
				}
			}
			got := unreadableCodexCreds(dir, tc.uid)
			if (len(got) > 0) != tc.want {
				t.Fatalf("named %v, want unreadable=%v", got, tc.want)
			}
			if tc.want && len(got) != len(CodexROMounts) {
				t.Fatalf("named %d of %d credentials", len(got), len(CodexROMounts))
			}
		})
	}
}

// An absent HOST_CODEX_DIR file is the mount's own failure to report; naming it
// here would put a permission verdict on a file that has no permissions.
func TestUnreadableCodexCredsIgnoresAbsent(t *testing.T) {
	if got := unreadableCodexCreds(t.TempDir(), os.Getuid()+1); len(got) != 0 {
		t.Fatalf("named %v for a directory holding no credentials", got)
	}
}

// buildMounts mounts the credential either way — codex is optional and a spawn
// that refused over it would break every turn that never runs codex — so the
// log is the whole surfacing. An unread emission is dead wiring, hence this.
func TestBuildMountsSaysWhenCodexCredsAreUnreachable(t *testing.T) {
	cfg, folders := fhsConfig(t)
	cfg.HostCodexDir = t.TempDir()
	for _, n := range CodexROMounts {
		p := filepath.Join(cfg.HostCodexDir, n)
		if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		// 0000: no uid but root can open it, and containerRunUID() is never root.
		if err := os.Chmod(p, 0o000); err != nil {
			t.Fatal(err)
		}
	}
	in := Input{Folder: "atlas/support"}
	groupDir := filepath.Join(cfg.GroupsDir, in.Folder)
	mustMkdir(t, groupDir)

	logged := captureWarnings(t)
	mounts := buildMounts(cfg, in, groupDir, false, folders)

	out := logged()
	for _, n := range CodexROMounts {
		if !strings.Contains(out, filepath.Join(cfg.HostCodexDir, n)) {
			t.Errorf("%s unreadable and unnamed; log was: %s", n, out)
		}
	}
	if !strings.Contains(out, "not logged in") {
		t.Errorf("the log does not name the symptom the operator sees: %s", out)
	}
	if findMount(mounts, "/home/node/.codex/"+CodexROMounts[0]) == nil {
		t.Error("the credential was dropped from the mount list; the log is the surfacing, not a skip")
	}
}

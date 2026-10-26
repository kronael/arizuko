package container

import (
	"bytes"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/kronael/arizuko/core"
)

// MaterializeAppSrc is what puts bytes at that staged path. A bind mount reads
// the host filesystem, so a copy that silently no-ops leaves every agent with an
// empty /opt/arizuko and no way to migrate.

// TestSetupGroup_CreatesPerGroupWebSlots verifies SetupGroup pre-creates
// the host-side web/pub/<folder>/ and web/priv/<folder>/ dirs that
// runner.go bind-mounts into the agent home. Spec 5/V-web-vhosts.
func TestSetupGroup_CreatesPerGroupWebSlots(t *testing.T) {
	tmp := t.TempDir()
	cfg := &core.Config{
		GroupsDir: filepath.Join(tmp, "groups"),
		IpcDir:    filepath.Join(tmp, "ipc"),
		WebDir:    filepath.Join(tmp, "web"),
	}
	os.MkdirAll(cfg.GroupsDir, 0o755)
	os.MkdirAll(cfg.IpcDir, 0o755)

	if err := SetupGroup(cfg, "newworld", ""); err != nil {
		t.Fatalf("SetupGroup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.WebDir, "pub", "newworld")); err != nil {
		t.Errorf("expected web/pub/newworld: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.WebDir, "priv", "newworld")); err != nil {
		t.Errorf("expected web/priv/newworld: %v", err)
	}
}

// TestSetupGroup_NestedWebSlots verifies subgroup folders get nested
// dirs preserved in the unified web tree.
func TestSetupGroup_NestedWebSlots(t *testing.T) {
	tmp := t.TempDir()
	cfg := &core.Config{
		GroupsDir: filepath.Join(tmp, "groups"),
		IpcDir:    filepath.Join(tmp, "ipc"),
		WebDir:    filepath.Join(tmp, "web"),
	}
	os.MkdirAll(cfg.GroupsDir, 0o755)
	os.MkdirAll(cfg.IpcDir, 0o755)

	if err := SetupGroup(cfg, "atlas/support", ""); err != nil {
		t.Fatalf("SetupGroup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.WebDir, "pub", "atlas", "support")); err != nil {
		t.Errorf("expected web/pub/atlas/support: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.WebDir, "priv", "atlas", "support")); err != nil {
		t.Errorf("expected web/priv/atlas/support: %v", err)
	}
}

// --- BUGS F62: the per-group web slots are chowned where they are created ---

// canChownToContainerUID reports whether this process can hand a file it owns
// to containerUID. Only root can change an owner to a different uid; a process
// already running as containerUID may "change" it to itself.
func canChownToContainerUID() bool {
	return os.Geteuid() == 0 || os.Geteuid() == containerUID
}

// captureWarnings redirects the default slog to a buffer for the test's
// duration and returns the accumulated text.
func captureWarnings(t *testing.T) func() string {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf.String
}

// TestChownR_ReportsFailure locks the mechanism behind F62: chownR used to
// discard every os.Lchown error, so runed (uid 1000) chowning a root-owned
// path failed silently and a read-only ~/public_html persisted for months.
// The error must now come back to the caller.
func TestChownR_ReportsFailure(t *testing.T) {
	if canChownToContainerUID() {
		t.Skipf("euid %d can chown to %d; EPERM is unreachable here", os.Geteuid(), containerUID)
	}
	dir := t.TempDir()
	if err := chownR(dir, containerUID, containerUID); err == nil {
		t.Fatal("chownR discarded the Lchown failure (want the EPERM returned)")
	}
}

// TestChownR_ReportsAnUnreadableDirectory is F62's other half. The Lchown
// error came back after that fix; a WALK error did not, and it is the worse
// one — a directory runed cannot read means its whole subtree is skipped with
// no Lchown attempted, so chownR answered success over a tree it never
// touched. That is the silence F62 is named for, one level up.
func TestChownR_ReportsAnUnreadableDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every directory; the walk error is unreachable here")
	}
	root := t.TempDir()
	closed := filepath.Join(root, "closed")
	if err := os.Mkdir(closed, 0o755); err != nil {
		t.Fatal(err)
	}
	// A file INSIDE it, so a walk that could descend would find work to do.
	if err := os.WriteFile(filepath.Join(closed, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(closed, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(closed, 0o755) })

	err := chownR(root, os.Getuid(), os.Getgid())
	if err == nil {
		t.Fatal("chownR answered success over a directory it could not read; " +
			"the subtree stays unchowned and nothing says so")
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("chownR returned %v, want the permission error from the walk", err)
	}
}

// TestSetupGroup_ChownsWebSlots is F62 itself: SetupGroup pre-creates
// web/pub/<folder> and web/priv/<folder>, but the only chown in the path
// (seedGroupDir's) walks GroupsDir — a different tree — so the slots were never
// touched. Assert the chown now reaches both slot paths: by ownership where
// this process can chown, by the loud failure where it cannot.
func TestSetupGroup_ChownsWebSlots(t *testing.T) {
	tmp := t.TempDir()
	cfg := &core.Config{
		GroupsDir: filepath.Join(tmp, "groups"),
		IpcDir:    filepath.Join(tmp, "ipc"),
		WebDir:    filepath.Join(tmp, "web"),
	}
	os.MkdirAll(cfg.GroupsDir, 0o755)
	os.MkdirAll(cfg.IpcDir, 0o755)

	logged := captureWarnings(t)
	if err := SetupGroup(cfg, "slotted", ""); err != nil {
		t.Fatalf("SetupGroup: %v", err)
	}

	slots := []string{
		filepath.Join(cfg.WebDir, "pub", "slotted"),
		filepath.Join(cfg.WebDir, "priv", "slotted"),
	}
	for _, slot := range slots {
		if canChownToContainerUID() {
			st, err := os.Stat(slot)
			if err != nil {
				t.Fatalf("stat %s: %v", slot, err)
			}
			sys, ok := st.Sys().(*syscall.Stat_t)
			if !ok {
				t.Skip("no syscall.Stat_t on this platform")
			}
			if int(sys.Uid) != containerUID {
				t.Errorf("%s owned by uid %d, want %d — the slot was never chowned",
					slot, sys.Uid, containerUID)
			}
			continue
		}
		// Cannot chown here, so the proof that the chown was ATTEMPTED on the
		// slot is the warning naming it. Silence is the F62 bug.
		if !strings.Contains(logged(), slot) {
			t.Errorf("no chown warning naming %s; the slot is still skipped silently", slot)
		}
	}
}

// TestSetupGroup_WebSlotMkdirIsFatal: an uncreatable slot must fail group
// creation, not pass silently. Left silent, the docker daemon materializes the
// missing bind source as root at spawn and the agent gets an unwritable
// ~/public_html — the symptom F63 shows an operator as "broken mount".
func TestSetupGroup_WebSlotMkdirIsFatal(t *testing.T) {
	tmp := t.TempDir()
	cfg := &core.Config{
		GroupsDir: filepath.Join(tmp, "groups"),
		IpcDir:    filepath.Join(tmp, "ipc"),
		WebDir:    filepath.Join(tmp, "web"),
	}
	os.MkdirAll(cfg.GroupsDir, 0o755)
	os.MkdirAll(cfg.IpcDir, 0o755)
	// A regular file where the "pub" directory belongs: MkdirAll under it
	// fails with ENOTDIR.
	os.MkdirAll(cfg.WebDir, 0o755)
	if err := os.WriteFile(filepath.Join(cfg.WebDir, "pub"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := SetupGroup(cfg, "blocked", ""); err == nil {
		t.Fatal("SetupGroup ignored an uncreatable web slot (want an error)")
	}
}

package container

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const Bin = "docker"

// SeedCodexDirs pre-creates each group's `.codex/` (uid 1000) before any
// spawn, so cold-start parallel `docker run` can't race the runner's lazy
// MkdirAll and let Docker materialize the bind source as root (gated did
// this in gateway.seedCodexDirs). A group dir is any directory under
// groupsDir holding a `.claude/` subdir (the seedGroupDir marker); runed
// has no groups table to enumerate, so it walks the tree. Group folders
// nest (corp/eng/sre), so the walk is recursive. No-op when codex is off.
func SeedCodexDirs(groupsDir string) {
	filepath.WalkDir(groupsDir, func(p string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if fi, e := os.Stat(filepath.Join(p, ".claude")); e != nil || !fi.IsDir() {
			return nil
		}
		codexDir := filepath.Join(p, ".codex")
		if e := os.MkdirAll(codexDir, 0o755); e != nil {
			slog.Warn("seed codex dir", "group", p, "err", e)
			return nil
		}
		chownR(codexDir, containerUID, containerUID)
		return nil
	})
}

func StopContainerArgs(name string) []string {
	return []string{"stop", name}
}

func EnsureRunning() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, Bin, "info").CombinedOutput()
	if err != nil {
		return fmt.Errorf("container runtime unreachable: %w\n%s", err, out)
	}
	return nil
}

func CleanupOrphans(instance, image string) {
	prefix := "arizuko-"
	if instance != "" {
		prefix = "arizuko-" + instance + "-"
	}
	// Single docker ps call with both filters: name-prefix AND image.
	// Previously each filter ran in its own call and results were unioned
	// (OR), so on a host running multiple instances the image filter
	// would match peers and kill them.
	args := []string{"ps", "--filter=name=" + prefix, "--format", "{{.Names}}"}
	if image != "" {
		args = append(args, "--filter=ancestor="+image)
	}
	out, err := exec.Command(Bin, args...).Output()
	if err != nil {
		slog.Warn("failed to list containers for cleanup", "err", err)
		return
	}
	var orphans []string
	for _, name := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if name != "" {
			orphans = append(orphans, name)
		}
	}

	for _, name := range orphans {
		_ = exec.Command(Bin, StopContainerArgs(name)...).Run()
	}
	if len(orphans) > 0 {
		slog.Info("stopped orphaned containers", "count", len(orphans), "names", orphans)
	}
}

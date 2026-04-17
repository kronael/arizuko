package container

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"
)

const Bin = "docker"

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

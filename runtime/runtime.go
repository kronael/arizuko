package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"
)

const Bin = "docker"

func ReadonlyMountArgs(hostPath, containerPath string) []string {
	return []string{"-v", hostPath + ":" + containerPath + ":ro"}
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
	slog.Debug("container runtime reachable")
	return nil
}

func CleanupOrphans(image string) {
	filters := []string{"name=arizuko-"}
	if image != "" {
		filters = append(filters, "ancestor="+image)
	}

	seen := map[string]bool{}
	var orphans []string
	for _, f := range filters {
		out, err := exec.Command(Bin, "ps", "--filter="+f, "--format", "{{.Names}}").Output()
		if err != nil {
			slog.Warn("failed to list containers for cleanup", "err", err)
			continue
		}
		for _, name := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
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

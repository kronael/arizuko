package container

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/onvos/arizuko/core"
	"github.com/onvos/arizuko/runtime"
)

// StartSidecars launches sidecar containers and returns
// their names for cleanup.
func StartSidecars(
	cfg *RunnerConfig, folder string,
	sidecars map[string]core.Sidecar,
	ipcDir string,
) []string {
	if len(sidecars) == 0 {
		return nil
	}

	sockDir := filepath.Join(ipcDir, "sidecars")
	os.MkdirAll(sockDir, 0o755)

	var names []string
	for name, spec := range sidecars {
		cname := sidecarName(folder, name)
		sockPath := filepath.Join(sockDir, name+".sock")

		// Remove stale socket
		os.Remove(sockPath)

		mem := spec.MemMB
		if mem == 0 {
			mem = 256
		}
		cpus := spec.CPUs
		if cpus == 0 {
			cpus = 0.5
		}
		net := spec.Net
		if net == "" {
			net = "none"
		}

		args := []string{
			"run", "-d", "--rm",
			"--name", cname,
			fmt.Sprintf("--memory=%dm", mem),
			fmt.Sprintf("--cpus=%.1f", cpus),
			fmt.Sprintf("--network=%s", net),
			"-v", hp(cfg, sockDir) + ":/sockets",
			"-e", "SOCKET_PATH=/sockets/" + name + ".sock",
			"-e", "TZ=" + cfg.Timezone,
		}

		for k, v := range spec.Env {
			args = append(args, "-e", k+"="+v)
		}

		args = append(args, spec.Image)

		cmd := exec.Command(runtime.Bin, args...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			slog.Error("sidecar start failed",
				"name", name, "image", spec.Image,
				"err", err, "output", string(out))
			continue
		}

		slog.Info("sidecar started",
			"name", name, "container", cname,
			"image", spec.Image)
		names = append(names, cname)
	}

	return names
}

// StopSidecars stops the given sidecar containers.
func StopSidecars(names []string) {
	for _, name := range names {
		cmd := exec.Command(
			runtime.Bin, runtime.StopContainerArgs(name)...)
		if err := cmd.Run(); err != nil {
			exec.Command(runtime.Bin, "rm", "-f", name).Run()
		}
		slog.Debug("sidecar stopped", "container", name)
	}
}

func sidecarName(folder, name string) string {
	safe := safeNameRe.ReplaceAllString(folder, "-")
	return fmt.Sprintf("arizuko-sc-%s-%s", safe, name)
}

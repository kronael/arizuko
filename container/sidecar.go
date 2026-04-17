package container

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/onvos/arizuko/core"
	"github.com/onvos/arizuko/groupfolder"
)

func startSidecars(
	cfg *core.Config, folder string,
	sidecars map[string]core.Sidecar,
	ipcDir string,
) []string {
	if len(sidecars) == 0 {
		return nil
	}

	sockDir := groupfolder.IpcSidecars(ipcDir)
	os.MkdirAll(sockDir, 0o755)

	var names []string
	for name, spec := range sidecars {
		cname := sidecarName(folder, name)
		// Per-sidecar subdir isolates each sidecar's socket from peers.
		// Previously every sidecar mounted the shared sockDir and could
		// read/write siblings' sockets to impersonate them to the agent.
		perDir := filepath.Join(sockDir, name)
		if err := os.MkdirAll(perDir, 0o700); err != nil {
			slog.Error("sidecar mkdir failed",
				"name", name, "dir", perDir, "err", err)
			continue
		}
		// Reset mode in case dir pre-existed with looser permissions.
		os.Chmod(perDir, 0o700)
		// Refuse to start if a container with this name already exists
		// (concurrent Run on same folder): pre-removing the socket of a
		// running peer would silently break MCP connectivity.
		if out, err := exec.Command(Bin, "ps", "-a", "--filter", "name=^"+cname+"$", "--format", "{{.Names}}").Output(); err == nil && strings.TrimSpace(string(out)) == cname {
			slog.Warn("sidecar container already exists; skipping start",
				"name", name, "container", cname)
			continue
		}
		sockPath := filepath.Join(perDir, name+".sock")
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
			"-v", hp(cfg, perDir) + ":/sockets",
			"-e", "SOCKET_PATH=/sockets/" + name + ".sock",
			"-e", "TZ=" + cfg.Timezone,
		}

		for k, v := range spec.Env {
			args = append(args, "-e", k+"="+v)
		}

		args = append(args, spec.Image)

		cmd := exec.Command(Bin, args...)
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

func stopSidecars(names []string) {
	for _, name := range names {
		cmd := exec.Command(
			Bin, StopContainerArgs(name)...)
		if err := cmd.Run(); err != nil {
			exec.Command(Bin, "rm", "-f", name).Run()
		}
		slog.Debug("sidecar stopped", "container", name)
	}
}

func sidecarName(folder, name string) string {
	safe := safeNameRe.ReplaceAllString(folder, "-")
	return fmt.Sprintf("arizuko-sc-%s-%s", safe, name)
}

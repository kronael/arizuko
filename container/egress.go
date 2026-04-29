package container

import (
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"github.com/onvos/arizuko/crackbox/pkg/client"
)

// EgressConfig holds the optional crackbox isolation settings. Zero value
// disables isolation; spawn falls back to the default Docker bridge.
type EgressConfig struct {
	// Network is the Docker network name to attach the agent to. Empty
	// disables egress isolation entirely.
	Network string
	// AdminURL is the HTTP base URL of the crackbox proxy admin API
	// (e.g. http://crackbox:3129).
	AdminURL string
	// AllowlistFn returns the resolved allowlist for a folder. May return
	// nil for "default-deny" (no rules) — crackbox treats that as a block.
	AllowlistFn func(folder string) ([]string, error)
}

func (e EgressConfig) Enabled() bool {
	return e.Network != "" && e.AdminURL != ""
}

// inspectContainerIP returns the IPv4 address assigned to <containerName>
// on <network>. Polled briefly because docker run + network attach is
// async; the IP is set within ~50ms in practice.
func inspectContainerIP(containerName, network string) (string, error) {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		out, err := exec.Command(Bin, "inspect", "-f",
			fmt.Sprintf(`{{(index .NetworkSettings.Networks %q).IPAddress}}`, network),
			containerName,
		).Output()
		if err == nil {
			ip := strings.TrimSpace(string(out))
			if ip != "" && ip != "<no value>" {
				return ip, nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return "", fmt.Errorf("could not inspect IP for %s on %s", containerName, network)
}

// registerEgress informs crackbox about a newly-spawned container. Returns
// the container's IP for unregister; returns empty + nil error if egress is
// disabled (caller treats as no-op).
func registerEgress(cfg EgressConfig, folder, containerName string) (ip string, _ error) {
	if !cfg.Enabled() {
		return "", nil
	}
	ip, err := inspectContainerIP(containerName, cfg.Network)
	if err != nil {
		return "", fmt.Errorf("inspect ip: %w", err)
	}
	allowlist, err := cfg.AllowlistFn(folder)
	if err != nil {
		return "", fmt.Errorf("resolve allowlist: %w", err)
	}
	if err := client.New(cfg.AdminURL).Register(ip, folder, allowlist); err != nil {
		return "", fmt.Errorf("crackbox register: %w", err)
	}
	slog.Info("egress registered",
		"folder", folder, "ip", ip, "rules", len(allowlist))
	return ip, nil
}

func unregisterEgress(cfg EgressConfig, ip string) {
	if !cfg.Enabled() || ip == "" {
		return
	}
	if err := client.New(cfg.AdminURL).Unregister(ip); err != nil {
		slog.Warn("egress unregister", "ip", ip, "err", err)
	}
}

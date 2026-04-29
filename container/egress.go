package container

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// EgressConfig holds the optional crackbox/egred isolation settings. Zero
// value disables isolation; spawn falls back to the default Docker bridge.
type EgressConfig struct {
	// Network is the Docker network name to attach the agent to. Empty
	// disables egress isolation entirely.
	Network string
	// EgredAPI is the HTTP base URL of egred (e.g. http://egred:3129).
	EgredAPI string
	// AllowlistFn returns the resolved allowlist for a folder. May return
	// nil for "default-deny" (no rules) — egred treats that as a block.
	AllowlistFn func(folder string) ([]string, error)
}

func (e EgressConfig) Enabled() bool {
	return e.Network != "" && e.EgredAPI != ""
}

// inspectContainerIP returns the IPv4 address assigned to <containerName>
// on <network>. Polled briefly because docker run + network attach is
// async; the IP is set within ~50ms in practice.
func inspectContainerIP(containerName, network string) (string, error) {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		out, err := exec.Command(Bin, "inspect", "-f",
			fmt.Sprintf("{{.NetworkSettings.Networks.%s.IPAddress}}", network),
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

// registerEgress informs egred about a newly-spawned container. Returns the
// container's IP for unregister; returns empty + nil error if egress is
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
	body, _ := json.Marshal(map[string]interface{}{
		"ip":        ip,
		"folder":    folder,
		"allowlist": allowlist,
	})
	resp, err := http.Post(cfg.EgredAPI+"/v1/register",
		"application/json", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("egred register: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("egred register: status %d", resp.StatusCode)
	}
	slog.Info("egress registered",
		"folder", folder, "ip", ip, "rules", len(allowlist))
	return ip, nil
}

func unregisterEgress(cfg EgressConfig, ip string) {
	if !cfg.Enabled() || ip == "" {
		return
	}
	body, _ := json.Marshal(map[string]string{"ip": ip})
	resp, err := http.Post(cfg.EgredAPI+"/v1/unregister",
		"application/json", bytes.NewReader(body))
	if err != nil {
		slog.Warn("egress unregister", "ip", ip, "err", err)
		return
	}
	resp.Body.Close()
}

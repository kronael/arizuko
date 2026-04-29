package container

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"log/slog"
	"net"

	"github.com/onvos/arizuko/crackbox/pkg/client"
)

// EgressConfig holds the optional crackbox isolation settings. Zero value
// disables isolation; spawn falls back to the default Docker bridge.
type EgressConfig struct {
	// Network is the Docker network name to attach the agent to. Empty
	// disables egress isolation entirely.
	Network string
	// Subnet is the CIDR of the agents network — used to pick a fresh IP
	// per spawn so we can register with crackbox *before* the container
	// starts (avoids the agent firing traffic before its IP is known).
	Subnet string
	// ProxyURL is what the agent's HTTP_PROXY/HTTPS_PROXY env points at
	// (e.g. http://crackbox:3128).
	ProxyURL string
	// AdminURL is the HTTP base URL of the crackbox admin API
	// (e.g. http://crackbox:3129).
	AdminURL string
	// AllowlistFn returns the resolved allowlist for an opaque id (in
	// arizuko's case the folder, but crackbox treats it as a label).
	AllowlistFn func(id string) ([]string, error)
}

func (e EgressConfig) Enabled() bool {
	return e.Network != "" && e.AdminURL != ""
}

// PickIP returns a random host address inside the configured subnet.
// /16 default has 65k addresses, so collisions with running containers
// are vanishingly rare; docker run --ip would error if it ever happens.
func (e EgressConfig) PickIP() (string, error) {
	_, n, err := net.ParseCIDR(e.Subnet)
	if err != nil {
		return "", fmt.Errorf("egress subnet %q: %w", e.Subnet, err)
	}
	ip4 := n.IP.To4()
	if ip4 == nil {
		return "", fmt.Errorf("egress subnet %q: ipv4 only in v1", e.Subnet)
	}
	ones, _ := n.Mask.Size()
	if ones >= 31 {
		return "", fmt.Errorf("egress subnet %q: too small", e.Subnet)
	}
	hostBits := 32 - ones
	// Reserve .1 (gateway), .2/.3 (typically the proxy + first auto-assign).
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	r := binary.BigEndian.Uint32(b[:])
	mask := uint32(1)<<uint(hostBits) - 1
	host := r & mask
	if host < 4 {
		host += 100 // avoid reserved low addresses
	}
	if host == mask { // broadcast
		host--
	}
	netInt := binary.BigEndian.Uint32(ip4) &^ mask
	var out [4]byte
	binary.BigEndian.PutUint32(out[:], netInt|host)
	return net.IP(out[:]).String(), nil
}

// registerEgress informs crackbox about a to-be-spawned container, returning
// the pre-assigned IP. Caller must pass --ip <ip> to docker run so the
// container actually lands on this address. Disabled config is a no-op.
func registerEgress(cfg EgressConfig, id string) (ip string, _ error) {
	if !cfg.Enabled() {
		return "", nil
	}
	ip, err := cfg.PickIP()
	if err != nil {
		return "", err
	}
	allowlist, err := cfg.AllowlistFn(id)
	if err != nil {
		return "", fmt.Errorf("resolve allowlist: %w", err)
	}
	if err := client.New(cfg.AdminURL).Register(ip, id, allowlist); err != nil {
		return "", fmt.Errorf("crackbox register: %w", err)
	}
	slog.Info("egress registered",
		"id", id, "ip", ip, "rules", len(allowlist))
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

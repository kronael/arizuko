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
//
// Per-folder isolation: gated creates a fresh internal Docker network
// per folder under NetworkPrefix and attaches Crackbox to each. The
// agent container lands on its folder's network and reaches crackbox
// via Docker DNS (which returns the network-scoped A record).
type EgressConfig struct {
	// NetworkPrefix is prepended to the sanitized folder name to form
	// the per-folder Docker network name (e.g. "arizuko_krons"). The
	// prefix should be unique per arizuko instance to avoid collisions
	// between instances on the same docker host.
	NetworkPrefix string
	// CrackboxContainer is the docker name of the crackbox container
	// to attach to each folder network (e.g. "arizuko_crackbox_krons").
	CrackboxContainer string
	// ParentSubnet is the parent CIDR (e.g. 10.99.0.0/16) carved into
	// /24s — one per folder. Must be /8 to /23.
	ParentSubnet string
	// ProxyURL is what the agent's HTTP_PROXY/HTTPS_PROXY env points at
	// (e.g. http://crackbox:3128).
	ProxyURL string
	// AdminURL is the HTTP base URL of the crackbox admin API
	// (e.g. http://crackbox:3129).
	AdminURL string
	// AdminSecret is the optional bearer token sent on register/unregister.
	// Empty disables auth (must match crackbox-side config).
	AdminSecret string
	// AllowlistFn returns the resolved allowlist for an opaque id (in
	// arizuko's case the folder, but crackbox treats it as a label).
	AllowlistFn func(id string) ([]string, error)
}

// active returns whether the config is sufficient to enable egress for a
// spawn. AdminURL presence is the master switch — set the URL or don't.
func (e EgressConfig) active() bool {
	return e.AdminURL != "" && e.NetworkPrefix != "" && e.CrackboxContainer != ""
}

// PickIP returns a random host address inside the supplied /24 subnet.
// Reserves .0 (network), .1 (gateway), .2 (first auto-assign — often
// crackbox), and .255 (broadcast).
func PickIP(subnet string) (string, error) {
	_, n, err := net.ParseCIDR(subnet)
	if err != nil {
		return "", fmt.Errorf("egress subnet %q: %w", subnet, err)
	}
	ip4 := n.IP.To4()
	if ip4 == nil {
		return "", fmt.Errorf("egress subnet %q: ipv4 only", subnet)
	}
	ones, _ := n.Mask.Size()
	if ones >= 31 {
		return "", fmt.Errorf("egress subnet %q: too small", subnet)
	}
	hostBits := 32 - ones
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

// registerEgress ensures the per-folder network exists, attaches crackbox
// to it, picks an IP inside the folder's /24, and registers with crackbox.
// Returns (network, ip). Disabled config is a no-op (returns "", "", nil).
func registerEgress(cfg EgressConfig, id string) (network, ip string, _ error) {
	if !cfg.active() {
		return "", "", nil
	}
	netName, subnet, err := ensureFolderNetwork(
		cfg.NetworkPrefix, cfg.CrackboxContainer, cfg.ParentSubnet, id)
	if err != nil {
		return "", "", err
	}
	ip, err = PickIP(subnet)
	if err != nil {
		return "", "", err
	}
	allowlist, err := cfg.AllowlistFn(id)
	if err != nil {
		return "", "", fmt.Errorf("resolve allowlist: %w", err)
	}
	if err := newClient(cfg).Register(ip, id, allowlist); err != nil {
		return "", "", fmt.Errorf("crackbox register: %w", err)
	}
	slog.Info("egress registered",
		"id", id, "network", netName, "ip", ip, "rules", len(allowlist))
	return netName, ip, nil
}

func unregisterEgress(cfg EgressConfig, ip string) {
	if !cfg.active() || ip == "" {
		return
	}
	if err := newClient(cfg).Unregister(ip); err != nil {
		slog.Warn("egress unregister", "ip", ip, "err", err)
	}
}

func newClient(cfg EgressConfig) *client.Client {
	return client.New(cfg.AdminURL, cfg.AdminSecret)
}

package container

import (
	"fmt"
	"hash/fnv"
	"log/slog"
	"net"
	"strings"
	"sync"
)

// Per-folder network isolation. Each folder gets its own internal Docker
// network so a compromised agent can only ARP its own crackbox interface,
// not peer agents in other folders. Crackbox attaches to every folder
// network as the sole egress path. Networks persist for the instance
// lifetime (cheap, ~1MB kernel state per net) — operators can
// `docker network rm <name>` if cleanup is needed.

// netMgr serializes folder-network creation + crackbox attachment so two
// concurrent spawns on the same folder don't both call docker network
// create. The outer mutex guards the map; per-folder mutexes guard the
// docker calls themselves. Per-folder granularity lets parallel spawns
// on different folders proceed without contention.
type netMgr struct {
	outer    sync.Mutex
	perFolder map[string]*sync.Mutex

	// Allocated /24s (key: subnet string "10.99.X.0/24"). Populated lazily
	// from `docker network inspect` of any folder network we create or
	// reuse, so re-runs after gated restart don't double-allocate. Guarded
	// by outer.
	allocated map[string]bool
}

var defaultNetMgr = &netMgr{
	perFolder: map[string]*sync.Mutex{},
	allocated: map[string]bool{},
}

func (m *netMgr) folderLock(folder string) *sync.Mutex {
	m.outer.Lock()
	defer m.outer.Unlock()
	if mu, ok := m.perFolder[folder]; ok {
		return mu
	}
	mu := &sync.Mutex{}
	m.perFolder[folder] = mu
	return mu
}

// markAllocated records that subnet is in use. Idempotent.
func (m *netMgr) markAllocated(subnet string) {
	m.outer.Lock()
	defer m.outer.Unlock()
	m.allocated[subnet] = true
}

// isAllocated returns whether subnet is already in use locally.
func (m *netMgr) isAllocated(subnet string) bool {
	m.outer.Lock()
	defer m.outer.Unlock()
	return m.allocated[subnet]
}

// FolderNetwork returns the docker network name for a folder under prefix.
// e.g. prefix="arizuko_krons" folder="atlas/support" -> "arizuko_krons_atlas-support"
func FolderNetwork(prefix, folder string) string {
	return prefix + "_" + SanitizeFolder(folder)
}

// pickFolderSubnet derives a /24 inside parent for the given folder.
// Uses FNV-1a hash for a deterministic starting point, then linear-probes
// allocated /24s until a free one is found. Returns the subnet CIDR.
//
// Parent must be IPv4 with prefix /8 to /23 (gives 1-65536 usable /24s).
// Caller is responsible for marking the result as allocated and for
// avoiding races (we hold the per-folder lock during this call).
func pickFolderSubnet(mgr *netMgr, parent, folder string) (string, error) {
	_, n, err := net.ParseCIDR(parent)
	if err != nil {
		return "", fmt.Errorf("parent subnet %q: %w", parent, err)
	}
	ip4 := n.IP.To4()
	if ip4 == nil {
		return "", fmt.Errorf("parent subnet %q: ipv4 only", parent)
	}
	ones, _ := n.Mask.Size()
	if ones < 8 || ones > 24 {
		return "", fmt.Errorf("parent subnet %q: prefix /%d outside /8../24", parent, ones)
	}
	slots := 1 << uint(24-ones) // /16 -> 256 /24s, /20 -> 16, /24 -> 1
	if slots <= 0 {
		return "", fmt.Errorf("parent subnet %q: no /24 slots", parent)
	}
	h := fnv.New32a()
	h.Write([]byte(folder))
	start := int(h.Sum32() % uint32(slots))
	// base32: 32-bit network address with the slot bits zeroed out.
	base32 := uint32(ip4[0])<<24 | uint32(ip4[1])<<16 | uint32(ip4[2])<<8 | uint32(ip4[3])
	slotMask := uint32(slots-1) << 8 // slot bits sit just above the host /24 byte
	base32 &^= slotMask
	for i := 0; i < slots; i++ {
		idx := (start + i) % slots
		net32 := base32 | (uint32(idx) << 8)
		cidr := fmt.Sprintf("%d.%d.%d.0/24",
			byte(net32>>24), byte(net32>>16), byte(net32>>8))
		if !mgr.isAllocated(cidr) {
			return cidr, nil
		}
	}
	return "", fmt.Errorf("parent subnet %q: all %d /24 slots exhausted", parent, slots)
}

// ensureFolderNetwork creates the per-folder network if missing, attaches
// crackbox to it, and returns (network name, /24 cidr). Idempotent and
// safe under concurrent spawns: serialized per folder.
//
// Failure modes:
//   - parent subnet exhausted -> returns error, caller aborts spawn
//   - crackbox container missing/stopped -> attach fails, returns error
//   - network already exists with different subnet -> reuses existing
func ensureFolderNetwork(prefix, crackbox, parent, folder string) (string, string, error) {
	if prefix == "" {
		return "", "", fmt.Errorf("network prefix empty")
	}
	if crackbox == "" {
		return "", "", fmt.Errorf("crackbox container name empty")
	}
	mu := defaultNetMgr.folderLock(folder)
	mu.Lock()
	defer mu.Unlock()

	netName := FolderNetwork(prefix, folder)

	// Check if network already exists.
	if existingSubnet, ok := inspectNetworkSubnet(netName); ok {
		defaultNetMgr.markAllocated(existingSubnet)
		if err := connectCrackbox(crackbox, netName); err != nil {
			return "", "", err
		}
		return netName, existingSubnet, nil
	}

	// pickFolderSubnet probes our internal allocator state, but Docker
	// may already have networks (e.g. orphans from a previous instance
	// name) sitting on the same /24. Retry with the next slot when
	// `network create` returns "Pool overlaps". Bounded by slot count.
	var subnet string
	for attempt := 0; attempt < 8; attempt++ {
		s, err := pickFolderSubnet(defaultNetMgr, parent, folder)
		if err != nil {
			return "", "", err
		}
		err = createNetwork(netName, s)
		if err == nil {
			subnet = s
			break
		}
		if !strings.Contains(err.Error(), "Pool overlaps") {
			return "", "", err
		}
		// Mark this slot taken so pickFolderSubnet probes the next one.
		defaultNetMgr.markAllocated(s)
		slog.Warn("egress subnet overlap, retrying", "subnet", s, "folder", folder)
	}
	if subnet == "" {
		return "", "", fmt.Errorf("could not find a free subnet for folder %q in parent %s", folder, parent)
	}
	defaultNetMgr.markAllocated(subnet)
	if err := connectCrackbox(crackbox, netName); err != nil {
		return "", "", err
	}
	return netName, subnet, nil
}

// inspectNetworkSubnet returns the first IPAM /24 subnet of the named
// network, and ok=false if the network does not exist.
func inspectNetworkSubnet(name string) (string, bool) {
	cmd := execCommand(Bin, "network", "inspect", "-f",
		"{{range .IPAM.Config}}{{.Subnet}}{{\"\\n\"}}{{end}}", name)
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		s := strings.TrimSpace(line)
		if s != "" {
			return s, true
		}
	}
	return "", false
}

// createNetwork wraps `docker network create --internal --subnet ...`.
// Treats "already exists" as success — the inspect path above usually
// catches it first, but compose creating its own network of the same
// name is still possible.
func createNetwork(name, subnet string) error {
	out, err := execCommand(Bin, "network", "create",
		"--internal", "--subnet", subnet, name).CombinedOutput()
	if err == nil {
		slog.Info("egress network created", "name", name, "subnet", subnet)
		return nil
	}
	s := string(out)
	if strings.Contains(s, "already exists") {
		return nil
	}
	return fmt.Errorf("network create %s: %w (%s)", name, err, strings.TrimSpace(s))
}

// connectCrackbox attaches the crackbox container to a folder network.
// Idempotent: re-attaching an already-connected container returns an
// error string we tolerate.
func connectCrackbox(crackbox, network string) error {
	// `--alias crackbox` makes the short hostname resolve via Docker's
	// embedded DNS on this network. Without it the agent container's
	// HTTPS_PROXY=http://crackbox:3128 silently fails DNS resolution
	// (the compose service-name alias only applies to the project's
	// default network, not the per-folder networks gated creates at
	// runtime).
	out, err := execCommand(Bin, "network", "connect",
		"--alias", "crackbox", network, crackbox).CombinedOutput()
	if err == nil {
		slog.Info("egress crackbox attached", "container", crackbox, "network", network)
		return nil
	}
	s := string(out)
	if strings.Contains(s, "already exists in network") ||
		strings.Contains(s, "is already connected to network") ||
		strings.Contains(s, "endpoint with name "+crackbox+" already exists") {
		return nil
	}
	return fmt.Errorf("network connect %s -> %s: %w (%s)",
		crackbox, network, err, strings.TrimSpace(s))
}

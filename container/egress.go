package container

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/kronael/arizuko/egress"
)

// egressLabel marks proxy containers so a runed restart can reap the ones its
// predecessor left behind. Value is the instance's network prefix.
const egressLabel = "arizuko.egress"

// Vars, not consts, so tests can point the probe at a local listener and cut
// the deadline. Production never reassigns them.
var (
	// egressPort is the proxy's listen port inside its own container. One
	// proxy per spawn in its own network namespace, so this never collides.
	egressPort = 3128
	// egressReadyTimeout bounds the wait for the proxy's /health. The spawn
	// fails if it elapses — never spawn the agent with no proxy answering.
	egressReadyTimeout = 15 * time.Second
	egressReadyPoll    = 20 * time.Millisecond
)

// EgressConfig holds this instance's egress isolation settings. Zero value
// disables it, and a deployment with no egress configured spawns exactly as
// it did before — no network, no proxy, no filtering.
//
// One proxy per spawn (spec 6/24): the allowlist is baked into the proxy
// process at start and dies with the turn. There is no registry, no admin
// API and no shared proxy, so there is no entry that can go stale and no
// source IP that can be reused by the next tenant.
type EgressConfig struct {
	// NetworkPrefix is prepended to the sanitized folder name to form
	// the per-folder Docker network name (e.g. "arizuko_krons"). The
	// prefix must be unique per arizuko instance to avoid collisions
	// between instances on the same docker host.
	NetworkPrefix string
	// ParentSubnet is the parent CIDR (e.g. 10.99.0.0/16) carved into
	// /24s — one per folder. Must be /8 to /23.
	ParentSubnet string
	// Image holds the `egress` binary (see egress/Dockerfile). Setting it
	// is what turns egress isolation on.
	Image string
	// UplinkNetwork is the docker network that gives the proxy its route to
	// the internet, and gives runed a path to probe the proxy's listener.
	// The spawn is never attached to it — the spawn's own network is
	// --internal, so the proxy is its only way out.
	UplinkNetwork string
	// AllowlistFn returns the resolved allowlist for an opaque id (in
	// arizuko's case the folder).
	AllowlistFn func(id string) ([]string, error)
}

func (e EgressConfig) active() bool {
	return e.Image != "" && e.NetworkPrefix != "" &&
		e.UplinkNetwork != "" && e.AllowlistFn != nil
}

// egressSpawn is one spawn's private egress path.
type egressSpawn struct {
	Network  string // docker network to attach the spawn to
	IP       string // the address the spawn must hold — the proxy accepts no other
	ProxyURL string // HTTP_PROXY/HTTPS_PROXY value for the spawn
	stop     func() // tears the proxy down; never nil once started
}

// Stop kills this spawn's proxy. Safe on a zero value.
func (e egressSpawn) Stop() {
	if e.stop != nil {
		e.stop()
	}
}

// PickIP returns a random host address inside the supplied /24 subnet.
// Reserves .0 (network), .1 (gateway), .2 (first auto-assign) and .255
// (broadcast).
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

// startEgressProxy starts this spawn's own egress proxy and returns where to
// put the spawn and how to reach the proxy. Disabled config is a no-op
// (zero egressSpawn, nil error) — a deployment without egress spawns as before.
//
// Order matters and is the reason this returns before the caller runs the
// agent: the proxy's listener is answering before this function returns, so
// the agent's first outbound request cannot land on a closed port. Any
// failure returns an error, tears down what it created, and MUST fail the
// spawn — a spawn that proceeds without its proxy is an unfiltered container,
// which inverts the property this exists for.
func startEgressProxy(cfg EgressConfig, id, spawnName string) (egressSpawn, error) {
	if !cfg.active() {
		return egressSpawn{}, nil
	}
	netName, subnet, err := ensureFolderNetwork(cfg.NetworkPrefix, cfg.ParentSubnet, id)
	if err != nil {
		return egressSpawn{}, err
	}
	allowlist, err := cfg.AllowlistFn(id)
	if err != nil {
		return egressSpawn{}, fmt.Errorf("resolve allowlist: %w", err)
	}
	spawnIP, proxyIP, err := pickPair(subnet)
	if err != nil {
		return egressSpawn{}, err
	}

	name := spawnName + "-egress"
	// --client pins the proxy to this one spawn. The folder network holds the
	// folder's other spawns and the proxy also sits on the uplink; without the
	// pin a neighbour could send its CONNECT here and borrow an allowlist it
	// was never granted (an elevated turn's is unrestricted).
	args := []string{
		"run", "-d", "--rm",
		"--name", name,
		"--label", egressLabel + "=" + cfg.NetworkPrefix,
		"--network", netName,
		"--ip", proxyIP,
		"--cap-drop", "ALL",
		"--read-only",
		cfg.Image,
		"--listen", fmt.Sprintf(":%d", egressPort),
		"--client", spawnIP,
		"--allow", strings.Join(allowlist, ","),
	}
	if out, err := execCommand(Bin, args...).CombinedOutput(); err != nil {
		return egressSpawn{}, fmt.Errorf("egress proxy run %s: %w (%s)",
			name, err, strings.TrimSpace(string(out)))
	}
	stop := func() {
		if out, err := execCommand(Bin, "rm", "-f", name).CombinedOutput(); err != nil {
			slog.Warn("egress proxy teardown", "container", name,
				"err", err, "out", strings.TrimSpace(string(out)))
		}
	}

	// The uplink is the proxy's route to the internet AND runed's path to it:
	// without it the proxy sits on an --internal network and every allowed
	// host would still fail to dial.
	if out, err := execCommand(Bin, "network", "connect",
		cfg.UplinkNetwork, name).CombinedOutput(); err != nil {
		stop()
		return egressSpawn{}, fmt.Errorf("egress uplink connect %s -> %s: %w (%s)",
			name, cfg.UplinkNetwork, err, strings.TrimSpace(string(out)))
	}

	uplinkIP, err := containerIP(name, cfg.UplinkNetwork)
	if err != nil {
		stop()
		return egressSpawn{}, fmt.Errorf("egress proxy uplink ip: %w", err)
	}
	probe := fmt.Sprintf("http://%s%s",
		net.JoinHostPort(uplinkIP, fmt.Sprint(egressPort)), egress.HealthPath)
	if err := waitHealthy(probe, egressReadyTimeout); err != nil {
		stop()
		return egressSpawn{}, fmt.Errorf("egress proxy %s not ready: %w", name, err)
	}

	slog.Info("egress proxy up", "id", id, "container", name,
		"network", netName, "spawn_ip", spawnIP, "proxy_ip", proxyIP,
		"rules", len(allowlist))
	return egressSpawn{
		Network:  netName,
		IP:       spawnIP,
		ProxyURL: fmt.Sprintf("http://%s:%d", proxyIP, egressPort),
		stop:     stop,
	}, nil
}

// pickPair returns two distinct addresses in subnet: one for the spawn, one
// for its proxy.
func pickPair(subnet string) (spawnIP, proxyIP string, _ error) {
	spawnIP, err := PickIP(subnet)
	if err != nil {
		return "", "", err
	}
	for range 8 {
		proxyIP, err = PickIP(subnet)
		if err != nil {
			return "", "", err
		}
		if proxyIP != spawnIP {
			return spawnIP, proxyIP, nil
		}
	}
	return "", "", fmt.Errorf("egress subnet %q: no second free address", subnet)
}

// containerIP reads a container's address on one network.
func containerIP(name, network string) (string, error) {
	out, err := execCommand(Bin, "inspect", "-f",
		fmt.Sprintf(`{{(index .NetworkSettings.Networks %q).IPAddress}}`, network),
		name).Output()
	if err != nil {
		return "", fmt.Errorf("inspect %s on %s: %w", name, network, err)
	}
	ip := strings.TrimSpace(string(out))
	if ip == "" || ip == "<no value>" {
		return "", fmt.Errorf("container %s has no address on %s", name, network)
	}
	return ip, nil
}

// waitHealthy polls the proxy's /health until it answers 200. This is what
// makes "proxy ready before the container's first egress attempt" a fact
// instead of a hope: the caller starts the agent only after this returns nil.
// A TCP connect would not be enough — it succeeds the moment the port is
// bound, which can precede the handler serving.
func waitHealthy(url string, deadline time.Duration) error {
	c := &http.Client{Timeout: 2 * time.Second}
	end := time.Now().Add(deadline)
	var last error
	for time.Now().Before(end) {
		resp, err := c.Get(url)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			last = fmt.Errorf("health %s", resp.Status)
		} else {
			last = err
		}
		time.Sleep(egressReadyPoll)
	}
	if last == nil {
		last = fmt.Errorf("timed out")
	}
	return last
}

// SweepEgressProxies kills proxy containers this instance left behind. A runed
// that died mid-turn cannot run its own teardown, and an orphan proxy holds a
// live allowlist on a folder network — including an elevated turn's `*`. Runs
// once at startup; the prefix is the label value, which keeps the sweep off
// other instances' proxies on the same docker host.
func SweepEgressProxies(networkPrefix string) {
	if networkPrefix == "" {
		return
	}
	out, err := execCommand(Bin, "ps", "-aq",
		"--filter", "label="+egressLabel+"="+networkPrefix).Output()
	if err != nil {
		slog.Warn("egress sweep: list", "err", err)
		return
	}
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		id := strings.TrimSpace(line)
		if id == "" {
			continue
		}
		slog.Warn("egress sweep: removing orphaned proxy", "container", id)
		if o, err := execCommand(Bin, "rm", "-f", id).CombinedOutput(); err != nil {
			slog.Warn("egress sweep: remove", "container", id,
				"err", err, "out", strings.TrimSpace(string(o)))
		}
	}
}

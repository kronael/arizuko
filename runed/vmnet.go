package runed

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/egress"
)

// vmNetOps is the guest's network, as the reap needs to see it: the rule set and
// the tap, both removable from a record alone so the disk scan can undo them
// after a runed crash.
type vmNetOps interface {
	rulesDown(rec *vmRecord)
	tapDown(tap string)
}

// tapPrefix is the /30 every guest gets: two usable addresses, the guest's and
// the host side's, and no neighbour. A guest has nobody to talk to on its own
// link, so the shared bridge and the DHCP address discovery a pool of VMs needs
// do not exist here — both addresses are fixed before the guest boots.
const tapPrefix = 30

// tapNet carves the idx'th /30 out of the configured subnet. The guest takes the
// low address and the host side the high one (spec 6/41 §"Egress by name, sealed
// below the kernel").
func tapNet(subnet string, idx int) (guest, host string, err error) {
	_, n, err := net.ParseCIDR(subnet)
	if err != nil {
		return "", "", fmt.Errorf("tap subnet %q: %w", subnet, err)
	}
	ip4 := n.IP.To4()
	if ip4 == nil {
		return "", "", fmt.Errorf("tap subnet %q: ipv4 only", subnet)
	}
	ones, _ := n.Mask.Size()
	if ones > tapPrefix-2 {
		return "", "", fmt.Errorf("tap subnet %q: /%d holds no /%d for a guest and the host",
			subnet, ones, tapPrefix)
	}
	if idx < 0 {
		return "", "", fmt.Errorf("tap subnet %q: negative index %d", subnet, idx)
	}
	base := binary.BigEndian.Uint32(ip4)
	offset := uint32(idx) * 4
	if offset+3 >= uint32(1)<<uint(32-ones) {
		return "", "", fmt.Errorf("tap subnet %q: index %d is past the subnet", subnet, idx)
	}
	var g, h [4]byte
	binary.BigEndian.PutUint32(g[:], base+offset+1)
	binary.BigEndian.PutUint32(h[:], base+offset+2)
	return net.IP(g[:]).String(), net.IP(h[:]).String(), nil
}

// tapName is the guest's tap interface. An ifname is 15 characters, so the
// instance is hashed rather than spelled: "arz-" + 4 hex of the instance name +
// "-t" + the index, 12 characters at most.
func tapName(instance string, idx int) string {
	h := sha256.Sum256([]byte(instance))
	return fmt.Sprintf("arz-%s-t%02x", hex.EncodeToString(h[:2]), idx&0xff)
}

// tapRules renders the guest's whole rule set, in the order the chain must read
// it: the one accept above the drops.
//
// Static by design. The guest's only route is the host side of its tap, and the
// only thing it may reach there is the proxy, so a name never reaches netfilter
// and there is nothing per-destination to render: the proxy is the one matcher
// for names and addresses alike. UDP/53 is covered by the same drop, which is
// what closes the resolver side channel an internal docker network leaves open
// (spec 6/41 §"Egress by name, sealed below the kernel"; BUGS L136, L189).
func tapRules(rec *vmRecord) [][]string {
	port := strconv.Itoa(rec.ProxyPort)
	return [][]string{
		{"INPUT", "-i", rec.Tap, "-s", rec.GuestIP, "-d", rec.HostIP,
			"-p", "tcp", "--dport", port, "-j", "ACCEPT"},
		{"OUTPUT", "-o", rec.Tap, "-s", rec.HostIP, "-d", rec.GuestIP,
			"-p", "tcp", "--sport", port, "-j", "ACCEPT"},
		{"INPUT", "-i", rec.Tap, "-j", "DROP"},
		{"OUTPUT", "-o", rec.Tap, "-j", "DROP"},
		{"FORWARD", "-i", rec.Tap, "-j", "DROP"},
		{"FORWARD", "-o", rec.Tap, "-j", "DROP"},
	}
}

// rulesUp inserts the rule set. Inserted in reverse so the rendered order is the
// order the chain reads: -I prepends, so the last insert sits on top.
//
// A failure here fails the turn. A tap that is up with a partial rule set is a
// guest with a route it was never granted, which is the exact inversion of the
// property the proxy exists for.
func (q *qemuRuntime) rulesUp(rec *vmRecord) error {
	rules := tapRules(rec)
	for i := len(rules) - 1; i >= 0; i-- {
		r := rules[i]
		// Insert if absent. A duplicate rule is sticky: the teardown deletes one
		// instance of it, so the second would outlive the guest it was written
		// for and apply to whatever takes the tap name next.
		if exec.Command(q.paths.Firewall, append([]string{"-C", r[0]}, r[1:]...)...).Run() == nil {
			continue
		}
		args := append([]string{"-I", r[0], "1"}, r[1:]...)
		if out, err := exec.Command(q.paths.Firewall, args...).CombinedOutput(); err != nil {
			return fmt.Errorf("%s %s: %w (%s)", q.paths.Firewall,
				strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// rulesDown deletes exactly what rulesUp inserted, re-rendered from the record
// so a disk scan after a crash removes the same six rules.
func (q *qemuRuntime) rulesDown(rec *vmRecord) {
	if rec.Tap == "" || rec.GuestIP == "" {
		return
	}
	for _, r := range tapRules(rec) {
		args := append([]string{"-D", r[0]}, r[1:]...)
		if out, err := exec.Command(q.paths.Firewall, args...).CombinedOutput(); err != nil {
			slog.Warn("tap rule delete", "rule", strings.Join(args, " "),
				"err", err, "out", strings.TrimSpace(string(out)))
		}
	}
}

// ipv6ConfDir is the per-device IPv6 knob tree. A var so a test can point it at
// a temp tree; production never reassigns it.
var ipv6ConfDir = "/proc/sys/net/ipv6/conf"

// sealIPv6 removes the IPv6 address family from the tap, before the link comes up.
//
// The rule set is iptables, so it says nothing about IPv6 (`tapRules`). Without
// this seal the guest and the host side of the tap each take a link-local address
// and the guest reaches every IPv6 listener in runed's namespace — runed's own
// :8080 among them, because Go's ":8080" binds the wildcard in both families. The
// drops the guest is supposed to face are in the wrong table to see any of it.
//
// It is closed today only by the environment: docker sets disable_ipv6=1 in a
// container on a v4-only network (measured in arizuko_runed_krons). An operator
// who enables IPv6 on the compose network opens it, and a boundary that rests on
// somebody else's default is not a boundary (BUGS `L383`).
//
// A kernel built without IPv6 has no knob and nothing to disable, so an absent
// path is not an error. A knob that already reads 1 — the tap inherits
// `default.disable_ipv6`, which docker sets on a v4-only network — needs no
// write, and the write would not take: /proc/sys is read-only in the runed
// container, caps or not (measured on sloth 2026-10-05, 33 turns refused in
// two minutes, BUGS `L395`). So the write is attempted only when the family is
// open, and then a write that fails IS an error: a turn must never run with an
// address family its rule set cannot see.
func sealIPv6(tap string) error {
	p := filepath.Join(ipv6ConfDir, tap, "disable_ipv6")
	have, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("seal ipv6 on %s: %w", tap, err)
	}
	if strings.TrimSpace(string(have)) == "1" {
		return nil
	}
	if err := os.WriteFile(p, []byte("1\n"), 0o644); err != nil {
		return fmt.Errorf("seal ipv6 on %s: %w", tap, err)
	}
	return nil
}

// tapUp creates the guest's tap, seals IPv6 on it, and addresses the host side.
//
// The seal sits between the creation and the link coming up, because a link that
// is up has its link-local address already.
func (q *qemuRuntime) tapUp(rec *vmRecord) error {
	run := func(s ...string) error {
		if out, err := exec.Command(q.paths.IP, s...).CombinedOutput(); err != nil {
			return fmt.Errorf("ip %s: %w (%s)", strings.Join(s, " "), err,
				strings.TrimSpace(string(out)))
		}
		return nil
	}
	if err := run("tuntap", "add", "dev", rec.Tap, "mode", "tap"); err != nil {
		q.tapDown(rec.Tap)
		return err
	}
	if err := sealIPv6(rec.Tap); err != nil {
		q.tapDown(rec.Tap)
		return err
	}
	for _, s := range [][]string{
		{"addr", "add", fmt.Sprintf("%s/%d", rec.HostIP, tapPrefix), "dev", rec.Tap},
		{"link", "set", rec.Tap, "up"},
	} {
		if err := run(s...); err != nil {
			q.tapDown(rec.Tap)
			return err
		}
	}
	return nil
}

// tapDown removes the tap. Deleting the link drops its addresses with it.
func (q *qemuRuntime) tapDown(tap string) {
	if tap == "" {
		return
	}
	if out, err := exec.Command(q.paths.IP, "tuntap", "del", "dev", tap, "mode", "tap").
		CombinedOutput(); err != nil {
		slog.Warn("tap delete", "tap", tap, "err", err, "out", strings.TrimSpace(string(out)))
	}
}

// egressArgs renders the proxy's argument list for one turn. The allowlist and
// the gateway host come from container.EgressRules, which is the same
// translation the Docker substrate's proxy gets: `*` becomes --allow-all and the
// configured gateway is allowed by construction, in ONE place, so the two
// substrates cannot disagree about what a folder may reach.
func egressArgs(rec *vmRecord, allowlist []string, gatewayURL string) []string {
	names, allowAll := container.EgressRules(allowlist, gatewayURL)
	args := []string{
		"--listen", net.JoinHostPort(rec.HostIP, strconv.Itoa(rec.ProxyPort)),
		// --client pins the proxy to this one guest. Nothing else can reach the
		// tap's host side, but the pin is what makes that a property of the
		// proxy rather than of the rule set alone.
		"--client", rec.GuestIP,
		"--allow", strings.Join(names, ","),
	}
	if allowAll {
		args = append(args, "--allow-all")
	}
	return args
}

// startEgress starts this turn's proxy and proves it healthy before the guest
// exists. A failure fails the turn: booting the guest anyway would put an
// unfiltered guest on the tap.
func (q *qemuRuntime) startEgress(rec *vmRecord, allowlist []string) (*exec.Cmd, string, error) {
	args := egressArgs(rec, allowlist, q.cfg.ModelGatewayURL)
	cmd := exec.Command(q.paths.Egress, args...)
	cmd.Stderr = newPrefixWriter("egress", rec.Folder)
	if err := cmd.Start(); err != nil {
		return nil, "", fmt.Errorf("egress proxy start: %w", err)
	}
	proxyURL := "http://" + net.JoinHostPort(rec.HostIP, strconv.Itoa(rec.ProxyPort))
	if err := container.WaitHealthy(proxyURL+egress.HealthPath, egressReady); err != nil {
		_ = cmd.Process.Kill()
		return nil, "", fmt.Errorf("egress proxy not ready: %w", err)
	}
	slog.Info("egress proxy up", "folder", rec.Folder, "pid", cmd.Process.Pid,
		"listen", rec.HostIP, "guest", rec.GuestIP, "rules", len(allowlist))
	return cmd, proxyURL, nil
}

// egressReady bounds the wait for the proxy's /health, matching the Docker
// substrate's own bound.
const egressReady = 15 * time.Second

// proxyEnv is the four variables the guest needs to use its proxy. A substrate
// adds them because their value is its own address (spec 6/41 §"The seam").
func proxyEnv(env map[string]string, proxyURL string) {
	env["HTTP_PROXY"] = proxyURL
	env["HTTPS_PROXY"] = proxyURL
	env["NO_PROXY"] = "localhost,127.0.0.1"
	env["NODE_OPTIONS"] = "--require=/app/proxy-shim.js"
}

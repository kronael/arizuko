// Package run is what crackbox IS: the composition of the two halves it
// ships separately — the egress proxy and the sandbox — into one command.
//
//	crackbox run --allow a,b -- cmd     container sandbox, egress filtered by the proxy
//	crackbox run --kvm --allow a,b      KVM sandbox, egress filtered on its tap
//
// Neither half needs the other. Take egress/ for a forward proxy, sandbox/
// for a KVM library; this package is the convenience of having both.
package run

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/kronael/arizuko/egress"
	"github.com/kronael/arizuko/sandbox"
)

const (
	proxyPort    = 3128
	readyTimeout = 15 * time.Second
	readyPoll    = 20 * time.Millisecond
)

type Args struct {
	Image     string   // user's container image (docker mode)
	Cmd       []string // command + args inside the sandbox
	Allow     []string // allowlist
	ID        string   // optional, generated if empty
	ProxyImg  string   // image holding the egress binary
	NetSubnet string   // sandbox network CIDR (docker mode)
	Uplink    string   // docker network giving the proxy its route out
	KVM       bool     // run the command in a KVM VM instead of a container
	DataDir   string   // VM state root (KVM mode)
	Quiet     bool     // suppress crackbox-prefixed status lines
}

// Run executes the command in a sandbox with filtered egress and returns its
// exit code. Blocks until the command exits, then tears everything down.
func Run(a Args) (int, error) {
	if len(a.Cmd) == 0 && !a.KVM {
		return 2, fmt.Errorf("command required")
	}
	if a.ID == "" {
		a.ID = "crackbox-" + randTag()
	}
	if a.KVM {
		return runKVM(a)
	}
	return runContainer(a)
}

// runContainer composes egress + docker: the sandbox goes on an --internal
// network with no route out, and its own proxy — holding the allowlist and
// pinned to its address — is the only way through.
func runContainer(a Args) (int, error) {
	if a.Image == "" {
		return 2, fmt.Errorf("image required")
	}
	if a.ProxyImg == "" {
		a.ProxyImg = "arizuko-egress:latest"
	}
	if a.NetSubnet == "" {
		a.NetSubnet = "10.88.0.0/24"
	}
	if a.Uplink == "" {
		a.Uplink = "bridge"
	}

	tag := randTag()
	netName := "crackbox-" + tag
	proxyName := "crackbox-proxy-" + tag
	userName := "crackbox-user-" + tag

	// --internal is half the enforcement: without it the sandbox reaches the
	// internet directly and the proxy is decoration.
	logf(a, "creating network %s", netName)
	if err := docker("network", "create", "--internal",
		"--subnet", a.NetSubnet, netName); err != nil {
		return 1, fmt.Errorf("network create: %w", err)
	}
	defer docker("network", "rm", netName) //nolint:errcheck // teardown

	// Both addresses are assigned before either container starts, so the proxy
	// can be pinned to the sandbox's address from the moment it binds.
	userIP, proxyIP, err := pickPair(a.NetSubnet)
	if err != nil {
		return 1, fmt.Errorf("pick addresses: %w", err)
	}

	logf(a, "starting proxy %s @ %s", proxyName, proxyIP)
	if err := docker("run", "-d", "--rm",
		"--name", proxyName, "--network", netName, "--ip", proxyIP,
		"--cap-drop", "ALL", "--read-only",
		a.ProxyImg,
		"--listen", fmt.Sprintf(":%d", proxyPort),
		"--client", userIP,
		"--allow", strings.Join(a.Allow, ",")); err != nil {
		return 1, fmt.Errorf("proxy run: %w", err)
	}
	defer docker("rm", "-f", proxyName) //nolint:errcheck // teardown

	// The uplink is the proxy's route out — and the only one in the sandbox's
	// reach, since the sandbox is not attached to it.
	if err := docker("network", "connect", a.Uplink, proxyName); err != nil {
		return 1, fmt.Errorf("proxy uplink %s: %w", a.Uplink, err)
	}
	uplinkIP, err := containerIP(proxyName, a.Uplink)
	if err != nil {
		return 1, fmt.Errorf("proxy uplink ip: %w", err)
	}
	// Start the sandbox only once the proxy answers. A sleep would race; a
	// TCP connect would pass before the handler serves.
	if err := waitHealthy(fmt.Sprintf("http://%s%s",
		net.JoinHostPort(uplinkIP, fmt.Sprint(proxyPort)), egress.HealthPath)); err != nil {
		return 1, fmt.Errorf("proxy not ready: %w", err)
	}

	proxyURL := fmt.Sprintf("http://%s:%d", proxyIP, proxyPort)
	logf(a, "creating sandbox %s @ %s (proxy %s)", userName, userIP, proxyURL)
	createArgs := []string{"create",
		"--name", userName, "--network", netName, "--ip", userIP,
		"-i",
		"-e", "HTTP_PROXY=" + proxyURL,
		"-e", "HTTPS_PROXY=" + proxyURL,
		"-e", "NO_PROXY=localhost,127.0.0.1",
		a.Image,
	}
	createArgs = append(createArgs, a.Cmd...)
	if err := docker(createArgs...); err != nil {
		return 1, fmt.Errorf("sandbox create: %w", err)
	}
	defer docker("rm", "-f", userName) //nolint:errcheck // teardown

	logf(a, "starting + attaching")
	cmd := exec.Command("docker", "start", "-a", userName)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stop
		logf(a, "interrupt — killing sandbox")
		_ = exec.Command("docker", "kill", userName).Run()
	}()

	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode(), nil
		}
		return 1, err
	}
	return 0, nil
}

// runKVM composes the other half: a qemu VM whose outbound traffic is
// filtered by netfilter rules on its own tap device, from the same allowlist
// the proxy would match. Needs KVM, qemu and root.
func runKVM(a Args) (int, error) {
	if a.DataDir == "" {
		return 2, fmt.Errorf("--data-dir required for --kvm")
	}
	h, err := sandbox.New(strings.ReplaceAll(a.ID, "-", "_"), a.DataDir)
	if err != nil {
		return 1, fmt.Errorf("sandbox: %w", err)
	}
	logf(a, "spawning vm (allow %v)", a.Allow)
	handle, err := h.Spawn(sandbox.VMConfig{AllowList: a.Allow})
	if err != nil {
		return 1, fmt.Errorf("spawn vm: %w", err)
	}
	defer func() {
		if err := h.Stop(handle); err != nil {
			fmt.Fprintf(os.Stderr, "crackbox: stop vm %s: %v\n", handle.ID, err)
		}
	}()
	logf(a, "vm %s up @ %s", handle.ID, handle.IP)
	if len(a.Cmd) == 0 {
		return 0, nil
	}
	code, stdout, stderr, err := h.Exec(handle, a.Cmd, os.Stdin)
	os.Stdout.Write(stdout)
	os.Stderr.Write(stderr)
	if err != nil {
		return 1, fmt.Errorf("exec in vm: %w", err)
	}
	return code, nil
}

func docker(args ...string) error {
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return nil
}

func containerIP(name, network string) (string, error) {
	out, err := exec.Command("docker", "inspect", "-f",
		fmt.Sprintf(`{{(index .NetworkSettings.Networks %q).IPAddress}}`, network),
		name).Output()
	if err != nil {
		return "", err
	}
	ip := strings.TrimSpace(string(out))
	if ip == "" || ip == "<no value>" {
		return "", fmt.Errorf("no address on %s", network)
	}
	return ip, nil
}

// waitHealthy polls the proxy's /health until it answers 200.
func waitHealthy(url string) error {
	c := &http.Client{Timeout: 2 * time.Second}
	end := time.Now().Add(readyTimeout)
	var last error
	for time.Now().Before(end) {
		resp, err := c.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			last = fmt.Errorf("health %s", resp.Status)
		} else {
			last = err
		}
		time.Sleep(readyPoll)
	}
	if last == nil {
		last = fmt.Errorf("timed out")
	}
	return last
}

func randTag() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// pickPair returns two distinct host addresses in subnet.
// ONE DRAW, and the second address is its neighbour. Two independent draws over
// ~250 usable hosts collide about once in 250, and the collision is silent
// (BUGS L240). Deriving the second from the first makes it impossible.
func pickPair(subnet string) (userIP, proxyIP string, _ error) {
	userIP, err := pickIP(subnet)
	if err != nil {
		return "", "", err
	}
	proxyIP, err = neighbourIP(userIP, subnet)
	if err != nil {
		return "", "", err
	}
	return userIP, proxyIP, nil
}

// neighbourIP is the address one above ip, wrapped back into the subnet's
// usable range — the same reserved ends pickIP avoids.
func neighbourIP(ip, subnet string) (string, error) {
	_, n, err := net.ParseCIDR(subnet)
	if err != nil {
		return "", fmt.Errorf("subnet %s: %w", subnet, err)
	}
	ip4 := net.ParseIP(ip).To4()
	if ip4 == nil {
		return "", fmt.Errorf("address %s: ipv4 only", ip)
	}
	ones, _ := n.Mask.Size()
	mask := uint32(1)<<uint(32-ones) - 1
	netInt := binary.BigEndian.Uint32(n.IP.To4()) &^ mask
	host := binary.BigEndian.Uint32(ip4)&mask + 1
	if host >= mask {
		host = 4
	}
	var out [4]byte
	binary.BigEndian.PutUint32(out[:], netInt|host)
	return net.IP(out[:]).String(), nil
}

// pickIP returns a random host address in subnet, avoiding the network
// address, the gateway (.1), .2 (docker's first auto-assign) and broadcast.
func pickIP(subnet string) (string, error) {
	_, n, err := net.ParseCIDR(subnet)
	if err != nil {
		return "", fmt.Errorf("parse subnet %q: %w", subnet, err)
	}
	ip4 := n.IP.To4()
	if ip4 == nil {
		return "", fmt.Errorf("ipv6 subnet not supported: %s", subnet)
	}
	ones, bits := n.Mask.Size()
	hostBits := bits - ones
	if hostBits < 3 {
		return "", fmt.Errorf("subnet %s too small (need /29 or larger)", subnet)
	}
	size := uint32(1) << uint(hostBits)
	const reservedLow = 3
	usable := size - reservedLow - 1
	var rb [4]byte
	if _, err := rand.Read(rb[:]); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	off := reservedLow + binary.BigEndian.Uint32(rb[:])%usable
	out := make(net.IP, 4)
	binary.BigEndian.PutUint32(out, binary.BigEndian.Uint32(ip4)+off)
	return out.String(), nil
}

func logf(a Args, format string, args ...any) {
	if a.Quiet {
		return
	}
	fmt.Fprintf(os.Stderr, "crackbox: "+format+"\n", args...)
}

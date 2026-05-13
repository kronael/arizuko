// Package run is the convenience wrapper for the `crackbox run` subcommand:
// spin up a Docker network + crackbox proxy daemon container + register the
// allowlist + run the user's command in a container with HTTPS_PROXY set,
// tear down on exit. This is sugar — there is no separate single-shot proxy
// implementation. The proxy that runs is the same daemon as `crackbox proxy
// serve`; we just kill it when the user's command exits.
package run

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/kronael/arizuko/crackbox/pkg/client"
)

type Args struct {
	Image     string   // user's container image
	Cmd       []string // command + args inside the container
	Allow     []string // allowlist
	ID        string   // optional, generated if empty
	ProxyImg  string   // crackbox proxy image (default: crackbox:latest)
	NetSubnet string   // default 10.88.0.0/16
	Quiet     bool     // suppress non-error log output
}

// Run orchestrates the full single-shot lifecycle. Blocks until the user
// command exits, then tears down all created Docker resources.
func Run(a Args) (int, error) {
	if a.Image == "" {
		return 2, fmt.Errorf("image required")
	}
	if a.ProxyImg == "" {
		a.ProxyImg = "crackbox:latest"
	}
	if a.NetSubnet == "" {
		a.NetSubnet = "10.88.0.0/16"
	}
	if a.ID == "" {
		a.ID = "crackbox-" + randTag()
	}

	// Share one tag across the network + both containers so they're easy to
	// grep together in `docker ps` / `docker network ls`.
	tag := randTag()
	netName := "crackbox-" + tag
	proxyName := "crackbox-proxy-" + tag
	userName := "crackbox-user-" + tag

	logf(a, "creating network %s", netName)
	if err := docker("network", "create", "--internal=false", "--subnet", a.NetSubnet, netName); err != nil {
		return 1, fmt.Errorf("network create: %w", err)
	}
	defer docker("network", "rm", netName)

	logf(a, "starting proxy %s", proxyName)
	if err := docker("run", "-d", "--rm",
		"--name", proxyName, "--network", netName,
		a.ProxyImg, "proxy", "serve"); err != nil {
		return 1, fmt.Errorf("proxy run: %w", err)
	}
	defer docker("kill", proxyName)

	proxyIP, err := waitForIP(proxyName, netName, 5*time.Second)
	if err != nil {
		return 1, fmt.Errorf("proxy ip: %w", err)
	}

	cli := client.New(fmt.Sprintf("http://%s:3129", proxyIP), "")

	// Wait for the admin listener to actually answer. Without this, a
	// --quiet run races (no sleep) and the first Register hits a closed
	// port. Poll instead of sleeping a fixed duration.
	if err := waitHealthy(cli, 5*time.Second); err != nil {
		return 1, fmt.Errorf("proxy health: %w", err)
	}

	// Pre-assign the user container's IP so we can register it before the
	// container starts. Otherwise IP is only known post-start, and a
	// fast-running command can fire (and fail) traffic before register
	// completes.
	userIP, err := pickUserIP(a.NetSubnet)
	if err != nil {
		return 1, fmt.Errorf("pick user ip: %w", err)
	}
	logf(a, "registering %s -> %v", userIP, a.Allow)
	if err := cli.Register(userIP, a.ID, a.Allow); err != nil {
		return 1, fmt.Errorf("register: %w", err)
	}

	logf(a, "creating user container %s @ %s", userName, userIP)
	createArgs := []string{"create",
		"--name", userName, "--network", netName, "--ip", userIP,
		"--dns", proxyIP,
		"-i",
		"-e", fmt.Sprintf("HTTP_PROXY=http://%s:3128", proxyIP),
		"-e", fmt.Sprintf("HTTPS_PROXY=http://%s:3128", proxyIP),
		"-e", "NO_PROXY=localhost,127.0.0.1",
		a.Image,
	}
	createArgs = append(createArgs, a.Cmd...)
	if err := docker(createArgs...); err != nil {
		return 1, fmt.Errorf("user create: %w", err)
	}
	defer docker("rm", "-f", userName)

	logf(a, "starting + attaching")
	cmd := exec.Command("docker", "start", "-a", userName)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stop
		logf(a, "interrupt — killing user container")
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

func docker(args ...string) error {
	cmd := exec.Command("docker", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		slog.Debug("docker fail", "args", args, "out", string(out), "err", err)
		return fmt.Errorf("docker %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return nil
}

func waitForIP(name, network string, deadline time.Duration) (string, error) {
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		out, err := exec.Command("docker", "inspect", "-f",
			fmt.Sprintf(`{{(index .NetworkSettings.Networks %q).IPAddress}}`, network),
			name).Output()
		if err == nil {
			ip := strings.TrimSpace(string(out))
			if ip != "" && ip != "<no value>" {
				return ip, nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return "", fmt.Errorf("timed out waiting for ip on %s", name)
}

func randTag() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// pickUserIP returns a random host IP within the IPv4 subnet, avoiding
// the network address, the gateway (.1), and the broadcast address.
// Rejects IPv6 and any prefix that yields fewer than 8 host addresses.
func pickUserIP(subnet string) (string, error) {
	_, n, err := net.ParseCIDR(subnet)
	if err != nil {
		return "", fmt.Errorf("parse subnet %q: %w", subnet, err)
	}
	ip4 := n.IP.To4()
	if ip4 == nil {
		return "", fmt.Errorf("ipv6 subnet not supported: %s", subnet)
	}
	ones, bits := n.Mask.Size()
	if bits != 32 {
		return "", fmt.Errorf("non-ipv4 mask in %s", subnet)
	}
	hostBits := bits - ones
	if hostBits < 3 {
		return "", fmt.Errorf("subnet %s too small (need /29 or larger)", subnet)
	}
	size := uint32(1) << uint(hostBits)
	// reserve .0 (network), .1 (gateway), broadcast (size-1), and avoid .2
	// which docker often assigns to the first attached container.
	const reservedLow = 3
	usable := size - reservedLow - 1
	if usable < 1 {
		return "", fmt.Errorf("subnet %s yields no usable host addresses", subnet)
	}
	var rb [4]byte
	if _, err := rand.Read(rb[:]); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	off := reservedLow + binary.BigEndian.Uint32(rb[:])%usable
	base := binary.BigEndian.Uint32(ip4)
	out := make(net.IP, 4)
	binary.BigEndian.PutUint32(out, base+off)
	return out.String(), nil
}

// waitHealthy polls the admin /health endpoint until it answers 2xx or
// the deadline elapses. Replaces the racy fixed sleep that --quiet
// previously skipped entirely.
func waitHealthy(cli *client.Client, deadline time.Duration) error {
	end := time.Now().Add(deadline)
	var lastErr error
	for time.Now().Before(end) {
		if err := cli.Health(); err == nil {
			return nil
		} else {
			lastErr = err
		}
		time.Sleep(50 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("timed out")
	}
	return lastErr
}

func logf(a Args, format string, args ...interface{}) {
	if a.Quiet {
		return
	}
	fmt.Fprintf(os.Stderr, "crackbox: "+format+"\n", args...)
}

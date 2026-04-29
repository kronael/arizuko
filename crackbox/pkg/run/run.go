// Package run is the convenience wrapper for the `crackbox run` subcommand:
// spin up a Docker network + crackbox proxy daemon container + register the
// allowlist + run the user's command in a container with HTTPS_PROXY set,
// tear down on exit. This is sugar — there is no separate single-shot proxy
// implementation. The proxy that runs is the same daemon as `crackbox proxy
// serve`; we just kill it when the user's command exits.
package run

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/onvos/arizuko/crackbox/pkg/client"
)

type Args struct {
	Image     string   // user's container image
	Cmd       []string // command + args inside the container
	Allow     []string // allowlist
	ID        string   // optional, generated if empty
	ProxyImg  string   // crackbox proxy image (default: crackbox:latest)
	NetSubnet string   // default 10.99.0.0/16
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
		a.NetSubnet = "10.99.0.0/16"
	}
	if a.ID == "" {
		a.ID = "crackbox-" + randTag()
	}

	netName := "crackbox-" + randTag()
	proxyName := "crackbox-proxy-" + randTag()
	userName := "crackbox-user-" + randTag()

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
	if !a.Quiet {
		// proxy needs a moment for its admin listener to bind.
		time.Sleep(300 * time.Millisecond)
	}

	cli := client.New(fmt.Sprintf("http://%s:3129", proxyIP))

	// Pre-create the user container (without starting) so we know its IP
	// before traffic flows. `docker create` returns the container ID.
	logf(a, "creating user container %s", userName)
	createArgs := []string{"create",
		"--name", userName, "--network", netName,
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

	userIP, err := waitForIP(userName, netName, 2*time.Second)
	if err != nil {
		return 1, fmt.Errorf("user ip: %w", err)
	}
	logf(a, "registering %s -> %v", userIP, a.Allow)
	if err := cli.Register(userIP, a.ID, a.Allow); err != nil {
		return 1, fmt.Errorf("register: %w", err)
	}

	logf(a, "starting user container")
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
			fmt.Sprintf("{{.NetworkSettings.Networks.%s.IPAddress}}", network),
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

func logf(a Args, format string, args ...interface{}) {
	if a.Quiet {
		return
	}
	fmt.Fprintf(os.Stderr, "crackbox: "+format+"\n", args...)
}

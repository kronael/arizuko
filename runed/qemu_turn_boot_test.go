package runed

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"golang.org/x/sys/unix"
)

const turnProofEntry = `#!/bin/sh
set -eu
IFS= read -r payload
[ "$payload" = turn-stdin ]
[ "$(id -u)" = 1000 ]
[ "$(nproc)" = 2 ]
printf 'started\n' > /home/node/started
printf '%s\n' "$payload" > /home/node/stdin
if [ "$TURN_MODE" = normal ]; then
  python3 -c '
import socket
with socket.socket(socket.AF_UNIX) as conn:
    conn.settimeout(5)
    conn.connect("/run/ipc/gated.sock")
    conn.sendall(b"{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"turn_probe\"}\n")
    reply = conn.makefile("rb").readline()
    assert reply
with open("/home/node/mcp", "wb") as output:
    output.write(reply)
'
  status=$(curl --proxy "$HTTP_PROXY" -sS --max-time 4 -o /home/node/allowed -w '%{http_code}' "http://$TURN_HOST:$TURN_PORT/allow")
  [ "$status" = 200 ]
  status=$(curl --proxy "$HTTP_PROXY" -sS --max-time 4 -o /dev/null -w '%{http_code}' http://denied.invalid/)
  [ "$status" = 403 ]
  if curl --noproxy '*' -sS --max-time 2 "http://$TURN_IP:$TURN_PORT/allow" >/dev/null 2>&1; then exit 31; fi
  if dig +time=1 +tries=1 "@$TURN_IP" denied.invalid >/dev/null 2>&1; then exit 32; fi
  printf 'blocked\n' > /home/node/network
fi
trap 'printf "terminated\n" > /home/node/terminated; exit 0' TERM
finish() {
  for file in /run/ipc/input/*.json; do [ -f "$file" ] && cat "$file" > /home/node/steer; done
  python3 -c 'open("/home/node/exit-write", "wb").write(b"x" * 1048576)'
  exit 0
}
trap finish USR1
printf 'ready\n' > /home/node/ready
while :; do sleep 0.1; done
`

func TestGuestTurnOverCtl(t *testing.T) {
	if testing.Short() {
		t.Skip("needs isolated KVM benchmark environment")
	}
	for _, key := range []string{
		"RUNED_BENCH_ID", "RUNED_BENCH_OUTPUT_DIR", envKernelDir,
		envRootfsDir, envCIDBase, envVsockBase, envTapSubnet,
	} {
		if os.Getenv(key) == "" {
			t.Fatalf("%s must be set for an isolated turn test", key)
		}
	}
	root, err := os.MkdirTemp(os.Getenv("RUNED_BENCH_OUTPUT_DIR"), "turn-proof-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanTurnProof(t, root) })
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &core.Config{
		Name: os.Getenv("RUNED_BENCH_ID"), ProjectRoot: root,
		GroupsDir: filepath.Join(root, "groups"), IpcDir: filepath.Join(root, "ipc"),
		MaxContainers: 1,
	}
	for _, dir := range []string{
		filepath.Join(root, "app"), filepath.Join(cfg.IpcDir, "solo", "inbox"),
		filepath.Join(cfg.GroupsDir, "solo", "inbox"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	entry := filepath.Join(root, "app", "entrypoint.sh")
	if err := os.WriteFile(entry, []byte(turnProofEntry), 0o755); err != nil {
		t.Fatal(err)
	}
	address, err := turnProofAddress()
	if err != nil {
		t.Fatal(err)
	}
	httpL, err := net.Listen("tcp", net.JoinHostPort(address, "0"))
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/allow" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, "allowed-through-proxy\n")
	})}
	go func() { _ = server.Serve(httpL) }()
	t.Cleanup(func() { _ = server.Close() })

	dns, err := net.ListenPacket("udp", net.JoinHostPort(address, "53"))
	if err != nil {
		t.Fatal(err)
	}
	var dnsRequests atomic.Int32
	dnsDone := make(chan struct{})
	go func() {
		defer close(dnsDone)
		var buf [512]byte
		for {
			if _, _, err := dns.ReadFrom(buf[:]); err != nil {
				return
			}
			dnsRequests.Add(1)
		}
	}()
	t.Cleanup(func() { _ = dns.Close(); <-dnsDone })

	socket := filepath.Join(cfg.IpcDir, "solo", "inbox", "gated.sock")
	mcp, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(socket, 0o666); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mcp.Close() })
	bridgeProof := make(chan error, 1)
	go serveTurnProofMCP(mcp, bridgeProof)

	runtime, _, err := NewQemuRuntime(cfg, resolverFor(cfg))
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(httpL.Addr().(*net.TCPAddr).Port)
	for _, mode := range []string{"normal", "kill", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			home := filepath.Join(root, "home-"+mode)
			if err := os.Mkdir(home, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Chown(home, 1000, 1000); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
			defer cancel()
			steers := make(chan func(string) bool, 1)
			handle := "turn-proof-" + mode
			turn := Turn{
				Handle: handle, Folder: "solo/inbox",
				Mounts: []core.Mount{
					{Host: home, Container: core.ContainerHome},
					{Host: entry, Container: "/app/entrypoint.sh", RO: true},
				},
				Env: map[string]string{
					"HOME": core.ContainerHome, "TURN_MODE": mode,
					"TURN_HOST": cfg.Name, "TURN_IP": address, "TURN_PORT": port,
				},
				Stdin: []byte("turn-stdin\n"), Egress: []string{cfg.Name},
				RegisterSteer: func(steer func(string) bool) { steers <- steer },
			}
			result := make(chan RunResult, 1)
			started := time.Now()
			go func() { result <- runtime.Run(ctx, turn) }()
			ended := false
			defer func() {
				if ended {
					return
				}
				cancel()
				select {
				case <-result:
				case <-time.After(10 * time.Second):
					_ = runtime.Kill(handle)
				}
			}()
			waitTurnProof(t, filepath.Join(home, "started"), result, &ended)
			t.Logf("TURN_PROBE_START mode=%s run_to_marker_ms=%d",
				mode, time.Since(started).Milliseconds())
			waitTurnProof(t, filepath.Join(home, "ready"), result, &ended)
			rec, err := loadVMRecord(filepath.Join(root, "vms", handle))
			if err != nil {
				t.Fatal(err)
			}
			var fs unix.Statfs_t
			if err := unix.Statfs(filepath.Join(rec.TurnDir, rootfsDirName), &fs); err != nil || fs.Flags&unix.ST_RDONLY == 0 {
				t.Fatalf("guest rootfs export is not read-only: flags=%x err=%v", fs.Flags, err)
			}
			switch mode {
			case "normal":
				select {
				case err := <-bridgeProof:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("MCP bridge did not deliver a request")
				}
				var steer func(string) bool
				select {
				case steer = <-steers:
				case <-time.After(5 * time.Second):
					t.Fatal("runtime did not register steering")
				}
				if !steer("steered-through-ctl") {
					t.Fatal("guest did not acknowledge the steer")
				}
			case "kill":
				if err := runtime.Kill(handle); err != nil {
					t.Fatal(err)
				}
			case "cancel":
				cancel()
			}
			var got RunResult
			select {
			case got = <-result:
				ended = true
			case <-time.After(15 * time.Second):
				t.Fatal("Run did not finish after turn end")
			}
			t.Logf("TURN_PROOF mode=%s outcome=%s code=%d error=%q", mode, got.Outcome, got.ExitCode, got.Error)
			if mode == "normal" {
				if got.Outcome != "ok" || got.ExitCode != 0 {
					t.Fatalf("normal turn result: %+v", got)
				}
				for file, want := range map[string]string{
					"stdin": "turn-stdin", "mcp": `"ok":true`,
					"allowed": "allowed-through-proxy", "network": "blocked",
					"steer": "steered-through-ctl",
				} {
					b, err := os.ReadFile(filepath.Join(home, file))
					if err != nil || !strings.Contains(string(b), want) {
						t.Errorf("%s = %q, err=%v; want %q", file, b, err, want)
					}
				}
				fi, err := os.Stat(filepath.Join(home, "stdin"))
				if err != nil {
					t.Fatal(err)
				}
				if stat := fi.Sys().(*syscall.Stat_t); stat.Uid != 1000 {
					t.Errorf("guest write uid=%d, want 1000", stat.Uid)
				}
				written, err := os.ReadFile(filepath.Join(home, "exit-write"))
				if err != nil || !bytes.Equal(written, bytes.Repeat([]byte("x"), 1<<20)) {
					t.Errorf("final shared write after shutdown: bytes=%d err=%v", len(written), err)
				}
			} else if got.Outcome != "error" {
				t.Fatalf("%s turn result: %+v", mode, got)
			}
			if _, err := os.Stat(rec.TurnDir); !os.IsNotExist(err) {
				t.Errorf("turn directory survived: %v", err)
			}
			mountInfo, err := os.Open("/proc/self/mountinfo")
			if err != nil {
				t.Fatal(err)
			}
			if mounts := mountedUnder(mountInfo, rec.TurnDir); len(mounts) != 0 {
				t.Errorf("turn mounts survived: %v", mounts)
			}
			if err := mountInfo.Close(); err != nil {
				t.Error(err)
			}
			if _, err := net.InterfaceByName(rec.Tap); err == nil {
				t.Errorf("tap %s survived", rec.Tap)
			}
			for _, pid := range append([]int{rec.QemuPid, rec.EgressPid, rec.BridgePid}, rec.VirtiofsdPids...) {
				if pid > 0 && syscall.Kill(pid, 0) == nil {
					t.Errorf("turn pid %d survived", pid)
				}
			}
		})
	}
	_ = dns.Close()
	<-dnsDone
	if dnsRequests.Load() != 0 {
		t.Errorf("guest DNS reached host listener %d times", dnsRequests.Load())
	}
}

func serveTurnProofMCP(listener net.Listener, result chan<- error) {
	conn, err := listener.Accept()
	if err != nil {
		result <- err
		return
	}
	defer conn.Close()
	var cred *unix.Ucred
	raw, err := conn.(*net.UnixConn).SyscallConn()
	if err == nil {
		var credErr error
		err = raw.Control(func(fd uintptr) {
			cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		})
		if err == nil {
			err = credErr
		}
	}
	if err == nil && (cred == nil || cred.Uid != 1000) {
		err = fmt.Errorf("MCP bridge uid=%v, want 1000", cred)
	}
	if err == nil {
		var line string
		line, err = bufio.NewReader(conn).ReadString('\n')
		if err == nil && !strings.Contains(line, `"method":"turn_probe"`) {
			err = fmt.Errorf("MCP request = %q", line)
		}
	}
	if err == nil {
		_, err = io.WriteString(conn, "{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"ok\":true}}\n")
	}
	result <- err
}

func turnProofAddress() (string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, err := iface.Addrs()
		if err != nil {
			return "", err
		}
		for _, addr := range addresses {
			ip, _, err := net.ParseCIDR(addr.String())
			if err == nil && ip.To4() != nil {
				return ip.String(), nil
			}
		}
	}
	return "", fmt.Errorf("no non-loopback IPv4 address")
}

func waitTurnProof(t *testing.T, path string, result <-chan RunResult, ended *bool) {
	t.Helper()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(30 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		select {
		case got := <-result:
			*ended = true
			t.Fatalf("Run ended before entrypoint readiness: %+v", got)
		case <-ticker.C:
		case <-deadline:
			t.Fatalf("entrypoint did not become ready: %s", path)
		}
	}
}

func cleanTurnProof(t *testing.T, root string) {
	t.Helper()
	for _, mode := range []string{"normal", "kill", "cancel"} {
		home := filepath.Join(root, "home-"+mode)
		for _, file := range []string{"stdin", "started", "mcp", "allowed", "network", "steer", "ready", "terminated", "exit-write", ".arizuko-probe"} {
			_ = os.Remove(filepath.Join(home, file))
		}
		_ = os.Remove(home)
	}
	logs := filepath.Join(root, "groups", "solo", "inbox", "logs")
	if entries, err := os.ReadDir(logs); err == nil {
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "container-") {
				_ = os.Remove(filepath.Join(logs, entry.Name()))
			}
		}
	}
	_ = os.Remove(filepath.Join(root, "app", "entrypoint.sh"))
	_ = os.Remove(filepath.Join(root, "ipc", "solo", "inbox", "gated.sock"))
	for _, dir := range []string{
		logs, filepath.Join(root, "groups", "solo", "inbox"),
		filepath.Join(root, "groups", "solo"), filepath.Join(root, "groups"),
		filepath.Join(root, "ipc", "solo", "inbox"), filepath.Join(root, "ipc", "solo"),
		filepath.Join(root, "ipc"), filepath.Join(root, "app"), filepath.Join(root, "vms"), root,
	} {
		if err := os.Remove(dir); err != nil && !os.IsNotExist(err) {
			t.Errorf("remove test directory %s: %v", dir, err)
		}
	}
}

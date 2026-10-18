package runed

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// The turn frame's bytes, named rather than derived. The guest parses this line
// with its own decoder in its own module, so the encoding is a contract: stdin is
// base64 because Go marshals []byte that way on both sides, and a field renamed
// on one side silently stops arriving on the other.
func TestCtlFrameEncodesTheTurnAsTheGuestReadsIt(t *testing.T) {
	got, err := encodeCtlFrame(ctlFrame{
		Kind:  ctlTurn,
		Env:   map[string]string{"TZ": "UTC"},
		Stdin: []byte("hi"),
	})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	const want = `{"kind":"turn","env":{"TZ":"UTC"},"stdin":"aGk="}` + "\n"
	if string(got) != want {
		t.Errorf("turn frame\ngot:  %s\nwant: %s", got, want)
	}
}

// The guest's four frames, as literal lines. A decoder that silently tolerated a
// missing kind would read an exit as a log line and the turn would never end.
func TestCtlFrameDecodesTheGuestsLines(t *testing.T) {
	lines := strings.Join([]string{
		`{"kind":"ready","probe":{"init_entry_uptime":"0.72"}}`,
		`{"kind":"log","line":"[ant] thinking"}`,
		`{"kind":"steer_ack","id":"3"}`,
		`{"kind":"exit","code":7}`,
	}, "\n") + "\n"
	var got []ctlFrame
	if err := decodeCtlFrames(strings.NewReader(lines), func(f ctlFrame) error {
		got = append(got, f)
		return nil
	}); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := []ctlFrame{
		{Kind: "ready", Probe: map[string]string{"init_entry_uptime": "0.72"}},
		{Kind: "log", Line: "[ant] thinking"},
		{Kind: "steer_ack", ID: "3"},
		{Kind: "exit", Code: 7},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("decoded frames\ngot:  %+v\nwant: %+v", got, want)
	}
	if err := decodeCtlFrames(strings.NewReader("not a frame\n"), func(ctlFrame) error { return nil }); err == nil {
		t.Error("a line that is not a frame must end the read, not be skipped")
	}
}

// The host's frame type and the guest's are two declarations in two modules,
// because guest-init is PID 1 in an initrd holding one binary and takes no
// dependency it can avoid. Two declarations of one wire format drift, so this
// reads the guest's actual source.
func TestCtlFrameMatchesGuestInit(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "kernel", "guest-init", "main.go"))
	if err != nil {
		t.Fatalf("read guest-init: %v", err)
	}
	block := regexp.MustCompile(`(?s)type frame struct \{(.*?)\n\}`).FindSubmatch(src)
	if block == nil {
		t.Fatal("guest-init has no `type frame struct` — the ctl protocol moved and this guard did not")
	}
	field := regexp.MustCompile(`(?m)^\s*([A-Z]\w*)\s+\S+\s+` + "`" + `json:"(\w+)`)
	guest := map[string]string{}
	for _, m := range field.FindAllStringSubmatch(string(block[1]), -1) {
		guest[m[1]] = m[2]
	}
	host := map[string]string{}
	rt := reflect.TypeOf(ctlFrame{})
	for i := range rt.NumField() {
		f := rt.Field(i)
		host[f.Name] = strings.Split(f.Tag.Get("json"), ",")[0]
	}
	if !reflect.DeepEqual(guest, host) {
		t.Errorf("the ctl frame differs between the host and the guest\nguest: %v\nhost:  %v",
			sortedPairs(guest), sortedPairs(host))
	}
}

func sortedPairs(m map[string]string) []string {
	var out []string
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

// A steer is true ONLY on the guest's ack.
//
// routd marks the turn done the moment a steer is accepted, so a steer that
// returned true on the socket write would make a user's message vanish while the
// turn read as delivered: the guest tmpfs holding the pending batch is discarded
// at power-off. False is the documented fall-through to a fresh spawn, so the
// batch is re-fed instead (spec 6/41 §"Steer and stop").
func TestSteerIsTrueOnlyOnTheGuestsAck(t *testing.T) {
	t.Run("acked", func(t *testing.T) {
		sess, guest := ctlPair(t)
		go guestAnswering(guest, func(f ctlFrame) *ctlFrame {
			if f.Kind == ctlSteer {
				return &ctlFrame{Kind: ctlSteerAck, ID: f.ID}
			}
			return nil
		})
		if !sess.steer("a message", time.Second) {
			t.Fatal("an acked steer must be true")
		}
	})
	t.Run("written but never acked", func(t *testing.T) {
		sess, guest := ctlPair(t)
		go guestAnswering(guest, func(ctlFrame) *ctlFrame { return nil })
		if sess.steer("a message", 50*time.Millisecond) {
			t.Fatal("a steer the guest never acked must be false, or the message is lost silently")
		}
	})
	t.Run("acked for another batch", func(t *testing.T) {
		sess, guest := ctlPair(t)
		go guestAnswering(guest, func(f ctlFrame) *ctlFrame {
			if f.Kind == ctlSteer {
				return &ctlFrame{Kind: ctlSteerAck, ID: "another"}
			}
			return nil
		})
		if sess.steer("a message", 50*time.Millisecond) {
			t.Fatal("an ack for a different batch must not satisfy this one")
		}
	})
	t.Run("guest dies holding the batch", func(t *testing.T) {
		// The guest took the frame and ended without acking. The pending batch
		// lives on a tmpfs the power-off discards, so the only safe answer is
		// false: routd has already marked the turn done on a true.
		sess, guest := ctlPair(t)
		go guestAnswering(guest, func(f ctlFrame) *ctlFrame {
			if f.Kind == ctlSteer {
				guest.Close()
			}
			return nil
		})
		if sess.steer("a message", 2*time.Second) {
			t.Fatal("a guest that died before acking must not report the steer delivered")
		}
	})
	t.Run("guest already gone", func(t *testing.T) {
		sess, guest := ctlPair(t)
		guest.Close()
		// The read loop ends; a steer after that is the documented race.
		deadline := time.Now().Add(time.Second)
		for !sess.closed.Load() && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if sess.steer("a message", time.Second) {
			t.Fatal("a steer into a dead guest must be false")
		}
	})
}

func TestExitFrameCarriesTheGuestsCode(t *testing.T) {
	sess, guest := ctlPair(t)
	go guestAnswering(guest, func(ctlFrame) *ctlFrame { return nil })
	b, _ := encodeCtlFrame(ctlFrame{Kind: ctlExit, Code: 78})
	if _, err := guest.Write(b); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-sess.exit:
		if code != 78 {
			t.Errorf("exit code %d, want 78", code)
		}
	case <-time.After(time.Second):
		t.Fatal("the exit frame never reached the session")
	}
}

// The turn's log lines land in the turn log's stderr section, which is what an
// operator reads after a failure.
func TestLogFramesReachTheTurnLog(t *testing.T) {
	var buf lineBuffer
	sess, guest := ctlPairWith(t, buf.add)
	go guestAnswering(guest, func(ctlFrame) *ctlFrame { return nil })
	b, _ := encodeCtlFrame(ctlFrame{Kind: ctlLog, Line: "[ant] hello"})
	if _, err := guest.Write(b); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for !strings.Contains(buf.String(), "[ant] hello") {
		if time.Now().After(deadline) {
			t.Fatalf("log line never reached the turn log, got %q", buf.String())
		}
		time.Sleep(time.Millisecond)
	}
	_ = sess
}

// A squatted port fails the bind and runed never moves. Measured 2026-09-23: the
// host has ONE vsock port namespace, and every holder of /dev/vhost-vsock shares
// it — including an unprivileged process and a sibling container. That cannot
// become a hijack only because runed binds before QEMU starts, so the bind error
// must fail the turn rather than be retried elsewhere.
func TestVsockListenRefusesASquattedPort(t *testing.T) {
	requireVsock(t)
	const port = 61001
	l, err := vsockListen(port)
	if err != nil {
		t.Fatalf("first bind: %v", err)
	}
	defer l.Close()
	second, err := vsockListen(port)
	if err == nil {
		second.Close()
		t.Fatal("a second bind of the same host vsock port must fail")
	}
	if !strings.Contains(err.Error(), "bind") {
		t.Errorf("the error must name the bind: %v", err)
	}
}

// The bridge serves ONE guest: the CID the host assigned. The host kernel stamps
// it, so a second guest dialing this turn's port is refused at accept — the
// acceptance test's own case (spec 6/41 §Acceptance).
func TestMCPBridgeServesOnlyItsOwnGuestCID(t *testing.T) {
	requireVsock(t)
	requireVsockLoopback(t)
	socket := filepath.Join(t.TempDir(), "gated.sock")
	ul, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("folder socket: %v", err)
	}
	// The folder socket echoes, standing in for routd's MCP server.
	echoDone := make(chan struct{})
	go func() {
		defer close(echoDone)
		for {
			c, err := ul.Accept()
			if err != nil {
				return
			}
			buf := make([]byte, 64)
			n, _ := c.Read(buf)
			c.Write(buf[:n]) //nolint:errcheck // the test reads it or times out
			if err := c.Close(); err != nil {
				t.Errorf("close echo connection: %v", err)
			}
		}
	}()
	t.Cleanup(func() {
		if err := ul.Close(); err != nil {
			t.Errorf("close folder socket: %v", err)
		}
		<-echoDone
	})

	t.Run("its own guest reaches the folder socket", func(t *testing.T) {
		l, err := vsockListen(61002)
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		// Loopback traffic carries CID 1, which is this test's stand-in for the
		// one guest the bridge was started for.
		runMCPBridgeFixture(t, unix.VMADDR_CID_LOCAL, socket, l)
		conn := vsockDial(t, unix.VMADDR_CID_LOCAL, 61002)
		defer conn.Close()
		if _, err := conn.Write([]byte("ping")); err != nil {
			t.Fatalf("write: %v", err)
		}
		conn.SetReadDeadline(time.Now().Add(2 * time.Second)) //nolint:errcheck // a miss is the timeout below
		buf := make([]byte, 4)
		if _, err := conn.Read(buf); err != nil {
			t.Fatalf("the bridge did not join this guest to the folder socket: %v", err)
		}
		if string(buf) != "ping" {
			t.Errorf("read %q, want ping", buf)
		}
	})

	t.Run("another guest is refused at accept", func(t *testing.T) {
		l, err := vsockListen(61003)
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		// A bridge pinned to a CID this dialer does not have.
		runMCPBridgeFixture(t, unix.VMADDR_CID_LOCAL+9, socket, l)
		conn := vsockDial(t, unix.VMADDR_CID_LOCAL, 61003)
		defer conn.Close()
		conn.Write([]byte("ping"))                            //nolint:errcheck // the refusal may close first
		conn.SetReadDeadline(time.Now().Add(2 * time.Second)) //nolint:errcheck // see above
		buf := make([]byte, 4)
		if n, err := conn.Read(buf); err == nil && n > 0 {
			t.Fatalf("a guest with another CID reached the folder socket and read %q", buf[:n])
		}
	})
}

func runMCPBridgeFixture(t *testing.T, cid uint32, socket string, l *os.File) {
	t.Helper()
	t.Cleanup(func() {
		if err := l.Close(); err != nil {
			t.Errorf("close bridge listener: %v", err)
		}
	})
	cmd := exec.Command(os.Args[0], mcpBridgeFlag,
		strconv.FormatUint(uint64(cid), 10), socket)
	cmd.ExtraFiles = []*os.File{l}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start bridge: %v", err)
	}
	t.Cleanup(func() {
		if err := cmd.Process.Kill(); err != nil {
			t.Errorf("stop bridge: %v", err)
		}
		err := cmd.Wait()
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Errorf("wait for killed bridge: %v", err)
			return
		}
		status, ok := exit.Sys().(syscall.WaitStatus)
		if !ok || status.Signal() != syscall.SIGKILL {
			t.Errorf("bridge exit = %v, want SIGKILL", err)
		}
	})
}

// ctlPair is a session over an in-memory duplex pair: the host end and the
// guest's.
func ctlPair(t *testing.T) (*ctlSession, net.Conn) { return ctlPairWith(t, nil) }

func ctlPairWith(t *testing.T, logf func(string)) (*ctlSession, net.Conn) {
	t.Helper()
	host, guest := net.Pipe()
	sess := newCtlSession(host, logf, nil)
	go func() { _ = sess.serve() }()
	t.Cleanup(func() { host.Close(); guest.Close() })
	return sess, guest
}

// guestAnswering reads the host's frames and answers with whatever reply returns.
func guestAnswering(guest net.Conn, reply func(ctlFrame) *ctlFrame) {
	_ = decodeCtlFrames(guest, func(f ctlFrame) error {
		if r := reply(f); r != nil {
			b, err := encodeCtlFrame(*r)
			if err != nil {
				return err
			}
			_, err = guest.Write(b)
			return err
		}
		return nil
	})
}

// requireVsock skips when this kernel has no AF_VSOCK at all. The listeners are
// bound unprivileged and need no hypervisor, so the only reason to skip is a
// kernel without the transport.
func requireVsock(t *testing.T) {
	t.Helper()
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Skipf("this kernel has no AF_VSOCK: %v", err)
	}
	unix.Close(fd)
}

func requireVsockLoopback(t *testing.T) {
	t.Helper()
	const port = 61004
	l, err := vsockListen(port)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()
	conn := vsockDial(t, unix.VMADDR_CID_LOCAL, port)
	if err := conn.Close(); err != nil {
		t.Fatalf("close loopback probe: %v", err)
	}
}

// vsockDial connects to a host-side vsock port over the local transport.
func vsockDial(t *testing.T, cid, port uint32) *os.File {
	t.Helper()
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatalf("vsock socket: %v", err)
	}
	if err := unix.Connect(fd, &unix.SockaddrVM{CID: cid, Port: port}); err != nil {
		unix.Close(fd)
		t.Skipf("no vsock loopback on this kernel (connect cid=%d: %v)", cid, err)
	}
	return os.NewFile(uintptr(fd), "vsock-client")
}

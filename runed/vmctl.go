package runed

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
)

// ctlFrame is the control channel: one JSON object per line, both directions.
// The guest's half is kernel/guest-init/main.go's `frame`, and the two are
// separate declarations because guest-init is PID 1 in an initrd holding one
// binary and takes no dependency it can avoid — its own module, its own
// go.mod. TestCtlFrameMatchesGuestInit is what keeps them one wire format.
type ctlFrame struct {
	Kind  string            `json:"kind"`
	Env   map[string]string `json:"env,omitempty"`
	Stdin []byte            `json:"stdin,omitempty"`
	Batch string            `json:"batch,omitempty"`
	ID    string            `json:"id,omitempty"`
	Line  string            `json:"line,omitempty"`
	Code  int               `json:"code,omitempty"`
	Probe map[string]string `json:"probe,omitempty"`
}

// The frame kinds. The host sends turn, steer and stop; the guest answers with
// ready, log, steer_ack and exactly one exit.
const (
	ctlTurn     = "turn"
	ctlSteer    = "steer"
	ctlStop     = "stop"
	ctlReady    = "ready"
	ctlLog      = "log"
	ctlSteerAck = "steer_ack"
	ctlExit     = "exit"
)

// maxCtlLine bounds one frame. The turn frame carries the whole agent payload,
// which is the context layers plus the message batch, so the bound is the
// payload's, not a line's.
const maxCtlLine = 16 * 1024 * 1024

// encodeCtlFrame renders one frame for the wire.
func encodeCtlFrame(f ctlFrame) ([]byte, error) {
	b, err := json.Marshal(f)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// decodeCtlFrames reads frames until the stream ends, handing each to fn. A line
// that is not a frame ends the read: the guest writes JSON and nothing else on
// this channel, so anything else means the channel is not what it claims.
func decodeCtlFrames(r io.Reader, fn func(ctlFrame) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), maxCtlLine)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var f ctlFrame
		if err := json.Unmarshal(line, &f); err != nil {
			return fmt.Errorf("ctl: undecodable frame: %w", err)
		}
		if err := fn(f); err != nil {
			return err
		}
	}
	return sc.Err()
}

// vsockListen binds one host-side vsock port for every CID, non-blocking so the
// returned file is pollable and accept honours a deadline.
//
// A bind error is returned, never worked around: runed binds before QEMU starts
// (spec 6/41 §"The turn" steps 4-5), so a port a squatter already holds makes
// this fail before any guest exists. Picking another port instead is what would
// turn a denial into a guest talking to whoever answered.
func vsockListen(port uint32) (*os.File, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("vsock socket: %w", err)
	}
	if err := unix.Bind(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY, Port: port}); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("vsock bind port %d: %w", port, err)
	}
	if err := unix.Listen(fd, 8); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("vsock listen port %d: %w", port, err)
	}
	return os.NewFile(uintptr(fd), "vsock:"+strconv.FormatUint(uint64(port), 10)), nil
}

// vsockAccept accepts one connection and returns it with the peer's CID.
//
// The CID is the caller's identity and the host kernel's own: the vhost driver
// drops every packet whose source is not the CID the host assigned that guest
// ("Only accept correctly addressed packets", drivers/vhost/vsock.c), so a guest
// can no more claim another guest's CID than a container can claim another uid.
func vsockAccept(l *os.File, deadline time.Time) (*os.File, uint32, error) {
	if err := l.SetReadDeadline(deadline); err != nil {
		return nil, 0, err
	}
	rc, err := l.SyscallConn()
	if err != nil {
		return nil, 0, err
	}
	var nfd int
	var sa unix.Sockaddr
	var aerr error
	ctrlErr := rc.Read(func(fd uintptr) bool {
		nfd, sa, aerr = unix.Accept4(int(fd), unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK)
		return !errors.Is(aerr, unix.EAGAIN)
	})
	if ctrlErr != nil {
		return nil, 0, ctrlErr
	}
	if aerr != nil {
		return nil, 0, aerr
	}
	vm, ok := sa.(*unix.SockaddrVM)
	if !ok {
		_ = unix.Close(nfd)
		return nil, 0, fmt.Errorf("vsock accept: peer address is %T, not a vsock address", sa)
	}
	return os.NewFile(uintptr(nfd), "vsock-conn"), vm.CID, nil
}

// ctlSession is the host end of one guest's control channel. conn is an
// io.ReadWriteCloser rather than the accepted vsock file, so the frame protocol
// and the steer contract are testable over any pair of pipes.
type ctlSession struct {
	conn io.ReadWriteCloser
	// wmu serializes writes: a steer and a stop can race, and half a frame
	// interleaved with another is a frame the guest cannot parse.
	wmu  sync.Mutex
	amu  sync.Mutex
	acks map[string]chan struct{}
	// exit carries the guest's one exit frame.
	exit chan int
	// done closes when the read loop ends, which is the ONLY other way a steer
	// stops waiting for its ack. A closed ack channel therefore means an ack and
	// nothing else — the alternative, one channel closed for both an ack and a
	// dead guest, cost a lost steer that read as delivered.
	done chan struct{}
	// log receives each {log} line the guest sends: the agent's stderr.
	log  func(string)
	boot *bootLog
	seq  atomic.Int64
	// closed is set once the read loop ends, so a steer after the guest is gone
	// returns false instead of blocking for its ack.
	closed atomic.Bool
}

type bootLog struct {
	start  time.Time
	logger *slog.Logger
	folder string
	handle string
}

func newCtlSession(conn io.ReadWriteCloser, logf func(string), boot *bootLog) *ctlSession {
	return &ctlSession{
		conn: conn,
		acks: map[string]chan struct{}{},
		exit: make(chan int, 1),
		done: make(chan struct{}),
		log:  logf,
		boot: boot,
	}
}

// send writes one frame.
func (s *ctlSession) send(f ctlFrame) error {
	b, err := encodeCtlFrame(f)
	if err != nil {
		return err
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err = s.conn.Write(b)
	return err
}

// serve reads the guest's frames until the channel ends. It returns the read
// error, if any; a clean EOF is nil.
func (s *ctlSession) serve() error {
	err := s.read()
	// Ordered, not deferred: the flag is what stop() and steer() read, and
	// every waiter is released by done. A steer whose ack can no longer arrive
	// must return false now rather than at its grace.
	s.closed.Store(true)
	close(s.done)
	return err
}

func (s *ctlSession) read() error {
	return decodeCtlFrames(s.conn, func(f ctlFrame) error {
		switch f.Kind {
		case ctlLog:
			if s.log != nil {
				s.log(f.Line)
			}
		case ctlSteerAck:
			s.amu.Lock()
			if ch, ok := s.acks[f.ID]; ok {
				close(ch)
				delete(s.acks, f.ID)
			}
			s.amu.Unlock()
		case ctlExit:
			select {
			case s.exit <- f.Code:
			default:
			}
		case ctlReady:
			if s.boot != nil {
				s.boot.logger.Info("guest ready", "folder", s.boot.folder, "handle", s.boot.handle,
					"boot_ms", time.Since(s.boot.start).Milliseconds(), "probe", f.Probe)
				s.boot = nil
			}
		default:
			slog.Warn("ctl: unknown frame kind from guest", "kind", f.Kind)
		}
		return nil
	})
}

// steer delivers one batch into the live guest and reports whether the GUEST
// acknowledged it.
//
// The ack is the whole point. routd marks a turn done the moment a steer is
// accepted (routd/dispatch.go), so a write that only proves the host queued
// bytes would make a user's message vanish while the turn read as delivered:
// the guest tmpfs holding the pending batch is discarded at power-off. No ack is
// a false return, which is the documented fall-through to a fresh spawn, so the
// batch is re-fed rather than lost (spec 6/41 §"Steer and stop").
func (s *ctlSession) steer(batch string, grace time.Duration) bool {
	// The closed check only spares a write to a dead channel and its error log:
	// the GUARANTEE is the done case below, which answers false for a session
	// that ends at any point after this line.
	if batch == "" || s.closed.Load() {
		return false
	}
	id := strconv.FormatInt(s.seq.Add(1), 10)
	ch := make(chan struct{})
	s.amu.Lock()
	s.acks[id] = ch
	s.amu.Unlock()
	defer func() {
		s.amu.Lock()
		delete(s.acks, id)
		s.amu.Unlock()
	}()
	if err := s.send(ctlFrame{Kind: ctlSteer, Batch: batch, ID: id}); err != nil {
		slog.Error("ctl: steer write failed; falling through to a fresh spawn", "err", err)
		return false
	}
	select {
	case <-ch:
		return true
	case <-s.done:
		// The guest went away holding the batch. The tmpfs it was written to is
		// discarded at power-off, so this is a batch that must be re-fed.
		slog.Warn("ctl: guest ended before acking a steer; falling through to a fresh spawn")
		return false
	case <-time.After(grace):
		slog.Warn("ctl: no steer ack within grace; falling through to a fresh spawn",
			"grace", grace)
		return false
	}
}

// stop asks the guest to end the turn: guest-init sends the agent SIGTERM and
// powers off after its own grace. The deadline kill is separate — SIGKILL on the
// QEMU pid — because a guest that ignores this must still end.
func (s *ctlSession) stop() error {
	if s.closed.Load() {
		return nil
	}
	return s.send(ctlFrame{Kind: ctlStop})
}

// mcpBridgeFlag is the argv marker that turns the runed binary into one turn's
// MCP bridge. It is an ARGUMENT rather than an environment variable so the
// child's own /proc/<pid>/cmdline names what it is, which is what the reap
// checks before it signals a recorded pid.
const mcpBridgeFlag = "--mcp-bridge"

// MCPBridgeMain runs the MCP bridge when runed was exec'd as one, and reports
// whether it did. main calls it first, before any daemon setup.
//
//	runed --mcp-bridge <guest-cid> <folder socket path>     # listener on fd 3
//
// It is a child of runed running as the agent's uid, one per turn, and it is the
// turn's credential across the VM boundary. It holds the mcp listener for
// exactly one VM, accepts only a peer whose CID is that VM's, and dials exactly
// one path: that turn's folder socket. routd's check is byte-for-byte the one it
// runs today — the peer is the agent's uid — because this process IS that uid
// (spec 6/41 §"The peer credential").
func MCPBridgeMain() (int, bool) {
	if len(os.Args) < 4 || os.Args[1] != mcpBridgeFlag {
		return 0, false
	}
	cid64, err := strconv.ParseUint(os.Args[2], 10, 32)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcp-bridge: cid %q: %v\n", os.Args[2], err)
		return 2, true
	}
	return runMCPBridge(uint32(cid64), os.Args[3], os.NewFile(3, "mcp-listener")), true
}

// runMCPBridge serves the accept loop. One connection per tool call session;
// each is copied both ways until either end closes.
func runMCPBridge(cid uint32, socket string, l *os.File) int {
	// The listener arrives non-blocking because the parent bound it that way.
	// This process has no poller of its own to register it with, so it clears
	// the flag and accepts the plain way.
	if err := unix.SetNonblock(int(l.Fd()), false); err != nil {
		fmt.Fprintf(os.Stderr, "mcp-bridge: clear nonblock: %v\n", err)
		return 1
	}
	for {
		nfd, sa, err := unix.Accept4(int(l.Fd()), unix.SOCK_CLOEXEC)
		if err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			// The parent closes the listener at teardown, which ends the loop.
			return 0
		}
		peer, ok := sa.(*unix.SockaddrVM)
		if !ok || peer.CID != cid {
			// Another guest on this host dialing this turn's port. The host
			// kernel stamped the CID, so this is a refusal at accept, which is
			// the acceptance test's own case.
			got := uint32(0)
			if ok {
				got = peer.CID
			}
			fmt.Fprintf(os.Stderr, "mcp-bridge: refused peer cid %d, want %d\n", got, cid)
			_ = unix.Close(nfd)
			continue
		}
		go bridgeConn(os.NewFile(uintptr(nfd), "mcp-conn"), socket)
	}
}

// bridgeConn joins one accepted vsock connection to the folder's socket.
func bridgeConn(conn *os.File, socket string) {
	defer func() { _ = conn.Close() }()
	uc, err := net.Dial("unix", socket)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcp-bridge: dial %s: %v\n", socket, err)
		return
	}
	defer func() { _ = uc.Close() }()
	done := make(chan struct{}, 2)
	go func() { io.Copy(uc, conn); done <- struct{}{} }() //nolint:errcheck // a closed half ends the pair
	go func() { io.Copy(conn, uc); done <- struct{}{} }() //nolint:errcheck // same
	<-done
}

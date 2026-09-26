// guest-init is the guest's entire userspace: PID 1 in the initrd arizuko boots
// per turn (spec 6/41 §"The turn" steps 6-9). It mounts what the host exported,
// takes the turn's env and payload over vsock, runs the agent, and powers the
// guest off. Nothing else is in the initrd, so every driver it uses is built
// into the kernel (kernel/arizuko.fragment).
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

// AF_VSOCK and the sockaddr below are hand-rolled on purpose: guest-init is PID 1
// in an initrd that holds one binary, so it takes no dependency it can avoid.
// golang.org/x/sys/unix has SockaddrVM, and pulling it in would add a module and
// a go.sum to a program that needs five syscalls.
const afVSOCK = 40

// sockaddrVM is struct sockaddr_vm (linux/vm_sockets.h), 16 bytes.
type sockaddrVM struct {
	Family   uint16
	Reserved uint16
	Port     uint32
	CID      uint32
	Zero     [4]uint8
}

// hostCID is the other end of both vsock channels: vsock(7) fixes the host at
// CID 2. The ports are not constants — the host allocates a pair per guest and
// renders them onto the kernel command line (bootPorts).
const hostCID = 2

// frame is the ctl protocol, one JSON object per line in both directions.
// The host sends env+stdin once, then steer or stop; the guest answers with
// steer_ack, log lines and one exit.
type frame struct {
	Kind  string            `json:"kind"`
	Env   map[string]string `json:"env,omitempty"`
	Stdin []byte            `json:"stdin,omitempty"`
	Batch string            `json:"batch,omitempty"`
	ID    string            `json:"id,omitempty"`
	Line  string            `json:"line,omitempty"`
	Code  int               `json:"code,omitempty"`
	// Probe carries step 0's measurements. It is the only field the production
	// turn does not use, and it is what `make bench-boot` reads.
	Probe map[string]string `json:"probe,omitempty"`
}

// The frame kinds. The host sends turn, steer and stop; the guest answers with
// ready, log, steer_ack and exactly one exit. Same names as runed/vmctl.go.
const (
	kindTurn     = "turn"
	kindSteer    = "steer"
	kindStop     = "stop"
	kindReady    = "ready"
	kindLog      = "log"
	kindSteerAck = "steer_ack"
	kindExit     = "exit"
)

// maxFrame bounds one frame, and it is runed's maxCtlLine: the turn frame
// carries the whole agent payload, so the bound is the payload's, not a line's.
const maxFrame = 16 * 1024 * 1024

// Where the turn's files arrive and where the agent's root is assembled.
const (
	// shareRoot and shareTurn are the host's two virtio-fs tags
	// (runed/qemuhost.go).
	shareRoot = "root"
	shareTurn = "turn"
	// shareDir is where the turn share is mounted, and the source of every bind:
	// the host stages each family at the path the guest sees it, so a bind's
	// source is the share plus that path and neither side has a second naming
	// scheme to disagree about (runed/vmtree.go treeEntry).
	shareDir = "/turn"
	// manifestPath is what the HOST writes at the share root
	// (runed/vmtree.go treeManifestName). The guest reads that table rather than
	// knowing it, so a new mount family changes the host's renderer and never
	// this file.
	manifestPath = shareDir + "/mounts.json"
	// guestRoot is the root the agent runs in: the release rootfs read-only
	// underneath, a tmpfs on top, and the turn's families bound over both.
	guestRoot = "/newroot"
	// lowerDir and overlayDir hold the overlay's two halves. They stay outside
	// guestRoot so the agent's root holds nothing but the agent's own tree.
	lowerDir   = "/lower"
	overlayDir = "/overlay"
	// upperSize bounds the agent's writable layer. It is RAM inside -m, so the
	// size is explicit: a write past it is ENOSPC in the guest, never a host
	// reclaiming under pressure (spec §"The host budget"). 512m against the 2G
	// RUNED_GUEST_MEM default leaves the agent its RSS and /dev/shm its gigabyte.
	upperSize = "size=512m"
	// tmpfsSize bounds each guest-local tmpfs the manifest names — today the ipc
	// dir, which holds one steered batch at a time — for the same reason.
	tmpfsSize = "size=16m"
)

// What the release image put in the rootfs, and the ids the guest runs as.
const (
	// agentUID and agentGID are the agent's ids in the guest, always: the guest
	// agent is uid 1000 whatever the host uid is, and virtiofsd's single-id
	// namespace maps it onto the folder's owner (spec §"The peer credential").
	agentUID = 1000
	agentGID = 1000
	// agentBin is the release image's ENTRYPOINT (ant/Dockerfile).
	agentBin = "/app/entrypoint.sh"
	// socatBin is where the release image's apt install puts socat
	// (ant/Dockerfile).
	socatBin = "/usr/bin/socat"
	// mcpSocket is the path ant connects to, and it is already three times
	// declared: ant/src/mcp.ts MCP_SOCK, ant/Dockerfile's ARIZUKO_MCP_SOCKET and
	// the settings.json runed renders. The dir holding it is a guest tmpfs
	// because a unix socket does not cross virtio-fs — a bind-mounted socket
	// answers "Operation not supported" (spec §"The mechanisms").
	mcpSocket = "/run/ipc/gated.sock"
	// ipcInput is where a steered batch lands and what the agent drains
	// (ant/src/index.ts IPC_INPUT_DIR).
	ipcInput = "/run/ipc/input"
)

// The three waits this guest holds, each bounded because a guest that waits
// forever is one the host has to SIGKILL.
const (
	// turnWait bounds the wait for the {env,stdin} frame. The host sends it in
	// the same breath as it accepts ctl (runed/qemu.go step 8), so anything near
	// this bound means ctl is not carrying a turn. It is also what keeps the
	// step-0 probe boot — which opens ctl and sends no turn — powering off.
	turnWait = 10 * time.Second
	// socketWait bounds the wait for socat to bind. An agent whose first tool
	// call finds no socket answers nothing, which is why routd binds the folder's
	// socket before a spawn as well.
	socketWait = 5 * time.Second
	// ctlSettle bounds the wait for the host to finish with ctl after the exit
	// frame. It is a DELIVERY wait, not a timeout the turn spends: the host closes
	// the channel as soon as it has the frame, so this elapses only when the host
	// is already gone.
	ctlSettle = 3 * time.Second
	// stopGrace is how long the agent gets after {stop}'s SIGTERM before the
	// guest powers off regardless (spec §"Steer and stop"). It is the 10 s docker
	// used to give a stopping container, which is the grace the agent had before
	// the substrate changed, so its wrap-up window did not move. The host stops waiting
	// for the exit frame after RUNED_REAP_GRACE (5 s default) and SIGKILLs QEMU,
	// so an agent that needs the whole grace ends as a timeout either way —
	// shortening the agent's window to make the frame arrive would trade a
	// user's answer for a tidier status.
	stopGrace = 10 * time.Second
)

// The two exit codes guest-init reports as its own.
const (
	// initExit is sysexits.h EX_OSERR: the guest could not be assembled, so the
	// agent never ran. It is deliberately NOT 78, the agent's own EX_CONFIG and
	// the one code runed reads as terminal (runed/docker.go agentTerminalExit):
	// guest-init must not impersonate the agent's verdict on its own turn.
	initExit = 71
	// sigtermExit is what a shell reports for a process a SIGTERM ended, and
	// what the guest reports when the stop grace expires with the agent still up.
	sigtermExit = 128 + int(syscall.SIGTERM)
)

func main() {
	// guest-init opens its own console rather than trusting the fds it inherits.
	// Measured 2026-09-24: PID 1 in this initramfs gets an fd 1 that write(2)
	// rejects, so every Printf vanished while the guest's mount syscalls still
	// reached the kernel log — the guest looked hung and was only mute. An init
	// that owns its sink cannot be silenced by whatever exec handed it.
	openConsole()
	logf("guest-init: alive")

	// Step 0 reads uptime at entry: the kernel's own boot cost, from the guest
	// side rather than inferred from the host (spec §"The image and the guest
	// kernel", the four timestamps).
	entry := uptime()
	probe := map[string]string{"init_entry_uptime": entry}

	mountBase(probe)
	probe["mounts_done_uptime"] = uptime()

	// The substrate's two vsock ports, from the command line the host rendered.
	var ctlPort, mcpPort uint32
	cmdline, err := os.ReadFile("/proc/cmdline")
	if err == nil {
		ctlPort, mcpPort, err = bootPorts(string(cmdline))
	}
	if err != nil {
		// Without its ports this guest has no host to talk to. The console is
		// the only sink left, so the probe goes there and the guest powers off
		// rather than hanging.
		logf("guest-init: %v", err)
		probe["cmdline_error"] = err.Error()
		report(probe)
		off()
	}
	probe["ctl_port"] = itoa(int(ctlPort))
	probe["mcp_port"] = itoa(int(mcpPort))

	fd, err := dialCtl(ctlPort)
	if err != nil {
		// No control channel means no turn. Say so on the console, which is the
		// only sink left, and power off rather than hang holding a guest.
		logf("guest-init: ctl dial failed: %v", err)
		probe["ctl_error"] = err.Error()
		report(probe)
		off()
	}
	probe["ctl_up_uptime"] = uptime()
	report(probe)

	// The ready frame FIRST, before the root is assembled: it is the last of
	// `make bench-boot`'s four timestamps and the 1.0 s gate is on it
	// (spec §"The image and the guest kernel"), so nothing the turn needs is
	// allowed to sit in front of it.
	c := &ctl{w: fd, conn: fd}
	_ = c.send(frame{Kind: kindReady, Probe: probe})

	// From here every failure goes out on ctl and ends the turn with a nonzero
	// exit frame: a guest that powers off silently is a turn runed reports as
	// having no result at all (runed/qemu.go turnResult).
	if err := assembleRoot(); err != nil {
		c.fail("guest-init: %v", err)
	}
	if err := prepareIPC(); err != nil {
		c.fail("guest-init: %v", err)
	}

	// The frame loop runs for the rest of the turn: it hands main the turn frame
	// and serves steer and stop beside the running agent.
	t := &turn{c: c, input: guestRoot + ipcInput}
	t.wake = t.signalAgent
	first := make(chan frame, 1)
	go t.read(fd, first)

	var turnFrame frame
	select {
	case f, ok := <-first:
		if !ok {
			c.fail("guest-init: ctl closed before the turn frame arrived")
		}
		turnFrame = f
	case <-time.After(turnWait):
		c.fail("guest-init: no turn frame within %s", turnWait)
	}

	// Step 7: the MCP path's guest half, then the agent on top of it.
	if err := startBridge(c, mcpPort); err != nil {
		c.fail("guest-init: %v", err)
	}
	run(t, agentCmd(turnFrame.Env, turnFrame.Stdin))
	// The exit frame is the turn's only result, so the guest waits for the host to
	// have it before the VM is destroyed (BUGS `L375`).
	c.settle(ctlSettle)
	off()
}

// mountBase mounts what the guest needs before the turn: proc and sys for the
// runtime, devtmpfs for the console, and a sized tmpfs for the overlay upper.
// The upper is RAM inside -m, so its size is explicit: a write past it is
// ENOSPC in the guest, never a host reclaiming under pressure (spec §"The host
// budget").
func mountBase(probe map[string]string) {
	// /dev is NOT in this list, and the kernel does not populate it either:
	// CONFIG_DEVTMPFS_MOUNT has no effect on an initramfs boot, so /dev is exactly
	// the set of real nodes kernel/Dockerfile puts in the initrd (BUGS `L374`).
	// Mounting devtmpfs over it here is not the fix — that cost a whole debug cycle
	// on 2026-09-24, when re-mounting /dev invalidated the console fd the kernel had
	// already handed PID 1 as stdout and the guest powered off in silence.
	for _, m := range []struct{ src, dst, fs, opts string }{
		{"proc", "/proc", "proc", ""},
		{"sysfs", "/sys", "sysfs", ""},
		{"tmpfs", "/run", "tmpfs", "size=64m"},
		{"tmpfs", "/dev/shm", "tmpfs", "size=1g"},
	} {
		_ = os.MkdirAll(m.dst, 0o755)
		if err := syscall.Mount(m.src, m.dst, m.fs, 0, m.opts); err != nil {
			probe["mount_"+m.fs+"_error"] = err.Error()
		}
	}
	// virtio-fs is the one share the staging tree arrives on. Its absence is the
	// measurement step 0 wants, not a reason to stop.
	_ = os.MkdirAll(shareDir, 0o755)
	if err := syscall.Mount(shareTurn, shareDir, "virtiofs", 0, ""); err != nil {
		probe["virtiofs_error"] = err.Error()
	} else {
		probe["virtiofs"] = "mounted"
		// The clamp measured from inside the guest, BOTH ways, because both answers
		// are the property (spec §"The peer credential"): a write as the agent's uid
		// lands on the host as the folder's owner, and a write as uid 0 is REFUSED,
		// because the share's daemon is unprivileged and cannot own a file for
		// anyone else. A "root_write":"ok" here would be the hole this exists to
		// close.
		probe["guest_uid"] = itoa(syscall.Getuid())
		if err := os.WriteFile("/turn/from-guest-root.txt", []byte("as-uid-0\n"), 0o644); err == nil {
			probe["root_write"] = "ok — THE CLAMP IS OFF"
		} else {
			probe["root_write"] = "refused"
		}
		// Under the group's own directory, not at the share ROOT: the staging tree's
		// root is runed's and root-owned, so nobody but root may create a file
		// there — and root is exactly who the clamp refuses. The agent's writes all
		// land under its home, which the folder owns.
		if err := asAgent(func() error {
			return os.WriteFile(shareDir+"/home/node/.arizuko-probe", []byte("as-the-agent\n"), 0o644)
		}); err != nil {
			probe["turn_write_error"] = err.Error()
		} else {
			probe["turn_write"] = "ok"
		}
		if b, err := os.ReadFile("/turn/probe.txt"); err == nil {
			probe["turn_read"] = string(b[:len(b)-1])
		} else {
			probe["turn_read_error"] = err.Error()
		}
	}
}

// bootPorts reads the substrate's two vsock ports off the kernel command line.
//
// The command line, and not a ctl frame: ctl's own port is one of the two, and a
// frame cannot carry the address of the channel it arrives on — one reader for
// both keeps the substrate's addressing in one place, where the host renders it
// (runed/qemu.go qemuArgs -append). /proc/cmdline, and not init's environment,
// although the kernel puts an unrecognised KEY=VALUE there too: that path
// depends on init/main.c's unknown_bootoption keeping the key, which silently
// discards one containing a dot, while /proc/cmdline is the host's own string
// verbatim.
//
// A missing key is an error rather than a default. The host allocates a port
// pair per CID (runed/vmrecord.go vsockPorts), so a guest that guessed would
// dial another guest's control channel and be refused at the host's CID check.
func bootPorts(cmdline string) (ctlPort, mcpPort uint32, err error) {
	read := func(key string) (uint32, error) {
		for _, f := range strings.Fields(cmdline) {
			v, ok := strings.CutPrefix(f, key+"=")
			if !ok {
				continue
			}
			n, err := strconv.ParseUint(v, 10, 32)
			if err != nil {
				return 0, fmt.Errorf("kernel command line has %s=%q, which is not a port: %w", key, v, err)
			}
			return uint32(n), nil
		}
		return 0, fmt.Errorf("kernel command line carries no %s=<port>", key)
	}
	if ctlPort, err = read("ARIZUKO_CTL"); err != nil {
		return 0, 0, err
	}
	if mcpPort, err = read("ARIZUKO_MCP"); err != nil {
		return 0, 0, err
	}
	return ctlPort, mcpPort, nil
}

// treeEntry is one family the host staged. Path is the guest path and it is also
// the path inside the share, because the tree mirrors the guest's own layout.
type treeEntry struct {
	Path string `json:"path"`
	// RO is read but never applied: the host remounts each read-only bind before
	// virtiofsd starts, so every write the daemon attempts is EROFS already
	// (runed/vmtree.go bindAll). A guest-side flag would be a second enforcement
	// of one decision, and the weaker of the two.
	RO bool `json:"ro"`
}

// treeManifest is what the host declares and this guest reads. Its writer is
// runed/vmtree.go's treeManifest; the two are separate declarations for the same
// reason the frame is — guest-init is its own module and takes no dependency it
// can avoid.
type treeManifest struct {
	Mounts []treeEntry `json:"mounts"`
	// Tmpfs are guest-local paths that are NOT in the share, because a unix
	// socket does not cross virtio-fs (spec §"The mechanisms").
	Tmpfs []string `json:"tmpfs"`
}

// mountOp is one mount that assembles the agent's root.
type mountOp struct {
	Source string
	Target string
	// FS is "" for a bind.
	FS   string
	Data string
	// Needs are directories the mount's own options name, created before it:
	// overlayfs refuses an upperdir or workdir that does not exist, and both must
	// be on the same filesystem (Documentation/filesystems/overlayfs.rst).
	Needs []string
	Flags uintptr
	// Own hands the target to the agent. A guest tmpfs is mounted by root and
	// empty, where the release image had it owned by the agent
	// (ant/Dockerfile chown node:node /run/ipc).
	Own bool
}

// rootPlan is every mount that assembles the agent's root, in the order it has
// to be made. Pure: the guest's whole mount table comes out of the host's
// manifest and this function, so a test names every source, target and option
// without a hypervisor.
func rootPlan(m treeManifest) []mountOp {
	ops := []mountOp{
		// The release rootfs, read-only, as the overlay's LOWER. A virtio-fs
		// share works as an overlayfs lower and NOT as an upper
		// (kata-containers how-to-run-docker-with-kata.md), which is why the
		// writable layer below is a tmpfs and why the agent's every write dies
		// with the guest — Persistence stays mounted_home (spec §"The files").
		{Source: shareRoot, Target: lowerDir, FS: "virtiofs", Flags: syscall.MS_RDONLY},
		{Source: "tmpfs", Target: overlayDir, FS: "tmpfs", Data: upperSize},
		{Source: "overlay", Target: guestRoot, FS: "overlay",
			Data:  "lowerdir=" + lowerDir + ",upperdir=" + overlayDir + "/upper,workdir=" + overlayDir + "/work",
			Needs: []string{overlayDir + "/upper", overlayDir + "/work"}},
		// The kernel interfaces the agent's own runtime reads. They are not in
		// the manifest because they are not the host's to stage: node resolves
		// /proc/self, and the initrd's /dev is re-bound rather than re-mounted
		// so the sized /dev/shm mountBase already made comes with it and PID 1's
		// console fd is left alone.
		{Source: "proc", Target: guestRoot + "/proc", FS: "proc"},
		{Source: "sysfs", Target: guestRoot + "/sys", FS: "sysfs"},
		{Source: "/dev", Target: guestRoot + "/dev", Flags: syscall.MS_BIND | syscall.MS_REC},
		// devpts, because a pty is allocated by opening /dev/ptmx and then the
		// numbered slave under /dev/pts, and without the mount that second open
		// is ENOENT — so any turn tool that wants a terminal fails.
		//
		// MEASURED 2026-09-24, in the built artifact rather than assumed from the
		// fragment: kernel.config carries CONFIG_UNIX98_PTYS=y and CONFIG_TTY=y,
		// inherited from Firecracker's base config. The fragment does NOT name
		// either, so this mount rests on an inherited value — spec §"The image and
		// the guest kernel" records that, and it is pinned at the next rebuild
		// rather than forcing one for a value already set.
		{Source: "devpts", Target: guestRoot + "/dev/pts", FS: "devpts",
			Data: "gid=5,mode=620,ptmxmode=666", Needs: []string{guestRoot + "/dev/pts"}},
	}
	// The families, in the order the host sorted them: a parent before its
	// children, which is the layering the mount renderer already relies on.
	for _, e := range m.Mounts {
		ops = append(ops, mountOp{
			Source: shareDir + e.Path,
			Target: guestRoot + e.Path,
			Flags:  syscall.MS_BIND | syscall.MS_REC,
		})
	}
	for _, p := range m.Tmpfs {
		ops = append(ops, mountOp{
			Source: "tmpfs", Target: guestRoot + p, FS: "tmpfs", Data: tmpfsSize, Own: true,
		})
	}
	return ops
}

// assembleRoot builds the filesystem the agent sees (spec §"The turn" step 6).
func assembleRoot() error {
	b, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read the host's mount manifest: %w", err)
	}
	var m treeManifest
	if err := json.Unmarshal(b, &m); err != nil {
		return fmt.Errorf("decode %s: %w", manifestPath, err)
	}
	// A manifest with no families is a guest whose agent has no home, no release
	// and no share. Refuse it here rather than exec into an empty root, which
	// would read as an agent that answered nothing.
	if len(m.Mounts) == 0 {
		return fmt.Errorf("%s declares no mounts", manifestPath)
	}
	return applyPlan(rootPlan(m))
}

// applyPlan makes the plan's mounts.
func applyPlan(ops []mountOp) error {
	for _, op := range ops {
		for _, d := range op.Needs {
			if err := os.MkdirAll(d, 0o755); err != nil {
				return fmt.Errorf("mount point %s: %w", d, err)
			}
		}
		if err := ensureTarget(op); err != nil {
			return err
		}
		if err := syscall.Mount(op.Source, op.Target, op.FS, op.Flags, op.Data); err != nil {
			return fmt.Errorf("mount %s %s -> %s: %w", op.FS, op.Source, op.Target, err)
		}
		if op.Own {
			if err := os.Chown(op.Target, agentUID, agentGID); err != nil {
				return fmt.Errorf("chown %s to the agent: %w", op.Target, err)
			}
		}
	}
	return nil
}

// asAgent runs fn with the FILESYSTEM identity of the agent's uid, keeping every
// capability the caller holds (setfsuid touches file access checks and nothing
// else). It is per-THREAD, so the thread is locked for the duration.
//
// The turn's share is exported by a virtiofsd running as the agent's own uid —
// that daemon IS the clamp, and it cannot satisfy a request that carries any other
// uid (spec §"The peer credential"). guest-init is uid 0, so every file it creates
// on the share is refused with EACCES until it asks as the agent. Measured on krons
// 2026-09-25: `open /newroot/home/node/.codex/auth.json: permission denied` on a
// bind target, and the guest's own probe write refused the same way (BUGS `L373`).
func asAgent(fn func() error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := syscall.Setfsgid(agentGID); err != nil {
		return fmt.Errorf("setfsgid %d: %w", agentGID, err)
	}
	if err := syscall.Setfsuid(agentUID); err != nil {
		_ = syscall.Setfsgid(0)
		return fmt.Errorf("setfsuid %d: %w", agentUID, err)
	}
	defer func() {
		_ = syscall.Setfsuid(0)
		_ = syscall.Setfsgid(0)
	}()
	return fn()
}

// createOnShare makes a mount point, as root first and as the agent when root is
// refused. Root is right for the targets on the guest's own overlay, which is
// root-owned; the agent is the only identity the share accepts. One helper rather
// than a path test, because which targets land on the share is the renderer's
// decision and not a prefix this program can read off a path.
func createOnShare(what string, create func() error) error {
	err := create()
	if err == nil || !errors.Is(err, os.ErrPermission) {
		return err
	}
	if agentErr := asAgent(create); agentErr != nil {
		return fmt.Errorf("%s: refused as root (%v) and as the agent (%w)", what, err, agentErr)
	}
	return nil
}

// ensureTarget creates the mount point. A bind target must match its source's
// kind: the two codex credentials are single FILES overmounted read-only
// (runed/vmtree.go stageTargets), and a directory target for a file source is
// ENOTDIR at mount time.
func ensureTarget(op mountOp) error {
	// A mount point that is ALREADY THERE needs no write, and on the share that is
	// the difference between working and not: the share's daemon is unprivileged,
	// so a create it must perform for another uid is refused, while an existing
	// file is just a path to mount over. The codex credentials are exactly this
	// case — `groups/<folder>/.codex/auth.json` is seeded on the host and the guest
	// only overmounts it (BUGS `L373`).
	if _, err := os.Stat(op.Target); err == nil {
		return nil
	}
	if op.FS == "" {
		if fi, err := os.Stat(op.Source); err == nil && !fi.IsDir() {
			dir := filepath.Dir(op.Target)
			if err := createOnShare("mount point "+dir, func() error {
				return os.MkdirAll(dir, 0o755)
			}); err != nil {
				return fmt.Errorf("mount point %s: %w", dir, err)
			}
			return createOnShare("mount point "+op.Target, func() error {
				f, err := os.OpenFile(op.Target, os.O_CREATE|os.O_WRONLY, 0o644)
				if err != nil {
					return err
				}
				return f.Close()
			})
		}
	}
	if err := createOnShare("mount point "+op.Target, func() error {
		return os.MkdirAll(op.Target, 0o755)
	}); err != nil {
		return fmt.Errorf("mount point %s: %w", op.Target, err)
	}
	return nil
}

// prepareIPC makes the agent's input dir, which is where a steered batch lands
// and where the agent unlinks it from — so the agent needs write on the DIR, not
// on the file (ant/src/index.ts drainIpcInput).
func prepareIPC() error {
	dir := guestRoot + ipcInput
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("the agent's input dir: %w", err)
	}
	if err := os.Chown(dir, agentUID, agentGID); err != nil {
		return fmt.Errorf("chown the agent's input dir: %w", err)
	}
	return nil
}

// ctl is the guest's end of the control channel.
//
// The mutex serializes writes: the agent's stderr streams while a steer ack or
// the exit frame is sent, and half a frame interleaved with another is a frame
// the host cannot parse (runed's ctlSession holds one for the same reason). The
// Once is the turn's ONE exit frame — a stop whose grace expires and an agent
// that ends at the same moment must not put two on the wire.
type ctl struct {
	w    io.Writer
	mu   sync.Mutex
	once sync.Once
	// conn is the same connection as w when ctl runs over a real socket, and nil
	// in a test that drives the protocol over buffers. settle needs to READ it.
	conn *os.File
}

// settle waits for the host to finish with the control channel, and it is what
// makes the exit frame arrive. Power-off destroys the VM, so a frame still in the
// socket's buffer is lost: the host's read answered "connection reset by peer" one
// second before QEMU exited, `await` fell back to QEMU's own status of 0, and the
// turn log then recorded exit 0 with no stderr while the agent had exited 1 (BUGS
// `L375`).
//
// The host closes ctl once it has the frame, so a read that ends is the delivery
// receipt. Bounded, because a host that never closes must not hold a guest.
func (c *ctl) settle(d time.Duration) {
	if c.conn == nil {
		return
	}
	_ = c.conn.SetReadDeadline(time.Now().Add(d))
	var b [1]byte
	for {
		if _, err := c.conn.Read(b[:]); err != nil {
			return // EOF, reset or the deadline: the host is done either way
		}
	}
}

func (c *ctl) send(f frame) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return writeFrame(c.w, f)
}

// logf says one line on both of the guest's sinks: the console, which survives
// a dead ctl, and a {log} frame, which is what reaches the turn log.
func (c *ctl) logf(format string, a ...any) {
	line := fmt.Sprintf(format, a...)
	logf("%s", line)
	if err := c.send(frame{Kind: kindLog, Line: line}); err != nil {
		logf("guest-init: log frame failed: %v", err)
	}
}

// report sends the turn's one exit frame. RunResult.ExitCode IS this frame
// (spec §"The turn"), so a turn without one has no result at all.
func (c *ctl) report(code int) {
	c.once.Do(func() {
		if err := c.send(frame{Kind: kindExit, Code: code}); err != nil {
			logf("guest-init: exit frame failed: %v", err)
		}
	})
}

// fail reports a turn the guest could not run and powers off. It does not
// return: off() never does.
func (c *ctl) fail(format string, a ...any) {
	c.logf(format, a...)
	c.report(initExit)
	c.settle(ctlSettle)
	off()
}

// turn is the running turn: what a ctl frame acts on while the agent runs.
type turn struct {
	c *ctl
	// input is the agent's input dir as PID 1 sees it — outside the agent's
	// chroot, so it carries the root prefix.
	input string
	// pid is the agent's, 0 until it starts.
	pid atomic.Int32
	// wake signals the agent. It is a field because it is the one step of the
	// steer path that needs a live process, and the ack contract is what a test
	// must be able to drive (runed's dockerRuntime keeps `kill` the same way).
	wake func(syscall.Signal) error
	// steers names each batch file. The guest's own counter, never the frame's
	// id: the id is what the ack must carry, and a name taken off the wire is a
	// path the host chooses.
	steers atomic.Int64
	stops  sync.Once
}

// read serves the host's frames for the rest of the turn, handing main the one
// turn frame. It mirrors runed's read loop: the guest writes JSON and nothing
// else on this channel, so anything else ends the read.
func (t *turn) read(r io.Reader, first chan<- frame) {
	defer close(first)
	err := readFrames(r, func(f frame) error {
		switch f.Kind {
		case kindTurn:
			select {
			case first <- f:
			default:
				logf("guest-init: a second turn frame on a live turn, ignored")
			}
		case kindSteer:
			t.steer(f)
		case kindStop:
			t.stop()
		default:
			logf("guest-init: unknown frame kind %q from the host", f.Kind)
		}
		return nil
	})
	if err != nil {
		logf("guest-init: ctl read ended: %v", err)
	}
}

// steer delivers one batch into the live agent and acks it.
//
// The ack is the whole point, and it comes LAST. The host's steer returns true
// only on it and routd marks the turn done the moment a steer is accepted, so an
// ack that only proved the frame arrived would make a user's message vanish with
// the guest tmpfs at power-off. No ack is the documented fall-through to a fresh
// spawn, which re-feeds the batch (spec §"Steer and stop").
func (t *turn) steer(f frame) {
	if f.Batch == "" {
		t.c.logf("guest-init: an empty steer batch, not acked")
		return
	}
	if err := writeInput(t.input, f.Batch, t.steers.Add(1)); err != nil {
		t.c.logf("guest-init: steer write failed, not acked: %v", err)
		return
	}
	if err := t.wake(syscall.SIGUSR1); err != nil {
		t.c.logf("guest-init: steer signal failed, not acked: %v", err)
		return
	}
	if err := t.c.send(frame{Kind: kindSteerAck, ID: f.ID}); err != nil {
		t.c.logf("guest-init: steer ack failed: %v", err)
	}
}

// stop is the host ending the turn: the agent gets SIGTERM, and the guest powers
// off when the grace expires whether or not it went (spec §"Steer and stop").
// Once, so a second {stop} arms no second timer.
func (t *turn) stop() {
	t.stops.Do(func() {
		t.c.logf("guest-init: stop frame; SIGTERM to the agent")
		if err := t.wake(syscall.SIGTERM); err != nil {
			t.c.logf("guest-init: stop signal failed: %v", err)
		}
		time.AfterFunc(stopGrace, func() {
			t.c.logf("guest-init: the agent is still up %s after SIGTERM; powering off", stopGrace)
			t.c.report(sigtermExit)
			off()
		})
	})
}

// signalAgent is wake's production half.
func (t *turn) signalAgent(sig syscall.Signal) error {
	pid := int(t.pid.Load())
	if pid == 0 {
		return fmt.Errorf("the agent is not running")
	}
	return syscall.Kill(pid, sig)
}

// writeInput drops one {type:"message",text} file into the agent's input dir via
// temp+rename, so the agent's drainIpcInput never reads a partial file. Byte for
// byte the Docker substrate's writeIPCInput (runed/docker.go): the same payload,
// the same .json suffix the agent filters on, and a temp name that does not
// match it.
func writeInput(dir, batch string, seq int64) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]string{"type": "message", "text": batch})
	if err != nil {
		return err
	}
	name := strconv.FormatInt(time.Now().UnixMilli(), 10) + "-" + strconv.FormatInt(seq, 10) + ".json"
	fp := filepath.Join(dir, name)
	if err := os.WriteFile(fp+".tmp", payload, 0o644); err != nil {
		return err
	}
	if err := os.Rename(fp+".tmp", fp); err != nil {
		_ = os.Remove(fp + ".tmp")
		return err
	}
	return nil
}

// startBridge starts the guest's half of the MCP path: one socat serving the
// socket ant already expects and forwarding each connection over vsock to
// runed's bridge on the host (spec §"The turn" steps 7-8). It runs as the
// agent's uid, so the socket it creates is one the agent can open.
func startBridge(c *ctl, mcp uint32) error {
	cmd := exec.Command(socatBin,
		"UNIX-LISTEN:"+mcpSocket+",fork",
		"VSOCK-CONNECT:"+itoa(hostCID)+":"+strconv.FormatUint(uint64(mcp), 10))
	cmd.Env = []string{}
	cmd.SysProcAttr = inRoot()
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("mcp bridge stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", socatBin, err)
	}
	// socat's own errors are the turn's: a refused VSOCK-CONNECT is every tool
	// call failing, so they go on the wire beside the agent's.
	stream(c, stderr)
	deadline := time.Now().Add(socketWait)
	for {
		if _, err := os.Stat(guestRoot + mcpSocket); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("socat did not bind %s within %s", mcpSocket, socketWait)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// agentCmd renders the agent's process: the release image's entrypoint, inside
// the assembled root, as uid 1000, with the turn's environment and its payload
// on stdin (spec §"The turn" step 7).
func agentCmd(env map[string]string, stdin []byte) *exec.Cmd {
	cmd := exec.Command(agentBin)
	// Non-nil even when it is empty, because a nil Env hands the child THIS
	// process's environment — which holds the kernel command line's own keys.
	cmd.Env = envList(env)
	cmd.Stdin = bytes.NewReader(stdin)
	// Discarded, exactly as the Docker substrate discards it
	// (runed/docker.go `cmd.Stdout = io.Discard`): the agent's answer goes out of
	// band over the MCP socket, so nothing reads its stdout.
	cmd.Stdout = nil
	// Not the agent's home: ant passes its own cwd to the harness
	// (ant/src/claude.ts `cwd: HOME`) and the entrypoint cd's itself, so naming
	// a home here would be a second declaration of it for no reader.
	cmd.Dir = "/"
	cmd.SysProcAttr = inRoot()
	return cmd
}

// inRoot is how both children enter the assembled root.
//
// chroot rather than a switch_root, and rather than no root change at all: every
// binary in the release rootfs resolves its ELF interpreter and its libraries by
// absolute path, so a rootfs binary can only run with that tree as /. PID 1
// stays outside it, which keeps the share, the manifest and the agent's input
// dir reachable while the agent runs. The chroot is not the boundary — the VM is
// — and the child is uid 1000, which holds no CAP_SYS_CHROOT to leave it with.
func inRoot() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Chroot:     guestRoot,
		Credential: &syscall.Credential{Uid: agentUID, Gid: agentGID},
	}
}

// envList renders the turn's environment. Sorted, so two turns with the same
// input hand the agent the same environment.
func envList(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	slices.Sort(out)
	return out
}

// run starts the agent, streams its stderr as {log} frames and reports the turn
// with one {exit} frame carrying the agent's own code (spec §"The turn" steps
// 8-9). It does not power the guest off — main does, and that is what lets a
// test drive a real child through this whole path.
func run(t *turn, cmd *exec.Cmd) {
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.c.logf("guest-init: agent stderr: %v", err)
		t.c.report(initExit)
		return
	}
	if err := cmd.Start(); err != nil {
		t.c.logf("guest-init: exec %s: %v", cmd.Path, err)
		t.c.report(initExit)
		return
	}
	t.pid.Store(int32(cmd.Process.Pid))
	// Drained to EOF BEFORE Wait: Wait closes the pipe when it sees the process
	// exit, so a Wait that ran first would cut the agent's last lines — and every
	// log frame has to precede the exit frame on one ordered stream.
	<-stream(t.c, stderr)
	if err := cmd.Wait(); err != nil {
		// On ctl as well as the console: the host's turn log is where an operator
		// looks first, and this line is the only one that names WHY a turn had no
		// reply (BUGS `L375`).
		t.c.logf("guest-init: agent wait: %v", err)
	}
	t.c.report(exitCode(cmd.ProcessState))
}

// stream copies one child's stderr onto ctl as {log} frames and closes its
// channel at EOF, so a caller can order the exit frame after the last line.
func stream(c *ctl, r io.Reader) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64*1024), maxFrame)
		for sc.Scan() {
			if err := c.send(frame{Kind: kindLog, Line: sc.Text()}); err != nil {
				logf("guest-init: log frame failed: %v", err)
				return
			}
		}
	}()
	return done
}

// exitCode is the agent's code the way a shell and docker both report one: its
// exit status, or 128+signal when a signal ended it, so the {exit} frame carries
// one number whatever ended the agent (os.ProcessState.ExitCode answers -1 for a
// signalled process, and -1 would read as a code the agent chose).
func exitCode(ps *os.ProcessState) int {
	if ps == nil {
		return initExit
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return ps.ExitCode()
}

// console is the sink every message goes to: the turn log's guest half, and the
// only sink at all when ctl is not up.
var console int = -1

func openConsole() {
	// /dev/kmsg, not a tty. Measured 2026-09-24: /dev/ttyS0 and /dev/console both
	// OPEN and then fail every write with EIO, because a 16550A with no carrier
	// rejects writes unless CLOCAL is set, and `-serial file:` never asserts the
	// modem lines. Setting termios from PID 1 would work; /dev/kmsg needs no setup,
	// cannot be silenced by carrier state, and lands in the SAME stream the kernel
	// console already writes to — which is the turn log (spec §"The turn" step 9).
	// Ordering with kernel messages comes free.
	fd, err := syscall.Open("/dev/kmsg", syscall.O_WRONLY, 0)
	if err != nil {
		kmsg("open /dev/kmsg: " + err.Error())
		return
	}
	console = fd
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// kmsg reports through the kernel log, which is the one sink proven to reach the
// host before the console is known good: a failed virtiofs mount logs its source
// name, so the name carries the message. It exists to debug the console itself and
// is the only channel that works when the console does not.
func kmsg(msg string) {
	// The target must EXIST or the mount fails at path resolution and never
	// reaches the driver that logs the source name. /dev is in the initrd; /proc
	// is not, and pointing at it cost a whole cycle of silence.
	_ = syscall.Mount("ARIZUKO:"+msg, "/dev", "virtiofs", 0, "")
}

// logf writes one line to the console. It takes the raw fd rather than os.Stdout
// because stdout is the thing that was broken.
func logf(format string, a ...any) {
	if console < 0 {
		return
	}
	_, _ = syscall.Write(console, []byte(fmt.Sprintf(format, a...)+"\n"))
}

func dialCtl(port uint32) (*os.File, error) {
	fd, err := syscall.Socket(afVSOCK, syscall.SOCK_STREAM, 0)
	if err != nil {
		return nil, fmt.Errorf("vsock socket: %w", err)
	}
	sa := sockaddrVM{Family: afVSOCK, Port: port, CID: hostCID}
	// The host binds before it starts QEMU, so a refusal here is the host not
	// being ready yet rather than a missing listener. Retry briefly, then give up
	// loudly (spec §"The peer credential": a bind error fails the turn).
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, _, errno := syscall.Syscall(syscall.SYS_CONNECT, uintptr(fd),
			uintptr(unsafe.Pointer(&sa)), unsafe.Sizeof(sa))
		if errno == 0 {
			// An os.File, not the raw fd: the frame reader and writer are an
			// io.Reader and an io.Writer, which is what makes the protocol
			// testable over a pair of buffers.
			return os.NewFile(uintptr(fd), "ctl"), nil
		}
		err = errno
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("vsock connect cid=%d port=%d: %w", hostCID, port, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func writeFrame(w io.Writer, f frame) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

// readFrames decodes one JSON object per line and hands each to fn until the
// stream ends. A line that is not a frame ends the read: the host writes JSON and
// nothing else on this channel, so anything else means the channel is not what it
// claims (runed's decodeCtlFrames, the other half of this protocol).
func readFrames(r io.Reader, fn func(frame) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), maxFrame)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var f frame
		if err := json.Unmarshal(line, &f); err != nil {
			return fmt.Errorf("ctl: undecodable frame: %w", err)
		}
		if err := fn(f); err != nil {
			return err
		}
	}
	return sc.Err()
}

// report prints the probe map on the console so a run with no ctl still yields
// its measurements. The console is the turn log's other half (spec §"The turn").
func report(p map[string]string) {
	b, _ := json.Marshal(p)
	logf("ARIZUKO_PROBE %s", b)
}

func uptime() string {
	var si syscall.Sysinfo_t
	if err := syscall.Sysinfo(&si); err != nil {
		return "unknown"
	}
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return fmt.Sprintf("%ds", si.Uptime)
	}
	return string(b[:len(b)-1])
}

// off powers the guest down. The host arms -no-reboot and the command line
// carries reboot=t, so the triple fault microvm uses becomes an exit rather than
// a reset (spec §"The turn" step 9).
func off() {
	_ = os.Stdout.Sync()
	_ = syscall.Reboot(syscall.LINUX_REBOOT_CMD_POWER_OFF)
	for {
		time.Sleep(time.Second)
	}
}

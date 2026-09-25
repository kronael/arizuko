package runed

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/obs"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/substrate"
)

// qemuRuntime is the KVM substrate: one guest per turn, driven by
// qemu-system-x86_64, with the turn's files on one virtio-fs share, its control
// channel and its tool calls on vsock, and its only route out through the proxy
// on the host side of its tap (spec 6/41).
type qemuRuntime struct {
	cfg     *core.Config
	folders *groupfolder.Resolver
	paths   qemuPaths
	procs   procs
	// reapPoll is how often a pid is re-checked while it is being ended.
	reapPoll time.Duration
	// alloc serializes the CID and port allocation. The set of live guests is
	// read from disk, so two spawns that read it before either saved would pick
	// the same CID and the second QEMU would fail on it. One runed owns an
	// instance, so a mutex is the whole coordination this needs.
	alloc sync.Mutex
}

// NewQemuRuntime checks every precondition this substrate needs and refuses,
// naming what is missing, rather than starting an instance that cannot run a
// turn safely.
//
// The check is at runed START and not per turn: a missing input found per turn is
// a fleet that looks healthy and answers nothing. The uid clamp is
// in the list because without it every folder gets a host-root write it keeps
// after the guest is gone, so an instance that cannot clamp must not run turns.
func NewQemuRuntime(cfg *core.Config, folders *groupfolder.Resolver) (Runtime, substrate.Effective, error) {
	paths, err := qemuPreconditions(cfg)
	if err != nil {
		return nil, substrate.Effective{}, err
	}
	// Delivered equals Declared or runed does not start: there is no half
	// configuration to report here. A missing proxy binary or an unclampable
	// virtiofsd is a refusal above, never a spawn that quietly gets less than
	// the declaration says (spec 6/25 §3).
	eff := substrate.Effective{Declared: qemuProfile, Delivered: qemuProfile}
	q := &qemuRuntime{
		cfg: cfg, folders: folders, paths: paths,
		procs: hostProcs{}, reapPoll: 50 * time.Millisecond,
	}
	// A previous runed's guests, reaped from the disk. This is the orphan sweep
	// the Docker substrate does with `docker ps --filter=name=arizuko-`: here the
	// record is the label, so the scan is over the record root.
	q.sweep()
	return q, eff, nil
}

// layerImageEnv puts the turn's rendered environment over the agent image's own.
// The image is the BASE and the rendered map WINS, which is the order docker
// applies under the other substrate: `ENV` first, then `-e`.
//
// It lives here and not in `turnEnv` (`render.go`) because that renderer is
// substrate-agnostic and this layer is this substrate's alone: docker applies an
// image's config by itself, and a rootfs TREE carries none. PATH and HOME are the
// image's (`env_contract_test.go` names it as the owner), so without this the
// agent runs on whatever PATH the shell compiled in.
//
// The result is a fresh map. The renderer's map is the Manager's, and a substrate
// that wrote into it would hand the next reader its own proxy address.
func layerImageEnv(image, rendered map[string]string) map[string]string {
	env := make(map[string]string, len(image)+len(rendered))
	maps.Copy(env, image)
	maps.Copy(env, rendered)
	return env
}

// qemuProfile is what this substrate declares. Egress is domain-granular because
// the proxy matches names at CONNECT, and the guest has no resolver at all: a
// name is meaningful only in a CONNECT line, so nothing leaves the tap except to
// the proxy (spec 6/41 §"Egress by name, sealed below the kernel").
var qemuProfile = substrate.Profile{
	Backend:     substrate.KVM,
	Persistence: substrate.MountedHome,
	Sessions:    false,
	Egress:      substrate.EgressDomain,
}

// qemuSpec is one guest's whole argument list, as data. The renderer below is
// pure, so one turn always renders one argument list and a test can name every
// value it expects.
type qemuSpec struct {
	Kernel   string
	Initrd   string
	RootSock string
	TurnSock string
	Mem      string
	SMP      int
	CID      uint32
	Tap      string
	MAC      string
	PidFile  string
	Console  string
	CtlPort  uint32
	McpPort  uint32
}

// qemuArgs renders the machine.
//
// q35, not microvm, and that is measured rather than preferred: on QEMU 7.2.22
// microvm registers every virtio-mmio slot the kernel command line names and
// binds no device, so a vsock connect stays ENODEV and ACPI publishes a 365-byte
// DSDT declaring nothing. q35 with virtio-vsock-pci and vhost-user-fs-pci works,
// and it exits cleanly on a guest power-off where microvm prints "reboot: System
// halted" and QEMU stays alive (spec 6/41 §"The engine").
//
// The memfd backend AND the -numa line are both required for virtio-fs: the
// share needs the guest's memory shared, and omitting the numa node leaves the
// backend unused, which breaks the share silently.
//
// The two ports travel on the command line, UNDOTTED, because the kernel adds an
// unrecognised KEY=VALUE to init's environment and silently DISCARDS one whose key
// contains a dot as an unused module parameter (init/main.c unknown_bootoption,
// v6.1). They are the substrate's own addressing, not the turn's: the turn's env
// and payload arrive on ctl, never on a command line or a file.
func qemuArgs(s qemuSpec) []string {
	return []string{
		"-M", "q35",
		"-enable-kvm",
		"-cpu", "host",
		"-smp", strconv.Itoa(s.SMP),
		"-m", s.Mem,
		"-object", "memory-backend-memfd,id=mem,size=" + s.Mem + ",share=on",
		"-numa", "node,memdev=mem",
		"-kernel", s.Kernel,
		"-initrd", s.Initrd,
		"-append", fmt.Sprintf("console=ttyS0 reboot=t panic=-1 ARIZUKO_CTL=%d ARIZUKO_MCP=%d",
			s.CtlPort, s.McpPort),
		"-chardev", "socket,id=vfs0,path=" + s.RootSock,
		"-device", "vhost-user-fs-pci,queue-size=1024,chardev=vfs0,tag=" + shareRoot,
		"-chardev", "socket,id=vfs1,path=" + s.TurnSock,
		"-device", "vhost-user-fs-pci,queue-size=1024,chardev=vfs1,tag=" + shareTurn,
		"-device", fmt.Sprintf("vhost-vsock-pci,guest-cid=%d", s.CID),
		"-netdev", "tap,id=net0,ifname=" + s.Tap + ",script=no,downscript=no",
		"-device", "virtio-net-pci,netdev=net0,mac=" + s.MAC,
		"-nodefaults",
		"-no-reboot",
		"-serial", "file:" + s.Console,
		"-display", "none",
		"-pidfile", s.PidFile,
	}
}

// guestMAC derives the guest's address from its CID. 52:54:00 is QEMU's own
// locally-administered prefix, and the CID is unique among live guests, so no two
// live taps carry the same address.
func guestMAC(cid uint32) string {
	return fmt.Sprintf("52:54:00:%02x:%02x:%02x", byte(cid>>16), byte(cid>>8), byte(cid))
}

// virtiofsdArgs renders one share's daemon.
//
// The turn share is clamped: --uid-map and --gid-map state which guest ids exist
// on the host, so an id outside the range has none, and --sandbox=none because
// runed has already made the namespaces. The flags are the Rust daemon's; the
// packaged C build has none of them, which is what the clamp precondition
// refuses on.
//
// The flag names come from clampUIDFlag and clampGIDFlag, the same constants the
// precondition requires in the daemon's --help. A literal here is how BUGS `L367`
// happened: the args said --translate-uid, 1.10.1 answers "unexpected argument",
// and the check passed because it looked for the other spelling.
func virtiofsdArgs(p qemuPaths, sock, source string, clamp bool) []string {
	args := []string{
		"--socket-path=" + sock,
		"--shared-dir=" + source,
		"--cache=always",
		"--sandbox=none",
	}
	if clamp {
		args = append(args,
			clampUIDFlag+"="+p.UIDMap,
			clampGIDFlag+"="+p.GIDMap)
	}
	return args
}

// Run starts one guest turn.
func (q *qemuRuntime) Run(ctx context.Context, t Turn) RunResult {
	_, endSpan := obs.StartSpan(ctx, "container_spawn",
		"folder", t.Folder, "image", q.paths.Rootfs)
	obs.ContainerActiveInc()
	start := time.Now()

	res := q.spawn(ctx, t)

	obs.ContainerActiveDec()
	var spawnErr error
	if res.Outcome == runedv1.OutcomeError {
		spawnErr = fmt.Errorf("%s", res.Error)
	}
	endSpan(spawnErr)
	obs.RecordContainerSpawn(t.Folder, res.Outcome, time.Since(start).Seconds())
	return res
}

// spawn is the whole turn: the record, the tree, the two shares, the tap and its
// proxy, the two vsock listeners, the bridge, QEMU, the control channel, and the
// reap. Every exit returns one RunResult, so Run's span and metrics close in one
// place.
func (q *qemuRuntime) spawn(ctx context.Context, t Turn) RunResult {
	fail := func(f string, a ...any) RunResult {
		return RunResult{Outcome: runedv1.OutcomeError, Error: fmt.Sprintf(f, a...)}
	}

	// Step 1: the CID, the two ports and the record, before anything is started.
	q.alloc.Lock()
	rec, allocErr := q.allocate(t)
	q.alloc.Unlock()
	if allocErr != nil {
		return fail("%v", allocErr)
	}
	tree := treeFor(rec.TurnDir)
	// One teardown for every exit, in the order spec 6/41 §"The turn" step 9
	// fixes. It removes the record only when every pid it names is gone.
	defer func() {
		if err := reapVM(rec, q.procs, q.paths.Grace, q.reapPoll, tree, q); err != nil {
			slog.Error("vm teardown incomplete; record kept for the next disk scan",
				"handle", rec.Handle, "err", err)
		}
	}()

	groupDir, _ := q.folders.GroupPath(t.Folder)
	logsDir := filepath.Join(groupDir, "logs")
	os.MkdirAll(logsDir, 0o755)
	ts := time.Now().Format("2006-01-02T15-04-05")
	console := filepath.Join(logsDir, "container-"+ts+".console.log")

	// Step 2: the staging tree and the two shares.
	plan := treePlan(t.Mounts)
	if err := tree.stageTargets(plan); err != nil {
		return fail("%v", err)
	}
	if err := tree.bindAll(plan); err != nil {
		return fail("%v", err)
	}
	rootSock := filepath.Join(rec.TurnDir, rootSockName)
	turnSock := filepath.Join(rec.TurnDir, turnSockName)
	// The release rootfs is exported as it sits: the guest mounts it read-only
	// with a tmpfs overlay upper, and the clamp lands any write the guest
	// attempts as the agent's uid, which owns nothing in a root-owned rootfs.
	if err := q.startVirtiofsd(rec, rootSock, q.paths.Rootfs, false); err != nil {
		return fail("%v", err)
	}
	if err := q.startVirtiofsd(rec, turnSock, tree.dir, true); err != nil {
		return fail("%v", err)
	}

	// Step 3: the tap, its rule set and this turn's proxy — all before the guest
	// exists, so a guest never runs with a route it was not granted.
	if err := q.tapUp(rec); err != nil {
		return fail("%v", err)
	}
	if err := q.rulesUp(rec); err != nil {
		return fail("%v", err)
	}
	eg, proxyURL, err := q.startEgress(rec, t.Egress)
	if err != nil {
		return fail("%v", err)
	}
	rec.EgressPid = eg.Process.Pid
	if err := rec.save(); err != nil {
		return fail("vm record: %v", err)
	}
	go func() { _ = eg.Wait() }()
	// The guest's environment is the rendered one plus this substrate's own
	// proxy address. Copied, because the renderer's map is the Manager's and a
	// substrate that wrote into it would hand the next reader its own address.
	env := layerImageEnv(q.paths.ImageEnv, t.Env)
	proxyEnv(env, proxyURL)

	// Step 4: both vsock listeners, bound BEFORE QEMU starts. A port a squatter
	// already holds fails here, with no guest in existence and no traffic to
	// take; runed never picks another port.
	ctlL, err := vsockListen(rec.CtlPort)
	if err != nil {
		return fail("%v", err)
	}
	defer ctlL.Close()
	mcpL, err := vsockListen(rec.McpPort)
	if err != nil {
		return fail("%v", err)
	}

	// Step 5: the MCP bridge, one child per turn, running as the agent's uid so
	// routd's SO_PEERCRED check is the one it already runs.
	bridge, err := q.startBridge(rec, mcpL)
	mcpL.Close() // the child holds it now
	if err != nil {
		return fail("%v", err)
	}
	rec.BridgePid = bridge.Process.Pid
	if err := rec.save(); err != nil {
		return fail("vm record: %v", err)
	}
	go func() { _ = bridge.Wait() }()

	// Step 6: QEMU.
	args := qemuArgs(qemuSpec{
		Kernel: q.paths.Kernel, Initrd: q.paths.Initrd,
		RootSock: rootSock, TurnSock: turnSock,
		Mem: q.paths.Mem, SMP: q.paths.SMP,
		CID: rec.CID, Tap: rec.Tap, MAC: guestMAC(rec.CID),
		PidFile: filepath.Join(rec.TurnDir, qemuPidName),
		Console: console,
		CtlPort: rec.CtlPort, McpPort: rec.McpPort,
	})
	slog.Info("booting guest", "folder", t.Folder, "handle", t.Handle,
		"cid", rec.CID, "tap", rec.Tap, "mounts", len(plan.Mounts))
	slog.Debug("qemu args", "folder", t.Folder, "args", strings.Join(args, " "))
	qemu := exec.Command(q.paths.Qemu, args...)
	qemu.Stderr = newPrefixWriter("qemu", t.Folder)
	if err := qemu.Start(); err != nil {
		// The VMM did not launch. A retry spawns nothing, so the turn is
		// terminal rather than re-fed.
		return RunResult{Outcome: runedv1.OutcomeError,
			Error: "qemu start: " + err.Error(), Terminal: true}
	}
	rec.QemuPid = qemu.Process.Pid
	if err := rec.save(); err != nil {
		return fail("vm record: %v", err)
	}
	qemuDone := make(chan error, 1)
	go func() { qemuDone <- qemu.Wait() }()

	audit.Emit(context.Background(), audit.Event{
		Category: audit.CategoryAgent,
		Action:   "container.spawn",
		Actor:    "system",
		Surface:  audit.SurfaceGateway,
		Resource: "containers/" + t.Handle,
		Folder:   t.Folder,
		Outcome:  audit.OutcomeOK,
		ParamsSummary: map[string]any{
			"substrate": substrateName,
			"cid":       rec.CID,
			"elevated":  t.Env["ARIZUKO_IS_ROOT"] == "1",
		},
	})

	// Step 7: the guest's control channel. Only a peer whose CID is this VM's is
	// served; the host kernel stamped it, so nothing else can claim it.
	start := time.Now()
	type accepted struct {
		conn *os.File
		cid  uint32
		err  error
	}
	ac := make(chan accepted, 1)
	go func() {
		c, cid, err := vsockAccept(ctlL, time.Now().Add(q.paths.Boot))
		ac <- accepted{c, cid, err}
	}()
	var conn *os.File
	select {
	case a := <-ac:
		if a.err != nil {
			return fail("guest did not open ctl within %s (kernel console: %s): %v",
				q.paths.Boot, console, a.err)
		}
		if a.cid != rec.CID {
			a.conn.Close()
			return fail("ctl peer cid %d is not this guest's %d", a.cid, rec.CID)
		}
		conn = a.conn
	case werr := <-qemuDone:
		// QEMU died before the guest said anything. Its own status and the
		// console are the whole story, and reporting them now beats waiting out
		// the boot timeout on every turn.
		return fail("qemu exited before the guest opened ctl (code %d, kernel console: %s)",
			exitCodeOf(werr), console)
	case <-ctx.Done():
		return fail("turn deadline reached before the guest opened ctl (kernel console: %s)", console)
	}
	defer conn.Close()

	var logbuf lineBuffer
	sess := newCtlSession(conn, func(line string) {
		slog.Debug("guest", "folder", t.Folder, "line", line)
		logbuf.add(line)
	})
	served := make(chan error, 1)
	go func() { served <- sess.serve() }()

	// Step 8: the turn itself — its environment and its payload, on ctl.
	if err := sess.send(ctlFrame{Kind: ctlTurn, Env: env, Stdin: t.Stdin}); err != nil {
		return fail("send turn frame: %v", err)
	}
	if t.RegisterSteer != nil {
		t.RegisterSteer(func(batch string) bool { return sess.steer(batch, q.paths.Grace) })
	}

	end := q.await(ctx, sess, qemuDone, served, t)
	code, timedOut := end.code, end.timedOut
	elapsed := time.Since(start)
	logFile := filepath.Join(logsDir, "container-"+ts+".log")
	writeLog(logFile, t, elapsed, code, timedOut, logbuf.String()+"\nkernel console: "+console+"\n")

	slog.Info("guest exited", "folder", t.Folder, "handle", t.Handle,
		"code", code, "duration", elapsed, "timedOut", timedOut)
	exitOutcome := audit.OutcomeOK
	exitErrMsg := ""
	if timedOut {
		exitOutcome, exitErrMsg = audit.OutcomeError, "timeout"
	} else if code != 0 {
		exitOutcome, exitErrMsg = audit.OutcomeError, fmt.Sprintf("exit_code=%d", code)
	}
	audit.Emit(context.Background(), audit.Event{
		Category: audit.CategoryAgent, Action: "container.exit", Actor: "system",
		Surface: audit.SurfaceGateway, Resource: "containers/" + t.Handle,
		Folder: t.Folder, Outcome: exitOutcome, ErrorMsg: exitErrMsg,
		DurationMS: elapsed.Milliseconds(),
		ParamsSummary: map[string]any{
			"exit_code": code, "timed_out": timedOut, "substrate": substrateName,
		},
	})

	tail := logbuf.String()
	if len(tail) > 200 {
		tail = tail[len(tail)-200:]
	}
	return turnResult(end, elapsed, console, tail)
}

// turnResult maps how a turn ended onto what routd reads. One place, because
// each of these four answers means something different to the dispatcher: a
// retry, a fresh spawn, a breaker trip, or nothing at all.
func turnResult(end turnEnd, elapsed time.Duration, console, tail string) RunResult {
	if end.timedOut {
		return RunResult{Outcome: runedv1.OutcomeError,
			Error:    fmt.Sprintf("Guest timed out after %s", elapsed.Round(time.Second)),
			ExitCode: end.code}
	}
	if !end.framed {
		// No exit frame is no result. QEMU's status says the VMM ended, never
		// how the agent did, so reporting this as a clean turn would be the
		// silent success the project forbids.
		return RunResult{Outcome: runedv1.OutcomeError, ExitCode: end.code,
			Error: fmt.Sprintf("guest ended without an exit frame (qemu status %d, kernel console: %s)",
				end.code, console)}
	}
	if end.code != 0 {
		return RunResult{Outcome: runedv1.OutcomeError,
			Error:    fmt.Sprintf("Guest exited with code %d: %s", end.code, tail),
			ExitCode: end.code, Terminal: guestTerminalExit(end.code)}
	}
	// The agent's output goes out of band over routd's MCP socket, so this
	// substrate never sees it: a completed run is OK.
	return RunResult{Outcome: runedv1.OutcomeOK}
}

// allocate picks this guest's CID, its two vsock ports, its tap and its /30, and
// writes the record. Every later step appends its pid to that record as it
// starts the process, so a runed that dies at any point leaves a record naming
// what it started.
//
// The live set comes from the disk, never from memory: that is what lets a
// restarted runed see the guests it left behind.
func (q *qemuRuntime) allocate(t Turn) (*vmRecord, error) {
	records, broken := listVMRecords(q.paths.VMRoot)
	for _, dir := range broken {
		slog.Error("vm record unreadable: its processes cannot be reaped by name", "dir", dir)
	}
	cid, err := allocateCID(liveCIDs(records), q.cfg.MaxContainers)
	if err != nil {
		return nil, err
	}
	rec, err := newVMRecord(q.paths.VMRoot, t.Handle, t.Folder, cid, q.paths.VsockBase)
	if err != nil {
		return nil, err
	}
	idx := int(cid - vsockCIDMin)
	rec.Tap = tapName(q.cfg.Name, idx)
	rec.ProxyPort = q.paths.ProxyPort
	if rec.GuestIP, rec.HostIP, err = tapNet(q.paths.TapSubnet, idx); err != nil {
		return nil, err
	}
	if err := rec.save(); err != nil {
		return nil, fmt.Errorf("vm record: %w", err)
	}
	return rec, nil
}

// await waits for the turn to end, whichever way it ends.
//
// The one deadline is ctx's: Manager.spawn arms it from RunTTL with the group's
// container_config Timeout folded in, so this substrate holds no ceiling of its
// own. On ctx death the guest is asked to stop and then killed — the deferred
// reap SIGKILLs the QEMU pid and confirms it gone, because a guest power-off does
// not prove QEMU exited.
func (q *qemuRuntime) await(
	ctx context.Context, sess *ctlSession, qemuDone <-chan error, served <-chan error, t Turn,
) turnEnd {
	var soft <-chan time.Time
	if dl, ok := ctx.Deadline(); ok {
		if d := time.Until(dl) - containerSoftDeadlineOffset; d > 0 {
			timer := time.NewTimer(d)
			defer timer.Stop()
			soft = timer.C
		}
	}
	for {
		select {
		case code := <-sess.exit:
			// The guest's own exit frame. QEMU is on its way out; the reap
			// waits for the pid with its bounded grace either way.
			return turnEnd{code: code, framed: true}
		case <-soft:
			soft = nil
			// The same warning the Docker substrate writes into the ipc dir,
			// carried by the mechanism that reaches the guest's ipc dir here.
			slog.Info("soft deadline firing, warning agent", "folder", t.Folder, "handle", t.Handle)
			go sess.steer(deadlineWarning, q.paths.Grace)
		case err := <-served:
			// The control channel ended. That happens right AFTER a guest sends
			// its exit frame, so the frame is looked for first — see lastWord.
			if err != nil {
				slog.Error("ctl channel failed", "folder", t.Folder, "err", err)
			}
			if code, ok := drainExit(sess); ok {
				return turnEnd{code: code, framed: true}
			}
			// The guest died without saying how. QEMU's own status is the only
			// remaining word on what happened.
			select {
			case werr := <-qemuDone:
				return turnEnd{code: exitCodeOf(werr)}
			case <-time.After(q.paths.Grace):
				return turnEnd{}
			}
		case werr := <-qemuDone:
			// QEMU exited first: a guest that powered off, or a VMM that died.
			if code, ok := lastWord(sess, served, q.paths.Grace); ok {
				return turnEnd{code: code, framed: true}
			}
			return turnEnd{code: exitCodeOf(werr)}
		case <-ctx.Done():
			slog.Info("turn deadline reached, stopping guest", "folder", t.Folder, "handle", t.Handle)
			_ = sess.stop()
			if code, ok := lastWord(sess, served, q.paths.Grace); ok {
				return turnEnd{code: code, timedOut: true, framed: true}
			}
			return turnEnd{timedOut: true}
		}
	}
}

// lastWord waits for the guest's exit frame once something else has ended the
// turn.
//
// The frame and the process exit RACE: QEMU's status lands on one channel while
// the guest's last bytes are still in the reader, and a select over both picks
// either. Deciding "no exit frame" on that coin toss would report a turn that
// went perfectly as one with no result, which is worse than the silence it was
// meant to catch. The read loop dispatches the frame BEFORE it returns and the
// exit channel is buffered, so one look after `served` fires is conclusive.
func lastWord(sess *ctlSession, served <-chan error, grace time.Duration) (int, bool) {
	select {
	case code := <-sess.exit:
		return code, true
	case <-served:
		return drainExit(sess)
	case <-time.After(grace):
		return drainExit(sess)
	}
}

// drainExit takes the exit frame if one has already arrived.
func drainExit(sess *ctlSession) (int, bool) {
	select {
	case code := <-sess.exit:
		return code, true
	default:
		return 0, false
	}
}

// turnEnd is how a turn ended.
type turnEnd struct {
	code     int
	timedOut bool
	// framed is true when the GUEST reported the exit. RunResult.ExitCode is
	// the {exit} frame (spec 6/41 §"The turn"), so a turn without one has no
	// result: QEMU's own status says the VMM ended, never how the agent did.
	// Reporting that as a clean turn is the silent-success the project forbids.
	framed bool
}

// guestTerminalExit reports whether the guest's exit code proves a retry cannot
// repair the turn. Only the agent's own EX_CONFIG counts: Docker's reserved
// 125-127 are the CLI's invocation failures, and on this substrate those three
// are just exit codes the agent may return (spec 5/12, BUGS F73).
func guestTerminalExit(code int) bool { return code == agentTerminalExit }

// deadlineWarning is the message the agent gets two minutes before the hard
// kill. Same text the Docker substrate delivers, because it is the agent's
// contract, not the substrate's.
const deadlineWarning = "⚠️ SYSTEM: You have ~2 minutes before this session is forcefully terminated. " +
	"Wrap up NOW: summarize what you accomplished, what is still pending, and deliver your response to the user."

// exitCodeOf reads a process's exit code out of its Wait error.
func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return 0
}

// startVirtiofsd starts one share's daemon and records its pid before it can
// exit unnoticed.
func (q *qemuRuntime) startVirtiofsd(rec *vmRecord, sock, source string, clamp bool) error {
	cmd := exec.Command(q.paths.Virtiofsd, virtiofsdArgs(q.paths, sock, source, clamp)...)
	cmd.Stderr = newPrefixWriter("virtiofsd", rec.Folder)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("virtiofsd %s: %w", source, err)
	}
	rec.VirtiofsdPids = append(rec.VirtiofsdPids, cmd.Process.Pid)
	if err := rec.save(); err != nil {
		_ = cmd.Process.Kill()
		return fmt.Errorf("vm record: %w", err)
	}
	go func() { _ = cmd.Wait() }()
	// QEMU connects to the socket, so it must exist before QEMU starts. The
	// daemon creates it as it comes up.
	if err := waitForSocket(sock, q.paths.Grace); err != nil {
		return fmt.Errorf("virtiofsd %s: %w", source, err)
	}
	return nil
}

// waitForSocket waits for a vhost-user socket to appear.
func waitForSocket(path string, within time.Duration) error {
	deadline := time.Now().Add(within)
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("socket %s did not appear within %s", path, within)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// startBridge forks this turn's MCP bridge as the agent's uid, handing it the
// already-bound listener on fd 3. The child needs no privilege of its own and
// cannot choose another port.
func (q *qemuRuntime) startBridge(rec *vmRecord, listener *os.File) (*exec.Cmd, error) {
	socket := groupfolder.IpcSocket(mustIpcPath(q.folders, rec.Folder))
	uid, _ := container.RunUID()
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("mcp bridge: %w", err)
	}
	cmd := exec.Command(self, mcpBridgeFlag, strconv.FormatUint(uint64(rec.CID), 10), socket)
	cmd.ExtraFiles = []*os.File{listener}
	cmd.Stderr = newPrefixWriter("mcp-bridge", rec.Folder)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(uid)},
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mcp bridge: %w", err)
	}
	return cmd, nil
}

// mustIpcPath resolves the folder's ipc dir. An unresolvable folder yields an
// empty path, which the bridge reports when it cannot dial: routd binds that
// socket before the spawn, so an empty one is routd's absence, not a guess to
// paper over.
func mustIpcPath(folders *groupfolder.Resolver, folder string) string {
	dir, err := folders.IpcPath(folder)
	if err != nil {
		slog.Error("ipc dir unresolvable; the guest's tool calls will have nowhere to go",
			"folder", folder, "err", err)
		return ""
	}
	return dir
}

// Kill stops a live guest by handle (DELETE /v1/runs/{id}). Idempotent: a handle
// with no record on disk is a guest that is already gone.
//
// It reads the record from disk rather than an in-memory index, so the
// dashboard's kill and the disk scan reap the same way and a runed that
// restarted mid-turn can still kill what it left.
func (q *qemuRuntime) Kill(handle string) error {
	if handle == "" {
		return nil
	}
	dir, err := vmDir(q.paths.VMRoot, handle)
	if err != nil {
		return err
	}
	rec, err := loadVMRecord(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return reapVM(rec, q.procs, q.paths.Grace, q.reapPoll, treeFor(rec.TurnDir), q)
}

// sweep reaps every guest a previous runed left on disk. This is the orphan
// sweep, and the record is what makes it possible: four host processes per turn
// and no label, so a scan that could only find QEMU would leave the proxy, the
// bridge and both shares running with a live allowlist.
func (q *qemuRuntime) sweep() {
	records, broken := listVMRecords(q.paths.VMRoot)
	for _, dir := range broken {
		slog.Error("vm record unreadable at startup: its processes cannot be reaped by name",
			"dir", dir)
	}
	for _, rec := range records {
		slog.Warn("reaping a guest left by a previous runed",
			"handle", rec.Handle, "folder", rec.Folder, "cid", rec.CID)
		if err := reapVM(rec, q.procs, q.paths.Grace, q.reapPoll, treeFor(rec.TurnDir), q); err != nil {
			slog.Error("startup sweep incomplete", "handle", rec.Handle, "err", err)
		}
	}
}

// lineBuffer collects the guest's log lines for the turn log, bounded the way
// the Docker substrate bounds its stderr.
type lineBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lineBuffer) add(line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.b.Len() >= maxOutputSize {
		return
	}
	l.b.WriteString(line)
	l.b.WriteByte('\n')
}

func (l *lineBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// newPrefixWriter turns a child's stderr into log lines, so a virtiofsd or a
// proxy that complains is read rather than discarded.
func newPrefixWriter(what, folder string) io.Writer {
	r, w := io.Pipe()
	go func() {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 16*1024), 1024*1024)
		for sc.Scan() {
			slog.Info("vm process", "what", what, "folder", folder, "line", sc.Text())
		}
	}()
	return w
}

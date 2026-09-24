package runed

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
)

// The end-to-end guest: a real kernel on real KVM, the turn share, the control
// channel, the power-off and the reap. It drives the same functions Run drives —
// there is no test-only path into the substrate, because the preconditions are
// what keep an unclampable host from running turns and a flag that turned them
// off would be a gate that cannot fail.
//
// It runs by hand, on a host where every precondition passes:
//
//	sudo -E go test ./runed/ -run TestGuestBootsAndReportsOverCtl -count=1 -v
//
// and it SKIPS with the refusal text otherwise, so the skip names what the host
// is missing. Today that is the Rust virtiofsd the uid clamp needs (spec 6/41
// §Open question 2) and the `make kernel` artifact.
func TestGuestBootsAndReportsOverCtl(t *testing.T) {
	if testing.Short() {
		t.Skip("needs /dev/kvm, root and the release artifacts; run it by name: " +
			"sudo -E go test ./runed/ -run TestGuestBootsAndReportsOverCtl -count=1")
	}
	cfg, folders := renderWiring(t)
	cfg.MaxContainers = 5
	paths, err := qemuPreconditions(cfg)
	if err != nil {
		t.Skipf("this host cannot run the QEMU substrate:\n%v", err)
	}
	q := &qemuRuntime{cfg: cfg, folders: folders, paths: paths,
		procs: hostProcs{}, reapPoll: 10 * time.Millisecond}

	rec, err := newVMRecord(paths.VMRoot, "arizuko-test-boot", "solo/inbox", 3, paths.VsockBase)
	if err != nil {
		t.Fatal(err)
	}
	if err := rec.save(); err != nil {
		t.Fatal(err)
	}
	tree := treeFor(rec.TurnDir)

	// One family, so the assertion is about the share and not about the mount
	// table: the group's home, writable, which is where the clamp has to land.
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	plan := treePlan([]core.Mount{{Host: home, Container: core.ContainerHome}})
	if err := tree.stageTargets(plan); err != nil {
		t.Fatal(err)
	}
	if err := tree.bindAll(plan); err != nil {
		t.Fatal(err)
	}
	// guest-init reads this and echoes it back on ctl, which proves the share is
	// mounted rather than merely attached.
	if err := os.WriteFile(filepath.Join(tree.dir, "probe.txt"), []byte("hello-from-host\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rootSock := filepath.Join(rec.TurnDir, rootSockName)
	turnSock := filepath.Join(rec.TurnDir, turnSockName)
	if err := q.startVirtiofsd(rec, rootSock, paths.Rootfs, false); err != nil {
		t.Fatal(err)
	}
	if err := q.startVirtiofsd(rec, turnSock, tree.dir, true); err != nil {
		t.Fatal(err)
	}
	ctlL, err := vsockListen(rec.CtlPort)
	if err != nil {
		t.Fatalf("bind ctl: %v", err)
	}
	defer ctlL.Close()

	console := filepath.Join(rec.TurnDir, "console.log")
	args := qemuArgs(qemuSpec{
		Kernel: paths.Kernel, Initrd: paths.Initrd,
		RootSock: rootSock, TurnSock: turnSock,
		Mem: paths.Mem, SMP: paths.SMP, CID: rec.CID,
		Tap: tapName(cfg.Name, 0), MAC: guestMAC(rec.CID),
		PidFile: filepath.Join(rec.TurnDir, qemuPidName),
		Console: console, CtlPort: rec.CtlPort, McpPort: rec.McpPort,
	})
	qemu := exec.Command(paths.Qemu, args...)
	qemu.Stderr = os.Stderr
	started := time.Now()
	if err := qemu.Start(); err != nil {
		t.Fatalf("qemu start: %v", err)
	}
	rec.QemuPid = qemu.Process.Pid
	if err := rec.save(); err != nil {
		t.Fatal(err)
	}
	qemuDone := make(chan error, 1)
	go func() { qemuDone <- qemu.Wait() }()
	t.Cleanup(func() {
		if err := reapVM(rec, q.procs, paths.Grace, q.reapPoll, tree, nil); err != nil {
			t.Errorf("reap: %v", err)
		}
		// The reap must leave nothing running and no record behind.
		if syscall.Kill(rec.QemuPid, 0) == nil {
			t.Errorf("qemu pid %d is still alive after the reap", rec.QemuPid)
		}
		for _, pid := range rec.VirtiofsdPids {
			if syscall.Kill(pid, 0) == nil {
				t.Errorf("virtiofsd pid %d is still alive after the reap", pid)
			}
		}
		if _, err := os.Stat(filepath.Join(rec.TurnDir, vmRecordName)); !os.IsNotExist(err) {
			t.Errorf("the record survived a complete reap: %v", err)
		}
	})

	conn, peer, err := vsockAccept(ctlL, time.Now().Add(paths.Boot))
	if err != nil {
		t.Fatalf("guest did not open ctl within %s (console: %s): %v", paths.Boot, console, err)
	}
	defer conn.Close()
	if peer != rec.CID {
		t.Fatalf("ctl peer cid %d, want this guest's %d", peer, rec.CID)
	}
	t.Logf("ctl up %s after qemu exec", time.Since(started))

	frames := make(chan ctlFrame, 4)
	go func() {
		_ = decodeCtlFrames(conn, func(f ctlFrame) error { frames <- f; return nil })
		close(frames)
	}()
	var probe map[string]string
	select {
	case f, ok := <-frames:
		if !ok {
			t.Fatal("ctl closed with no frame")
		}
		if f.Kind != ctlReady {
			t.Fatalf("first frame is %q, want %q", f.Kind, ctlReady)
		}
		probe = f.Probe
	case <-time.After(paths.Boot):
		t.Fatalf("no frame within %s (console: %s)", paths.Boot, console)
	}
	t.Logf("guest probe: %v", probe)
	if probe["virtiofs"] != "mounted" {
		t.Errorf("the guest could not mount the turn share: %v", probe["virtiofs_error"])
	}
	if probe["turn_read"] != "hello-from-host" {
		t.Errorf("the guest read %q through the share, want hello-from-host", probe["turn_read"])
	}

	// The uid clamp, measured from the host side. The guest wrote as uid 0 and
	// the file must carry the one host id the folder uses: without the clamp it
	// lands as root root and outlives the guest (measured 2026-09-24).
	if probe["turn_write"] != "ok" {
		t.Fatalf("the guest could not write the share: %v", probe["turn_write_error"])
	}
	fi, err := os.Stat(filepath.Join(tree.dir, "from-guest.txt"))
	if err != nil {
		t.Fatalf("the guest's file is not on the host: %v", err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("no unix stat")
	}
	wantUID, _ := container.RunUID()
	if probe["guest_uid"] != "0" {
		t.Logf("note: the guest wrote as uid %s, so this asserts the clamp weakly", probe["guest_uid"])
	}
	if int(st.Uid) != wantUID {
		t.Errorf("the guest's file is owned by uid %d, want the agent's %d: the clamp is not holding",
			st.Uid, wantUID)
	}

	// The guest powers off. q35 with -no-reboot and reboot=t ends the process
	// cleanly (2.13 s wall for a whole turn, measured 2026-09-24); the grace and
	// the SIGKILL in the reap stay regardless, because that is what makes the
	// reap independent of the shutdown path a guest takes.
	select {
	case werr := <-qemuDone:
		t.Logf("qemu exited %v after %s", werr, time.Since(started))
	case <-time.After(paths.Boot):
		t.Errorf("qemu did not exit within %s of the guest's power-off", paths.Boot)
	}
}

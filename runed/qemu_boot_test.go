package runed

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	"golang.org/x/sys/unix"
)

// Each real KVM guest must mount the shares, enforce the UID clamp, send its
// first ready frame, power off, and leave no resources after reap. make
// bench-boot supplies isolated resources and immutable production artifacts.
func TestGuestBootsAndReportsOverCtl(t *testing.T) {
	if testing.Short() {
		t.Skip("needs isolated KVM benchmark environment")
	}
	for _, key := range []string{
		"RUNED_BENCH_ID", "RUNED_BENCH_OUTPUT_DIR", envKernelDir, envRootfsDir,
		envCIDBase, envVsockBase, envTapSubnet,
	} {
		if os.Getenv(key) == "" {
			t.Fatalf("%s must be set for an isolated boot test", key)
		}
	}
	pairs, err := strconv.Atoi(os.Getenv("RUNED_BENCH_PAIRS"))
	if err != nil || pairs < 1 {
		t.Fatalf("RUNED_BENCH_PAIRS must be a positive count: %v", err)
	}
	cfg, _ := renderWiring(t)
	cfg.Name = os.Getenv("RUNED_BENCH_ID")
	cfg.MaxContainers = 5
	if err := os.Chmod(filepath.Dir(cfg.ProjectRoot), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(cfg.ProjectRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	paths, err := qemuPreconditions(cfg)
	if err != nil {
		t.Fatal(err)
	}
	q := &qemuRuntime{cfg: cfg, paths: paths,
		procs: hostProcs{}, reapPoll: 10 * time.Millisecond}
	variants, err := bootVariants(os.Getenv("RUNED_BENCH_MODE"))
	if err != nil {
		t.Fatal(err)
	}
	for i := range pairs {
		order := slices.Clone(variants)
		if i%2 == 1 {
			order[0], order[1] = order[1], order[0]
		}
		for _, variant := range order {
			name := fmt.Sprintf("%02d-%s", i+1, variant.name)
			if !t.Run(name, func(t *testing.T) {
				d := runGuestBoot(t, q, cfg, name, *variant)
				variant.times = append(variant.times, d)
				t.Logf("BENCH_SAMPLE utc=%s variant=%s pair=%d boot_ms=%d", time.Now().UTC().Format(time.RFC3339Nano), variant.name, i+1, d.Milliseconds())
			}) {
				t.Fatal("boot trial failed")
			}
		}
	}
	for _, group := range variants {
		if len(group.times) != pairs {
			t.Fatalf("%s samples=%d, want %d", group.name, len(group.times), pairs)
		}
		slices.Sort(group.times)
		mid := group.times[len(group.times)/2]
		if len(group.times)%2 == 0 {
			mid = (group.times[len(group.times)/2-1] + mid) / 2
		}
		p90 := group.times[(9*len(group.times)+9)/10-1]
		t.Logf("BENCH_SUMMARY variant=%s n=%d p50_ms=%d p90_ms=%d",
			group.name, len(group.times), mid.Milliseconds(), p90.Milliseconds())
	}
	for _, variant := range variants {
		if !t.Run("precontrol-"+variant.name, func(t *testing.T) {
			diagnostic := *variant
			diagnostic.level = "badport"
			runGuestBoot(t, q, cfg, "precontrol-"+variant.name, diagnostic)
		}) {
			t.Fatal("guest pre-control failure was not visible or did not power off")
		}
	}
}

func runGuestBoot(t *testing.T, q *qemuRuntime, cfg *core.Config, name string, variant bootVariant) time.Duration {
	level := variant.level
	var capture *bootCapture
	t.Cleanup(func() {
		if capture != nil {
			capture.dump(t)
		}
	})
	t.Helper()
	paths := q.paths
	rec, err := newVMRecord(paths.VMRoot, name, "solo/inbox", paths.CIDBase, 0, paths.VsockBase)
	if err != nil {
		t.Fatal(err)
	}
	rec.Tap = tapName(cfg.Name, 0)
	rec.GuestIP, rec.HostIP, err = tapNet(paths.TapSubnet, 0)
	if err != nil {
		t.Fatal(err)
	}
	rec.ProxyPort = paths.ProxyPort
	if err := rec.save(); err != nil {
		t.Fatal(err)
	}
	tree := treeFor(rec.TurnDir)
	t.Cleanup(func() {
		if err := os.Remove(filepath.Join(tree.dir, "probe.txt")); err != nil && !os.IsNotExist(err) {
			t.Errorf("remove test probe: %v", err)
		}
		if err := reapVM(rec, q.procs, paths.Grace, q.reapPoll, tree, q); err != nil {
			t.Errorf("reap: %v", err)
		}
		if rec.QemuPid > 0 && syscall.Kill(rec.QemuPid, 0) == nil {
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
		if _, err := os.Stat(rec.TurnDir); !os.IsNotExist(err) {
			t.Errorf("the turn directory survived a complete reap: %v", err)
		}
	})

	home := filepath.Join(cfg.ProjectRoot, "home-"+name)
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	uid, _ := container.RunUID()
	if err := os.Chown(home, uid, uid); err != nil {
		t.Fatal(err)
	}
	plan := treePlan([]core.Mount{{Host: home, Container: core.ContainerHome}})
	if err := tree.stageTargets(plan); err != nil {
		t.Fatal(err)
	}
	if err := tree.bindAll(plan); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree.dir, "probe.txt"), []byte("hello-from-host\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rootSock := filepath.Join(rec.TurnDir, rootSockName)
	turnSock := filepath.Join(rec.TurnDir, turnSockName)
	rootfsRO, err := tree.bindRootfs(paths.Rootfs)
	if err != nil {
		t.Fatal(err)
	}
	var fs unix.Statfs_t
	if err := unix.Statfs(rootfsRO, &fs); err != nil || fs.Flags&unix.ST_RDONLY == 0 {
		t.Fatalf("rootfs export is not read-only: flags=%x err=%v", fs.Flags, err)
	}
	if err := q.startVirtiofsd(rec, rootSock, rootfsRO, false); err != nil {
		t.Fatal(err)
	}
	if err := q.startVirtiofsd(rec, turnSock, tree.dir, true); err != nil {
		t.Fatal(err)
	}
	if err := q.tapUp(rec); err != nil {
		t.Fatal(err)
	}
	if err := q.rulesUp(rec); err != nil {
		t.Fatal(err)
	}
	ctlL, err := vsockListen(rec.CtlPort)
	if err != nil {
		t.Fatalf("bind ctl: %v", err)
	}
	defer ctlL.Close()

	console := filepath.Join(os.Getenv("RUNED_BENCH_OUTPUT_DIR"), name+".console.log")
	args := qemuArgs(qemuSpec{
		Kernel: paths.Kernel, Initrd: paths.Initrd,
		RootSock: rootSock, TurnSock: turnSock,
		Mem: paths.Mem, SMP: paths.SMP, CID: rec.CID,
		Tap: rec.Tap, MAC: guestMAC(rec.CID),
		GuestIP: rec.GuestIP, HostIP: rec.HostIP,
		PidFile: filepath.Join(rec.TurnDir, qemuPidName),
		Console: console, CtlPort: rec.CtlPort, McpPort: rec.McpPort,
	})
	if level == "badport" {
		found := false
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "-append" {
				args[i+1] = strings.Replace(args[i+1], fmt.Sprintf("ARIZUKO_CTL=%d", rec.CtlPort), "ARIZUKO_CTL=bad", 1)
				if !strings.Contains(args[i+1], "ARIZUKO_CTL=bad") {
					t.Fatal("production command line has no control port token")
				}
				found = true
				break
			}
		}
		if !found {
			t.Fatal("QEMU argument renderer has no kernel command line")
		}
	} else if level != "" && level != "5" {
		t.Fatalf("unknown kernel log level %q", level)
	}
	args, err = bootArgs(args, variant)
	if err != nil {
		t.Fatal(err)
	}
	qemu := exec.Command(paths.Qemu, args...)
	qemu.Stderr = os.Stderr
	for _, metric := range []string{"cpu", "memory", "io"} {
		pressure, err := os.ReadFile("/proc/pressure/" + metric)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("BENCH_PRESSURE variant=%s metric=%s value=%q", variant.name, metric, strings.TrimSpace(string(pressure)))
	}
	if variant.trace {
		capture = newBootCapture(strings.TrimSuffix(console, ".console.log"))
		qemu.Stdout = capture.console
		qemu.Stderr = capture.trace
	}
	t.Logf("BENCH_ARGS variant=%s args=%q", variant.name, args)
	started := time.Now()
	if capture != nil {
		capture.console.start = started
		capture.trace.start = started
	}
	if err := qemu.Start(); err != nil {
		t.Fatalf("qemu start: %v", err)
	}
	rec.QemuPid = qemu.Process.Pid
	if err := rec.save(); err != nil {
		t.Fatal(err)
	}
	qemuDone := make(chan error, 1)
	go func() { qemuDone <- qemu.Wait() }()
	if level == "badport" {
		select {
		case err := <-qemuDone:
			if err != nil {
				t.Fatalf("pre-control guest did not power off cleanly: %v", err)
			}
		case <-time.After(paths.Boot):
			t.Fatalf("pre-control guest did not power off within %s", paths.Boot)
		}
		if capture != nil {
			capture.dump(t)
		}
		lines, err := os.ReadFile(console)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(lines), "guest-init: kernel command line has ARIZUKO_CTL=") ||
			!strings.Contains(string(lines), "ARIZUKO_PROBE ") {
			t.Fatalf("pre-control failure or diagnostic probe missing from console %s", console)
		}
		t.Logf("BENCH_DIAGNOSTIC utc=%s failure=bad_ctl visible=true", time.Now().UTC().Format(time.RFC3339Nano))
		return 0
	}

	conn, peer, err := vsockAccept(ctlL, time.Now().Add(paths.Boot))
	if err != nil {
		t.Fatalf("guest did not open ctl within %s (console: %s): %v", paths.Boot, console, err)
	}
	defer conn.Close()
	if peer != rec.CID {
		t.Fatalf("ctl peer cid %d, want this guest's %d", peer, rec.CID)
	}
	t.Logf("ctl accepted %s after qemu exec", time.Since(started))

	frames := make(chan ctlFrame, 4)
	go func() {
		_ = decodeCtlFrames(conn, func(f ctlFrame) error { frames <- f; return nil })
		close(frames)
	}()
	var probe map[string]string
	var ready time.Duration
	select {
	case f, ok := <-frames:
		if !ok {
			t.Fatal("ctl closed with no frame")
		}
		if f.Kind != ctlReady {
			t.Fatalf("first frame is %q, want %q", f.Kind, ctlReady)
		}
		ready = time.Since(started)
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

	if probe["turn_write"] != "ok" {
		t.Fatalf("the guest could not write the share: %v", probe["turn_write_error"])
	}
	if probe["root_write"] != "refused" {
		t.Fatalf("the guest could write outside the uid clamp: %q", probe["root_write"])
	}
	fi, err := os.Stat(filepath.Join(home, ".arizuko-probe"))
	if err != nil {
		t.Fatalf("the guest's file is not on the host: %v", err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("no unix stat")
	}
	wantUID, _ := container.RunUID()
	if probe["guest_uid"] != "0" {
		t.Fatalf("guest probe uid = %q, want root before the clamp", probe["guest_uid"])
	}
	if int(st.Uid) != wantUID {
		t.Errorf("the guest's file is owned by uid %d, want the agent's %d: the clamp is not holding",
			st.Uid, wantUID)
	}

	if err := conn.Close(); err != nil {
		t.Fatalf("close control: %v", err)
	}
	select {
	case werr := <-qemuDone:
		if werr != nil {
			t.Fatalf("guest poweroff: %v", werr)
		}
		t.Logf("qemu exited %v after %s", werr, time.Since(started))
		if kib := maxrssKiB(qemu.ProcessState); kib <= 0 {
			t.Errorf("qemu peak resident set = %d KiB, want a non-zero Maxrss", kib)
		}
	case <-time.After(paths.Grace):
		t.Fatal("qemu did not exit within reap grace")
	}
	if capture != nil {
		capture.dump(t)
	}
	lines, err := os.ReadFile(console)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(lines), "ARIZUKO_PROBE ") {
		t.Fatalf("guest diagnostic probe missing from console %s", console)
	}
	return ready
}

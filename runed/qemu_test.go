package runed

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

// The argument list is the substrate's whole machine, and every value in it was
// measured on hel1v5 on 2026-09-24 (spec 6/41 §"The engine", §"The turn" step 5).
// So the expectation is spelled out rather than derived from qemuArgs: a test that
// asked the renderer what it renders would pass any change to it, including the
// ones that are known not to work — microvm (which binds no virtio device on QEMU
// 7.2.22, so vsock stays ENODEV), a missing -numa node (which breaks the share
// silently), or a dropped -no-reboot (without which a guest power-off leaves QEMU
// alive).
func TestQemuArgsRendersTheMeasuredMachine(t *testing.T) {
	got := qemuArgs(qemuSpec{
		Kernel: "/srv/images/k/bzImage", Initrd: "/srv/images/k/initrd.gz",
		RootSock: "/srv/vms/h1/root.sock", TurnSock: "/srv/vms/h1/turn.sock",
		Mem: "2G", SMP: 2, CID: 7, Tap: "arz-ab12-t04", MAC: "52:54:00:00:00:07",
		PidFile: "/srv/vms/h1/qemu.pid", Console: "/g/logs/container-x.console.log",
		CtlPort: 5563, McpPort: 5564,
		GuestIP: "10.97.0.17", HostIP: "10.97.0.18",
	})
	want := []string{
		"-M", "q35",
		"-enable-kvm",
		"-cpu", "host",
		"-smp", "2",
		"-m", "2G",
		"-object", "memory-backend-memfd,id=mem,size=2G,share=on",
		"-numa", "node,memdev=mem",
		"-kernel", "/srv/images/k/bzImage",
		"-initrd", "/srv/images/k/initrd.gz",
		// ip= is how the guest gets an address and a default route AT ALL: nothing in
		// the guest configures a NIC, so without it every outbound request hangs to
		// the turn's deadline — the agent reached "Session initialized" and waited
		// there (BUGS `L377`). The gateway is the tap's host side, which is where
		// this turn's proxy listens and the only address its rule set accepts.
		"-append", "console=ttyS0 reboot=t panic=-1 ARIZUKO_CTL=5563 ARIZUKO_MCP=5564 " +
			"ip=10.97.0.17::10.97.0.18:255.255.255.252::eth0:off",
		"-chardev", "socket,id=vfs0,path=/srv/vms/h1/root.sock",
		"-device", "vhost-user-fs-pci,queue-size=1024,chardev=vfs0,tag=root",
		"-chardev", "socket,id=vfs1,path=/srv/vms/h1/turn.sock",
		"-device", "vhost-user-fs-pci,queue-size=1024,chardev=vfs1,tag=turn",
		"-device", "vhost-vsock-pci,guest-cid=7",
		"-netdev", "tap,id=net0,ifname=arz-ab12-t04,script=no,downscript=no",
		"-device", "virtio-net-pci,netdev=net0,mac=52:54:00:00:00:07",
		"-nodefaults",
		"-no-reboot",
		"-serial", "file:/g/logs/container-x.console.log",
		"-display", "none",
		"-pidfile", "/srv/vms/h1/qemu.pid",
	}
	if !slices.Equal(got, want) {
		t.Errorf("qemu args drifted from the measured list\ngot:  %s\nwant: %s",
			strings.Join(got, " "), strings.Join(want, " "))
	}
}

// The memfd backend's size and -m are one number. A hardcoded size would leave a
// guest whose backend is smaller than its memory, which is a share that stops
// working at a size nobody set.
func TestQemuArgsMemfdSizeFollowsGuestMem(t *testing.T) {
	got := strings.Join(qemuArgs(qemuSpec{Mem: "6144M", SMP: 4}), " ")
	if !strings.Contains(got, "-m 6144M") {
		t.Errorf("-m missing the configured size: %s", got)
	}
	if !strings.Contains(got, "memory-backend-memfd,id=mem,size=6144M,share=on") {
		t.Errorf("memfd size does not follow -m: %s", got)
	}
	if !strings.Contains(got, "-smp 4") {
		t.Errorf("-smp missing the configured count: %s", got)
	}
}

func TestGuestMACIsDerivedFromTheCID(t *testing.T) {
	for cid, want := range map[uint32]string{
		3:      "52:54:00:00:00:03",
		258:    "52:54:00:00:01:02",
		131072: "52:54:00:02:00:00",
	} {
		if got := guestMAC(cid); got != want {
			t.Errorf("guestMAC(%d) = %s, want %s", cid, got, want)
		}
	}
}

// The clamp is the one precondition without which the substrate hands every
// folder a host-root write it keeps after the guest is gone (measured
// 2026-09-24). The help text below is the packaged C daemon's own, verbatim from
// `/usr/lib/qemu/virtiofsd --help` on QEMU 7.2.22 — it lists sandbox, cache and
// thread options and no id mapping of any kind.
func TestClampRefusesADaemonThatCannotTranslate(t *testing.T) {
	cHelp := `usage: /usr/lib/qemu/virtiofsd [options]

    -h   --help                print help
    -o cache=<mode>            cache mode. could be one of "auto, always, none"
    -o sandbox=namespace|chroot
                               sandboxing mode:
`
	// Verbatim from virtiofsd 1.10.1, the build Alpine 3.20 packages and the
	// arizuko image carries. The flags are `--uid-map`/`--gid-map`, NOT the
	// `--translate-uid` the spec first named, and the value is a RANGE.
	rustHelp := `Usage: virtiofsd [OPTIONS] --shared-dir <shared_dir>

      --uid-map <UID_MAP>
          Map a range of UIDs from the host into the namespace, given as :namespace_uid:host_uid:count:
      --gid-map <GID_MAP>
      --sandbox <SANDBOX>
`
	cases := []struct {
		name string
		help string
		want string
	}{
		// The C build advertises neither flag and needs real root, so a host with
		// only it cannot run the daemon unprivileged and cannot clamp at all.
		{"packaged C daemon", cHelp, "--uid-map"},
		// The Rust build passes. The flags are the discriminator, not an argument
		// runed sends: an id map is legal only under `--sandbox namespace`, whose
		// own /proc mount docker refuses (BUGS `L372`).
		{"rust daemon", rustHelp, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bin := fakeHelpBinary(t, c.help)
			var missing []string
			checkClamp(qemuPaths{Virtiofsd: bin},
				func(f string, a ...any) { missing = append(missing, fmt.Sprintf(f, a...)) })
			joined := strings.Join(missing, "\n")
			if c.want == "" {
				if len(missing) != 0 {
					t.Fatalf("the Rust daemon must pass, got: %s", joined)
				}
				return
			}
			if !strings.Contains(joined, c.want) {
				t.Fatalf("refusal must name %q, got: %s", c.want, joined)
			}
		})
	}
}

func TestClampRefusesAMissingBinary(t *testing.T) {
	var missing []string
	checkClamp(qemuPaths{Virtiofsd: filepath.Join(t.TempDir(), "absent")},
		func(f string, a ...any) { missing = append(missing, fmt.Sprintf(f, a...)) })
	if len(missing) != 1 || !strings.Contains(missing[0], "RUNED_VIRTIOFSD") {
		t.Fatalf("a missing daemon must be named once with its setting, got %v", missing)
	}
}

// The guest size and the concurrency cap are one budget: this refusal is what
// stops five 4 GB guests being admitted onto a 15 GB host, where the second one
// to touch its pages takes the host down rather than the turn (spec 6/41 §"The
// host budget"). Both settings are named, because either one is the fix.
func TestMemoryFitsIsTheGuestSizeTimesTheCap(t *testing.T) {
	const total = 16376992 * 1024 // hel1v5's own MemTotal
	if err := memoryFits("4G", 5, total); err == nil {
		t.Fatal("4G x 5 = 20 GiB on a 15.6 GiB host must refuse")
	} else {
		for _, want := range []string{"RUNED_GUEST_MEM", "MAX_CONCURRENT_CONTAINERS"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("refusal must name %s: %v", want, err)
			}
		}
	}
	if err := memoryFits("2G", 5, total); err != nil {
		t.Errorf("2G x 5 = 10 GiB must fit 15.6 GiB: %v", err)
	}
	if err := memoryFits("2G", 0, total); err != nil {
		t.Errorf("a cap of 0 is one guest, not none: %v", err)
	}
	if err := memoryFits("plenty", 5, total); err == nil {
		t.Error("an unparseable -m size must refuse rather than pass as zero")
	}
}

func TestParseMemTotalReadsTheKernelsKilobytes(t *testing.T) {
	const meminfo = `MemTotal:       16376992 kB
MemFree:          390500 kB
MemAvailable:    3396432 kB
`
	got, err := parseMemTotal(meminfo)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got != 16376992*1024 {
		t.Errorf("MemTotal = %d bytes, want %d", got, 16376992*1024)
	}
	if _, err := parseMemTotal("Committed_AS: 12 kB\n"); err == nil {
		t.Error("a meminfo with no MemTotal must be an error, not zero")
	}
}

func TestParseGuestMemTakesQemusSpelling(t *testing.T) {
	for in, want := range map[string]int64{
		"2G":   2 << 30,
		"2g":   2 << 30,
		"2048": 2048 << 20, // QEMU's bare -m is a count of MiB
		"512M": 512 << 20,
	} {
		got, err := parseGuestMem(in)
		if err != nil {
			t.Errorf("parseGuestMem(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("parseGuestMem(%q) = %d, want %d", in, got, want)
		}
	}
	for _, in := range []string{"", "2T", "-1G", "lots"} {
		if _, err := parseGuestMem(in); err == nil {
			t.Errorf("parseGuestMem(%q) must refuse", in)
		}
	}
}

// The turn share is clamped and the release rootfs is not: the rootfs is
// root-owned and the guest mounts it read-only, so the flags differ by exactly
// the two translate options.
// virtiofsdArgs renders ONE list for both shares: the clamp is the daemon's uid,
// not a flag (BUGS `L372`). An id map is legal only under `--sandbox namespace`,
// and that sandbox's own /proc mount is EPERM inside docker, measured three ways.
// So a `-map` flag in this list is a daemon that refuses to start.
func TestVirtiofsdArgsCarryNoIDMap(t *testing.T) {
	want := []string{
		"--socket-path=/vms/h/turn.sock",
		"--shared-dir=/vms/h/tree",
		"--cache=always",
		"--sandbox=none",
	}
	if got := virtiofsdArgs(qemuPaths{}, "/vms/h/turn.sock", "/vms/h/tree"); !slices.Equal(got, want) {
		t.Errorf("turn share args\ngot:  %v\nwant: %v", got, want)
	}
	for _, a := range virtiofsdArgs(qemuPaths{}, "/vms/h/root.sock", "/srv/rootfs") {
		if strings.Contains(a, "-map") || strings.Contains(a, "sandbox=namespace") {
			t.Errorf("an id map or its namespace sandbox is a daemon that will not start: %v", a)
		}
	}
}

// The rule set is the whole egress story on this substrate: one accept to the
// proxy, and a drop on everything else in both directions — which is what makes
// UDP/53 unreachable and closes the resolver side channel by construction
// (BUGS L136, L189).
func TestTapRulesAreProxyOnly(t *testing.T) {
	rec := &vmRecord{Tap: "arz-ab12-t01", GuestIP: "10.97.0.5", HostIP: "10.97.0.6", ProxyPort: 3128}
	got := tapRules(rec)
	want := [][]string{
		{"INPUT", "-i", "arz-ab12-t01", "-s", "10.97.0.5", "-d", "10.97.0.6", "-p", "tcp", "--dport", "3128", "-j", "ACCEPT"},
		{"OUTPUT", "-o", "arz-ab12-t01", "-s", "10.97.0.6", "-d", "10.97.0.5", "-p", "tcp", "--sport", "3128", "-j", "ACCEPT"},
		{"INPUT", "-i", "arz-ab12-t01", "-j", "DROP"},
		{"OUTPUT", "-o", "arz-ab12-t01", "-j", "DROP"},
		{"FORWARD", "-i", "arz-ab12-t01", "-j", "DROP"},
		{"FORWARD", "-o", "arz-ab12-t01", "-j", "DROP"},
	}
	if len(got) != len(want) {
		t.Fatalf("rule count %d, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if !slices.Equal(got[i], want[i]) {
			t.Errorf("rule %d\ngot:  %v\nwant: %v", i, got[i], want[i])
		}
	}
	// No rule may name a protocol other than tcp, and none may accept anything
	// but the proxy port: a UDP accept here would be the DNS path the design
	// says does not exist.
	for _, r := range got[2:] {
		if !slices.Contains(r, "DROP") {
			t.Errorf("every rule after the two accepts must DROP: %v", r)
		}
	}
}

func TestTapNetGivesTheGuestTheLowAddress(t *testing.T) {
	cases := []struct {
		idx         int
		guest, host string
	}{
		{0, "10.97.0.1", "10.97.0.2"},
		{1, "10.97.0.5", "10.97.0.6"},
		{63, "10.97.0.253", "10.97.0.254"},
	}
	for _, c := range cases {
		g, h, err := tapNet("10.97.0.0/24", c.idx)
		if err != nil {
			t.Fatalf("tapNet idx %d: %v", c.idx, err)
		}
		if g != c.guest || h != c.host {
			t.Errorf("tapNet idx %d = %s/%s, want %s/%s", c.idx, g, h, c.guest, c.host)
		}
	}
	if _, _, err := tapNet("10.97.0.0/24", 64); err == nil {
		t.Error("an index past the subnet must refuse rather than wrap into the next /24")
	}
	if _, _, err := tapNet("10.97.0.0/31", 0); err == nil {
		t.Error("a subnet with no room for a guest and a host must refuse")
	}
}

func TestTapNameFitsAnInterfaceName(t *testing.T) {
	n := tapName("arizuko_krons", 4)
	if len(n) > 15 {
		t.Errorf("tap name %q is %d characters; an ifname is 15", n, len(n))
	}
	if n == tapName("arizuko_sloth", 4) {
		t.Error("two instances on one host must not render the same tap name")
	}
	if n == tapName("arizuko_krons", 5) {
		t.Error("two guests on one instance must not render the same tap name")
	}
}

// The `*` translation and the gateway append come from container.EgressRules, so
// the two substrates cannot disagree about what a folder may reach. This asserts
// the QEMU side renders that decision into the binary's flags.
func TestEgressArgsPinTheGuestAndTranslateStar(t *testing.T) {
	rec := &vmRecord{GuestIP: "10.97.0.5", HostIP: "10.97.0.6", ProxyPort: 3128}
	got := egressArgs(rec, []string{"api.github.com", "*"}, "https://gw.example:4000")
	want := []string{
		"--listen", "10.97.0.6:3128",
		"--client", "10.97.0.5",
		"--allow", "api.github.com,gw.example",
		"--allow-all",
	}
	if !slices.Equal(got, want) {
		t.Errorf("egress args\ngot:  %v\nwant: %v", got, want)
	}
	plain := egressArgs(rec, []string{"api.github.com"}, "")
	if slices.Contains(plain, "--allow-all") {
		t.Errorf("a list without `*` must not allow every host: %v", plain)
	}
	if !slices.Contains(plain, "--client") {
		t.Errorf("the proxy must always be pinned to its one guest: %v", plain)
	}
}

// The ports come from the turn's SLOT, so an instance's port window sits where the
// operator put it whatever CID window it owns. Deriving from the absolute CID tied
// the two together — base 20 with port base 5655 produced 5689 — and ran the tap
// subnet's index off the end of a /24 at any CID base over 64 (BUGS `L382`).
func TestVsockPortsAreDerivedFromTheSlot(t *testing.T) {
	for _, c := range []struct{ slot, ctl, mcp uint32 }{
		{0, 5555, 5556},
		{1, 5557, 5558},
		{4, 5563, 5564},
	} {
		ctl, mcp := vsockPorts(5555, c.slot)
		if ctl != c.ctl || mcp != c.mcp {
			t.Errorf("vsockPorts(5555, slot %d) = %d/%d, want %d/%d", c.slot, ctl, mcp, c.ctl, c.mcp)
		}
	}
	// The window is where the operator put it: slot 0 of a second instance is its
	// OWN base, never its base plus its CID offset.
	if ctl, _ := vsockPorts(5655, 0); ctl != 5655 {
		t.Errorf("slot 0 of the 5655 window is %d, want 5655", ctl)
	}
}

func TestAllocateCIDSkipsTheGuestsOnDisk(t *testing.T) {
	got, err := allocateCID(map[uint32]bool{3: true, 4: true}, 5, vsockCIDMin)
	if err != nil || got != 5 {
		t.Errorf("allocateCID = %d, %v; want 5, nil", got, err)
	}
	if got, err := allocateCID(nil, 5, vsockCIDMin); err != nil || got != 3 {
		t.Errorf("the first guest takes CID 3 (0,1,2 are reserved by vsock(7)), got %d, %v", got, err)
	}
	// A SECOND INSTANCE on the same host takes its own window, or both ask the host
	// for CID 3 and the same derived ports — which is the failure the second
	// instance's first turn showed: "vsock bind port 5555: address already in use"
	// (BUGS `L378`).
	if got, err := allocateCID(nil, 5, 20); err != nil || got != 20 {
		t.Errorf("a window starting at 20 must hand out 20, got %d, %v", got, err)
	}
	if got, err := allocateCID(map[uint32]bool{20: true}, 5, 20); err != nil || got != 21 {
		t.Errorf("a window skips its own live guests, got %d, %v", got, err)
	}
	// A base below the reserved floor is raised, never honoured: 0, 1 and 2 belong
	// to vsock(7) and a guest that claimed one would collide with the host itself.
	if got, err := allocateCID(nil, 5, 1); err != nil || got != vsockCIDMin {
		t.Errorf("a base under the floor must be raised to %d, got %d, %v", vsockCIDMin, got, err)
	}
	full := map[uint32]bool{3: true, 4: true, 5: true, 6: true, 7: true}
	if _, err := allocateCID(full, 5, vsockCIDMin); err == nil {
		t.Error("a full host must refuse rather than reuse a live guest's CID")
	}
}

// The refusal is one error naming every missing input, so an operator restarts
// runed once rather than once per input.
func TestQemuPreconditionsNameEveryMissingInput(t *testing.T) {
	t.Setenv(envKernelDir, "")
	t.Setenv(envRootfsDir, "")
	t.Setenv(envVirtiofsd, filepath.Join(t.TempDir(), "no-virtiofsd"))
	// BusyBox's ip: on the PATH, and no `tuntap`. The refusal has to come through
	// qemuPreconditions and not only from checkTuntap, or the check is written and
	// never called (BUGS `L369`).
	t.Setenv(envIPBin, fakeExitBinary(t, "ip", "BusyBox v1.36.1 multi-call binary.\n\nUsage: ip [OPTIONS] address|route|link", 1))
	_, err := qemuPreconditions(cfgFor(t))
	if err == nil {
		t.Fatal("a host with no kernel artifact, no rootfs and no clamp must refuse")
	}
	for _, want := range []string{
		envKernelDir,
		envRootfsDir,
		envVirtiofsd,
		envIPBin,
		"iproute2",
		"`make kernel`",
		"precondition(s) missing",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal must name %q:\n%v", want, err)
		}
	}
}

// fakeHelpBinary writes an executable that prints the given --help text, so the
// clamp check can be driven over a daemon that is not installed.
func fakeHelpBinary(t *testing.T, help string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "virtiofsd")
	script := "#!/bin/sh\ncat <<'EOF'\n" + help + "\nEOF\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake virtiofsd: %v", err)
	}
	return p
}

// cfgFor is the config the substrate checks read: the concurrency cap and the
// data dir, nothing else.
func cfgFor(t *testing.T) *core.Config {
	t.Helper()
	cfg, _ := renderWiring(t)
	cfg.MaxContainers = 5
	return cfg
}

func TestAwaitTakesTheExitFrameWhenQemuExitsInTheSameMoment(t *testing.T) {
	q := &qemuRuntime{paths: qemuPaths{Grace: 50 * time.Millisecond}}
	for i := range 50 {
		host, guest := net.Pipe()
		sess := newCtlSession(host, nil)
		served := make(chan error, 1)
		frame, err := encodeCtlFrame(ctlFrame{Kind: ctlExit, Code: 78})
		if err != nil {
			t.Fatal(err)
		}
		// The guest sends its exit frame and the channel then ends, which is
		// exactly what a power-off looks like.
		go func() {
			guest.Write(frame) //nolint:errcheck // the read loop sees it or the test fails
			guest.Close()
		}()
		go func() { served <- sess.serve() }()
		qemuDone := make(chan error, 1)
		// Wait for the frame to be dispatched, then make BOTH channels ready.
		deadline := time.Now().Add(2 * time.Second)
		for len(sess.exit) == 0 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		qemuDone <- nil
		end := q.await(context.Background(), sess, qemuDone, served, Turn{Folder: "solo/inbox"})
		host.Close()
		if !end.framed || end.code != 78 {
			t.Fatalf("round %d: await = %+v, want the guest's own exit 78", i, end)
		}
	}
}

// A turn whose guest never sent an exit frame has no result, and that is an
// error: QEMU's status says the VMM ended, never how the agent did.
func TestAwaitReportsAGuestThatNeverSaidHowItExited(t *testing.T) {
	q := &qemuRuntime{paths: qemuPaths{Grace: 20 * time.Millisecond}}
	host, guest := net.Pipe()
	defer host.Close()
	sess := newCtlSession(host, nil)
	served := make(chan error, 1)
	go func() { served <- sess.serve() }()
	guest.Close()
	qemuDone := make(chan error, 1)
	qemuDone <- nil
	end := q.await(context.Background(), sess, qemuDone, served, Turn{Folder: "solo/inbox"})
	if end.framed {
		t.Fatalf("await = %+v, want framed=false for a guest that said nothing", end)
	}
}

// What each ending means to routd. The values are named, because each answer
// sends the dispatcher somewhere different: ok closes the turn, an error with
// Terminal set stops the retry, and an error without it is re-fed.
func TestTurnResultMapsEveryEnding(t *testing.T) {
	const console = "/g/logs/container-x.console.log"
	cases := []struct {
		name     string
		end      turnEnd
		outcome  string
		exitCode int
		terminal bool
		names    string
	}{
		{"clean", turnEnd{code: 0, framed: true}, runedv1.OutcomeOK, 0, false, ""},
		{"agent error", turnEnd{code: 1, framed: true}, runedv1.OutcomeError, 1, false, "code 1"},
		{"agent config failure is terminal", turnEnd{code: 78, framed: true}, runedv1.OutcomeError, 78, true, "code 78"},
		{"a docker reserve is not terminal here", turnEnd{code: 125, framed: true}, runedv1.OutcomeError, 125, false, "code 125"},
		{"no exit frame", turnEnd{code: 0}, runedv1.OutcomeError, 0, false, "without an exit frame"},
		{"timeout", turnEnd{timedOut: true}, runedv1.OutcomeError, 0, false, "timed out"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := turnResult(c.end, 90*time.Second, console, "tail")
			if got.Outcome != c.outcome {
				t.Errorf("outcome %q, want %q", got.Outcome, c.outcome)
			}
			if got.ExitCode != c.exitCode {
				t.Errorf("exit code %d, want %d", got.ExitCode, c.exitCode)
			}
			if got.Terminal != c.terminal {
				t.Errorf("terminal %v, want %v", got.Terminal, c.terminal)
			}
			if c.names != "" && !strings.Contains(got.Error, c.names) {
				t.Errorf("error %q must name %q", got.Error, c.names)
			}
			if c.outcome == runedv1.OutcomeOK && got.Error != "" {
				t.Errorf("a clean turn carries no error, got %q", got.Error)
			}
		})
	}
	// The unframed error points at the kernel console, which is the only place
	// a guest that said nothing left anything behind.
	if e := turnResult(turnEnd{}, time.Second, console, ""); !strings.Contains(e.Error, console) {
		t.Errorf("an unframed turn must name the console: %q", e.Error)
	}
}

// A rootfs TREE carries no image config, and docker is what applies it under the
// other substrate: PATH and HOME belong to the image, which env_contract_test.go
// names as their owner. So the export captures the image's ENV beside the tree and
// this substrate layers it UNDER the rendered turn env — the order docker uses,
// `ENV` first and then `-e`. Without it the agent runs on whatever PATH the shell
// compiled in, and node is not on that.
func TestTheImageEnvIsTheBaseAndTheTurnWins(t *testing.T) {
	image := map[string]string{
		"PATH":         "/usr/local/go/bin:/usr/bin:/bin",
		"NODE_VERSION": "22.23.2",
		"TZ":           "Etc/UTC",
	}
	rendered := map[string]string{"TZ": "Europe/Prague", "ARIZUKO_MODEL": "opus"}

	env := layerImageEnv(image, rendered)

	if env["PATH"] != "/usr/local/go/bin:/usr/bin:/bin" {
		t.Errorf("the image's PATH must reach the guest, got %q", env["PATH"])
	}
	if env["NODE_VERSION"] != "22.23.2" {
		t.Errorf("every image ENV line reaches the guest, got %q", env["NODE_VERSION"])
	}
	if env["TZ"] != "Europe/Prague" {
		t.Errorf("the rendered turn env must WIN on a clash, got TZ=%q", env["TZ"])
	}
	if env["ARIZUKO_MODEL"] != "opus" {
		t.Errorf("the turn's own keys must survive, got %q", env["ARIZUKO_MODEL"])
	}
	// The renderer's map is the Manager's; writing into it would hand the next
	// reader this turn's values.
	if len(rendered) != 2 {
		t.Errorf("layerImageEnv must not write into the rendered map: %v", rendered)
	}
}

// readImageEnv parses docker's own `{{json .Config.Env}}`, so the format is
// docker's. A tree without the file is a stale export and refuses: guessing a PATH
// here would be a SECOND declaration of what ant/Dockerfile already says.
func TestReadImageEnvRefusesWhatItCannotUse(t *testing.T) {
	cases := []struct {
		name, body string
		wantKeys   int
		wantErr    string
	}{
		{"docker's own shape", `["PATH=/bin","HOME=/home/node"]`, 2, ""},
		{"a bare name means inherit, and a guest has nothing to inherit from",
			`["PATH=/bin","INHERIT_ME"]`, 1, ""},
		{"an empty set declared nothing", `[]`, 0, "declared nothing"},
		{"not an array at all", `{"PATH":"/bin"}`, 0, "cannot unmarshal"},
		{"entries but none usable", `["A","B"]`, 0, "none was KEY=value"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, imageEnvName), []byte(c.body), 0o644); err != nil {
				t.Fatal(err)
			}
			env, err := readImageEnv(dir)
			if c.wantKeys == 0 {
				if err == nil {
					t.Fatalf("must refuse, got %v", env)
				}
				// The MESSAGE, because two guards reject an empty array and a
				// bare "it errored" cannot tell which one ran.
				if !strings.Contains(err.Error(), c.wantErr) {
					t.Errorf("refusal must say %q, got %v", c.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("must parse docker's shape: %v", err)
			}
			if len(env) != c.wantKeys {
				t.Errorf("got %d keys, want %d: %v", len(env), c.wantKeys, env)
			}
		})
	}
	// A missing file is the stale-export case and is also a refusal.
	if _, err := readImageEnv(t.TempDir()); err == nil {
		t.Error("a rootfs with no captured image env must refuse, not default")
	}
}

// TestCheckTuntapRefusesBusyBoxIP pins BUGS L369. BusyBox's `ip` applet sits on
// Alpine's PATH and understands no `tuntap`, so a LookPath check passes and the
// tap — the guest's only interface — cannot be created. The failure then arrives
// once per turn as a usage message while runed reports healthy.
func TestCheckTuntapRefusesBusyBoxIP(t *testing.T) {
	busybox := fakeExitBinary(t, "ip", `BusyBox v1.36.1 (2025-11-23 14:32:18 UTC) multi-call binary.

Usage: ip [OPTIONS] address|route|link|tunnel|neigh|rule [ARGS]`, 1)

	var missing []string
	checkTuntap(qemuPaths{IP: busybox}, func(f string, a ...any) {
		missing = append(missing, fmt.Sprintf(f, a...))
	})
	if len(missing) != 1 {
		t.Fatalf("BusyBox ip must be refused, got %d refusals: %v", len(missing), missing)
	}
	for _, want := range []string{"tuntap", "BusyBox", "iproute2"} {
		if !strings.Contains(missing[0], want) {
			t.Errorf("the refusal must name %q so an operator knows the fix: %s", want, missing[0])
		}
	}

	// A capable `ip` passes. Exit 0 is what `ip tuntap show` answers when it
	// understands the subcommand, whether or not any tap exists.
	real := fakeExitBinary(t, "ip", "", 0)
	missing = nil
	checkTuntap(qemuPaths{IP: real}, func(f string, a ...any) {
		missing = append(missing, fmt.Sprintf(f, a...))
	})
	if len(missing) != 0 {
		t.Errorf("an ip that understands tuntap must pass: %v", missing)
	}
}

// fakeExitBinary writes an executable that prints out and exits with code.
func fakeExitBinary(t *testing.T, name, out string, code int) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	script := "#!/bin/sh\ncat <<'EOF'\n" + out + "\nEOF\nexit " + strconv.Itoa(code) + "\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake %s: %v", name, err)
	}
	return p
}

// TestWaitForSocketGivesUpWhenTheDaemonDies pins the other half of BUGS L372.
// virtiofsd CREATES its socket and then enters its sandbox, so a daemon that
// refuses its own arguments leaves the file behind: the old wait saw the file,
// returned nil, and QEMU answered "Failed to connect … Connection refused" one
// step later — naming the chardev and not the cause.
func TestWaitForSocketGivesUpWhenTheDaemonDies(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "turn.sock")
	// The file EXISTS and the daemon is gone: exactly the state that read as ready.
	if err := os.WriteFile(sock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	died := make(chan error, 1)
	died <- fmt.Errorf("exit status 1")
	err := waitForSocket(sock, time.Second, died)
	if err == nil {
		t.Fatal("a dead daemon must fail the wait even though its socket is there")
	}
	if !strings.Contains(err.Error(), "exited before its socket was usable") {
		t.Errorf("the failure must name the daemon's exit, not the socket: %v", err)
	}

	// A live daemon whose socket is there passes.
	if err := waitForSocket(sock, time.Second, make(chan error, 1)); err != nil {
		t.Errorf("a live daemon with its socket in place must pass: %v", err)
	}
}

// TestTapSubnetIsCheckedForEverySlot pins the half of BUGS L382 that let it ship: the
// precondition checked slot 0 and nothing else, so a subnet that holds the first
// guest and not the last passed at start and failed on the second concurrent turn.
// One arbitrary point is not a capacity check — `memoryFits` already knows that.
func TestTapSubnetIsCheckedForEverySlot(t *testing.T) {
	// A /28 is the smallest subnet tapNet accepts, and it holds FOUR /30 windows. So
	// slots 0-3 fit and slot 4 does not: the shape that used to be invisible, because
	// the precondition only ever asked about slot 0.
	const small = "10.97.0.0/28"
	for slot := range 4 {
		if _, _, err := tapNet(small, slot); err != nil {
			t.Fatalf("slot %d of a /28 must fit, got %v", slot, err)
		}
	}
	if _, _, err := tapNet(small, 4); err == nil {
		t.Fatal("slot 4 of a /28 must not fit: the test's own premise is wrong otherwise")
	}
	// Now the precondition's own loop shape, with a cap one past the subnet.
	var missing []string
	miss := func(f string, a ...any) { missing = append(missing, fmt.Sprintf(f, a...)) }
	for slot := 0; slot < 5; slot++ {
		if _, _, err := tapNet(small, slot); err != nil {
			miss("tap subnet cannot hold %d guests: %v", 5, err)
			break
		}
	}
	if len(missing) != 1 {
		t.Fatalf("a cap of 5 on a /28 must be refused exactly once, got %v", missing)
	}
	if !strings.Contains(missing[0], "past the subnet") {
		t.Errorf("the refusal must name the subnet's limit: %s", missing[0])
	}
}

// TestSealIPv6TakesTheFamilyOffTheTap pins BUGS `L383`. The rule set is iptables,
// so it says nothing about IPv6; without the seal the guest and the host side of
// the tap each take a link-local address and the guest reaches every IPv6 listener
// in runed's namespace, runed's own :8080 included.
//
// Docker closes it today by setting disable_ipv6=1 in a container on a v4-only
// network. That is somebody else's default, so the seal makes it arizuko's fact.
func TestSealIPv6TakesTheFamilyOffTheTap(t *testing.T) {
	root := t.TempDir()
	ipv6ConfDir = root
	t.Cleanup(func() { ipv6ConfDir = "/proc/sys/net/ipv6/conf" })

	const tap = "arz-ab12-t01"
	if err := os.MkdirAll(filepath.Join(root, tap), 0o755); err != nil {
		t.Fatal(err)
	}
	knob := filepath.Join(root, tap, "disable_ipv6")
	if err := os.WriteFile(knob, []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := sealIPv6(tap); err != nil {
		t.Fatalf("seal: %v", err)
	}
	if b, _ := os.ReadFile(knob); strings.TrimSpace(string(b)) != "1" {
		t.Errorf("disable_ipv6 = %q, want 1", strings.TrimSpace(string(b)))
	}

	// A kernel with no IPv6 has no knob and nothing to disable.
	if err := sealIPv6("arz-ab12-t99"); err != nil {
		t.Errorf("an absent knob is not an error: %v", err)
	}

	// A knob that will not take the write IS an error: the turn must not run with
	// an address family its rule set cannot see.
	if err := os.MkdirAll(filepath.Join(root, "arz-ab12-t02", "disable_ipv6"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := sealIPv6("arz-ab12-t02"); err == nil {
		t.Error("a failed write must fail the turn")
	}
}

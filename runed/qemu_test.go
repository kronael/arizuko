package runed

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
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
		"-append", "console=ttyS0 reboot=t panic=-1 ARIZUKO_CTL=5563 ARIZUKO_MCP=5564",
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
		name   string
		help   string
		uidMap string
		gidMap string
		want   string
	}{
		{"packaged C daemon", cHelp, ":1000:1000:1:", ":1000:1000:1:", "--uid-map"},
		{"no mapping given", rustHelp, "", "", "RUNED_VIRTIOFSD_UID_MAP"},
		{"gid mapping missing", rustHelp, ":1000:1000:1:", "", "RUNED_VIRTIOFSD_GID_MAP"},
		{"clamped", rustHelp, ":1000:1000:1:", ":1000:1000:1:", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bin := fakeHelpBinary(t, c.help)
			var missing []string
			checkClamp(qemuPaths{Virtiofsd: bin, UIDMap: c.uidMap, GIDMap: c.gidMap},
				func(f string, a ...any) { missing = append(missing, fmt.Sprintf(f, a...)) })
			joined := strings.Join(missing, "\n")
			if c.want == "" {
				if len(missing) != 0 {
					t.Fatalf("a clamping daemon with both mappings must pass, got: %s", joined)
				}
				return
			}
			if !strings.Contains(joined, c.want) {
				t.Fatalf("refusal must name %q, got: %s", c.want, joined)
			}
		})
	}
}

// A virtiofsd path that is not a file at all is named too: an image built without
// the Rust stage leaves nothing there, and the spawn would otherwise fail per
// turn instead of once at start.
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
// The clamp flags are the ones virtiofsd 1.10.1 ACTUALLY takes, and the literals
// below are copied from its own --help rather than from the code (BUGS `L367`):
//
//	--uid-map <UID_MAP>   given as :namespace_uid:host_uid:count:
//	--gid-map <GID_MAP>   given as :namespace_gid:host_gid:count:
//
// The daemon answers "unexpected argument '--translate-uid' found" to the name
// the substrate used to send, so a test that read the expectation off
// virtiofsdArgs was green while no turn could mount its share.
func TestVirtiofsdArgsClampOnlyTheTurnShare(t *testing.T) {
	p := qemuPaths{UIDMap: ":1000:1000:1:", GIDMap: ":1000:1000:1:"}
	turn := virtiofsdArgs(p, "/vms/h/turn.sock", "/vms/h/tree", true)
	want := []string{
		"--socket-path=/vms/h/turn.sock",
		"--shared-dir=/vms/h/tree",
		"--cache=always",
		"--sandbox=none",
		"--uid-map=:1000:1000:1:",
		"--gid-map=:1000:1000:1:",
	}
	if !slices.Equal(turn, want) {
		t.Errorf("turn share args\ngot:  %v\nwant: %v", turn, want)
	}
	root := virtiofsdArgs(p, "/vms/h/root.sock", "/srv/images/ant/rootfs", false)
	for _, a := range root {
		if strings.HasSuffix(strings.SplitN(a, "=", 2)[0], "-map") {
			t.Errorf("the read-only rootfs share must carry no translation: %v", root)
		}
	}
	if !slices.Contains(root, "--shared-dir=/srv/images/ant/rootfs") {
		t.Errorf("root share source missing: %v", root)
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

func TestVsockPortsAreDerivedFromTheCID(t *testing.T) {
	for _, c := range []struct{ cid, ctl, mcp uint32 }{
		{3, 5555, 5556},
		{4, 5557, 5558},
		{7, 5563, 5564},
	} {
		ctl, mcp := vsockPorts(5555, c.cid)
		if ctl != c.ctl || mcp != c.mcp {
			t.Errorf("vsockPorts(5555, %d) = %d/%d, want %d/%d", c.cid, ctl, mcp, c.ctl, c.mcp)
		}
	}
}

func TestAllocateCIDSkipsTheGuestsOnDisk(t *testing.T) {
	got, err := allocateCID(map[uint32]bool{3: true, 4: true}, 5)
	if err != nil || got != 5 {
		t.Errorf("allocateCID = %d, %v; want 5, nil", got, err)
	}
	if got, err := allocateCID(nil, 5); err != nil || got != 3 {
		t.Errorf("the first guest takes CID 3 (0,1,2 are reserved by vsock(7)), got %d, %v", got, err)
	}
	full := map[uint32]bool{3: true, 4: true, 5: true, 6: true, 7: true}
	if _, err := allocateCID(full, 5); err == nil {
		t.Error("a full host must refuse rather than reuse a live guest's CID")
	}
}

// The refusal is one error naming every missing input, so an operator restarts
// runed once rather than once per input.
func TestQemuPreconditionsNameEveryMissingInput(t *testing.T) {
	t.Setenv(envKernelDir, "")
	t.Setenv(envRootfsDir, "")
	t.Setenv(envVirtiofsd, filepath.Join(t.TempDir(), "no-virtiofsd"))
	t.Setenv(envUIDMap, "")
	t.Setenv(envGIDMap, "")
	_, err := qemuPreconditions(cfgFor(t))
	if err == nil {
		t.Fatal("a host with no kernel artifact, no rootfs and no clamp must refuse")
	}
	for _, want := range []string{
		envKernelDir,
		envRootfsDir,
		envVirtiofsd,
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

// TestCheckMapRangeTakesOnlyTheDaemonsSpelling pins the shape the operator's
// setting must have. `map:0:1000:1` is the value the substrate shipped with, and
// virtiofsd rejects it, so the refusal belongs at runed start and not in a log
// after the first turn's share fails to appear (BUGS `L367`).
func TestCheckMapRangeTakesOnlyTheDaemonsSpelling(t *testing.T) {
	if err := checkMapRange(":1000:1000:1:"); err != nil {
		t.Errorf("the daemon's own spelling must pass: %v", err)
	}
	for _, bad := range []string{"map:0:1000:1", ":0:1000:1", "0:1000:1:", ":a:1000:1:", ":0:1000:0:", ""} {
		if err := checkMapRange(bad); err == nil {
			t.Errorf("%q must be refused: virtiofsd parses the range itself and exits on a bad one", bad)
		}
	}
}

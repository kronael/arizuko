package runed

// What this host must provide before it may run a turn, and the one refusal that
// names everything it does not. Checked at runed START, not per spawn: an input
// found missing per turn is a fleet that looks healthy and answers nothing.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
)

// The QEMU substrate's configuration. Every one of these is an instance setting
// in the generated env, never a value derived from a runtime path: the kernel and
// the rootfs are release artifacts whose names carry a version and a config hash,
// and a daemon that went looking for them would find whichever one was newest.
const (
	envSubstrate    = "RUNED_SUBSTRATE"
	envKernelDir    = "RUNED_KERNEL_DIR"
	envRootfsDir    = "RUNED_ROOTFS_DIR"
	envVirtiofsd    = "RUNED_VIRTIOFSD"
	envUIDMap       = "RUNED_VIRTIOFSD_UID_MAP"
	envGIDMap       = "RUNED_VIRTIOFSD_GID_MAP"
	envQemu         = "RUNED_QEMU"
	envEgressBin    = "RUNED_EGRESS_BIN"
	envFirewallBin  = "RUNED_FIREWALL_BIN"
	envIPBin        = "RUNED_IP_BIN"
	envGuestMem     = "RUNED_GUEST_MEM"
	envGuestSMP     = "RUNED_GUEST_SMP"
	envVsockBase    = "RUNED_VSOCK_PORT_BASE"
	envTapSubnet    = "RUNED_TAP_SUBNET"
	envVMDir        = "RUNED_VM_DIR"
	envBootTimeout  = "RUNED_BOOT_TIMEOUT"
	envReapGrace    = "RUNED_REAP_GRACE"
	envProxyPortVar = "RUNED_PROXY_PORT"
)

// The substrate names RUNED_SUBSTRATE takes.
const (
	SubstrateDocker = "docker"
	SubstrateQEMU   = "qemu"
)

const (
	// bzImage and initrd.gz are what `make kernel` writes into the kernel
	// artifact directory; the substrate reads both from there and copies
	// neither.
	kernelImage  = "bzImage"
	kernelInitrd = "initrd.gz"
	// virtiofsdDefault is where the arizuko image's daemon sits: Alpine 3.20's
	// `virtiofsd` package is the RUST daemon (1.10.1), and it installs the same
	// binary at /usr/libexec/virtiofsd and /usr/lib/qemu/virtiofsd. On a Debian
	// host the same second path holds QEMU's C daemon instead, which carries no
	// id mapping at all, so this default names the file that CAN clamp and the
	// check below refuses the one that cannot (spec 6/41 §"The peer credential").
	virtiofsdDefault = "/usr/libexec/virtiofsd"
	// clampUIDFlag and clampGIDFlag are the clamp. Without it a guest states the
	// uid and gid virtiofsd applies to a host file: measured 2026-09-24, a guest
	// booted as uid 0 created a file owned by host `root root` through the
	// packaged C daemon under -o sandbox=chroot, and it outlived the guest.
	//
	// The flags are `--uid-map` and `--gid-map`, NOT the `--translate-uid` the
	// spec first named: 1.10.1's own --help carries no `translate` of any kind and
	// answers "unexpected argument" to one (BUGS `L367`). Each takes a RANGE,
	// written `:namespace_id:host_id:count:`, so the map says which guest ids
	// exist on the host and every id outside the range has none.
	//
	// These constants are BOTH the string checkClamp requires in the daemon's
	// --help and the string virtiofsdArgs sends it. Two declarations is what
	// L367 was: the check looked for one spelling and the spawn used another.
	clampUIDFlag = "--uid-map"
	clampGIDFlag = "--gid-map"
	// shareRoot and shareTurn are the two virtio-fs tags the guest mounts.
	// imageEnvName sits beside the rootfs tree, written by `make rootfs`.
	imageEnvName = ".image-env.json"
	shareRoot    = "root"
	shareTurn    = "turn"
)

// qemuPaths is what the preconditions resolved: a runtime holds this and looks
// nothing up again per turn.
type qemuPaths struct {
	Qemu      string
	Virtiofsd string
	UIDMap    string
	GIDMap    string
	Egress    string
	Firewall  string
	IP        string
	Kernel    string
	Initrd    string
	Rootfs    string
	// ImageEnv is the agent image's own `ENV` set, captured beside the rootfs by
	// `make rootfs`. docker applies it under the docker substrate — PATH and HOME
	// are the image's, which `env_contract_test.go` records — and a rootfs TREE
	// carries no config, so the host applies it here instead. One declaration,
	// one more reader.
	ImageEnv  map[string]string
	VMRoot    string
	Mem       string
	SMP       int
	VsockBase uint32
	TapSubnet string
	ProxyPort int
	Boot      time.Duration
	Grace     time.Duration
}

// qemuPreconditions resolves and checks everything, collecting every missing
// input so one refusal names them all: an operator who fixes one at a time
// restarts runed once per input.
func qemuPreconditions(cfg *core.Config) (qemuPaths, error) {
	var missing []string
	miss := func(f string, a ...any) { missing = append(missing, fmt.Sprintf(f, a...)) }

	p := qemuPaths{
		Qemu:      core.EnvOr(envQemu, "qemu-system-x86_64"),
		Virtiofsd: core.EnvOr(envVirtiofsd, virtiofsdDefault),
		UIDMap:    os.Getenv(envUIDMap),
		GIDMap:    os.Getenv(envGIDMap),
		Egress:    core.EnvOr(envEgressBin, "egress"),
		Firewall:  core.EnvOr(envFirewallBin, "iptables"),
		IP:        core.EnvOr(envIPBin, "ip"),
		Rootfs:    os.Getenv(envRootfsDir),
		VMRoot:    core.EnvOr(envVMDir, filepath.Join(cfg.ProjectRoot, "vms")),
		Mem:       core.EnvOr(envGuestMem, "2G"),
		SMP:       core.EnvInt(envGuestSMP, 2),
		VsockBase: uint32(core.EnvInt(envVsockBase, 5555)),
		TapSubnet: core.EnvOr(envTapSubnet, "10.97.0.0/24"),
		ProxyPort: core.EnvInt(envProxyPortVar, container.EgressPort),
		Boot:      core.DurOr(envBootTimeout, 30*time.Second),
		Grace:     core.DurOr(envReapGrace, 5*time.Second),
	}

	// runed must be root: the staging tree's bind mounts and a virtiofsd that
	// carries guest credentials to host files both need it (spec 6/41 §"The
	// runed deploy topology").
	if os.Getuid() != 0 {
		miss("runed runs as uid %d; the QEMU substrate needs root for the staging tree's bind mounts and for virtiofsd", os.Getuid())
	}
	// The three device nodes the generator must put in runed's service.
	for _, dev := range []string{"/dev/kvm", "/dev/vhost-vsock", "/dev/net/tun"} {
		f, err := os.OpenFile(dev, os.O_RDWR, 0)
		if err != nil {
			miss("%s does not open (%v) — add it to runed's compose service `devices:`", dev, err)
			continue
		}
		f.Close()
	}
	if _, err := exec.LookPath(p.Qemu); err != nil {
		miss("%s is not executable (%s=%s)", p.Qemu, envQemu, p.Qemu)
	} else if out, err := exec.Command(p.Qemu, "-version").CombinedOutput(); err != nil {
		miss("%s -version failed: %v (%s)", p.Qemu, err, strings.TrimSpace(string(out)))
	}
	for _, b := range []struct{ bin, env string }{
		{p.Egress, envEgressBin}, {p.Firewall, envFirewallBin}, {p.IP, envIPBin},
	} {
		if _, err := exec.LookPath(b.bin); err != nil {
			miss("%s is not executable (%s=%s): %v", b.bin, b.env, b.bin, err)
		}
	}
	// The kernel artifact, by the path the generator writes. `make kernel`
	// builds it into a directory keyed by version and config hash, so the name
	// is configuration and never a guess.
	kdir := os.Getenv(envKernelDir)
	switch {
	case kdir == "":
		miss("%s is unset — set it to the `make kernel` artifact dir holding %s and %s",
			envKernelDir, kernelImage, kernelInitrd)
	default:
		p.Kernel = filepath.Join(kdir, kernelImage)
		p.Initrd = filepath.Join(kdir, kernelInitrd)
		for _, f := range []string{p.Kernel, p.Initrd} {
			if _, err := os.Stat(f); err != nil {
				miss("%s missing: %v (build it with `make kernel`)", f, err)
			}
		}
	}
	if p.Rootfs == "" {
		miss("%s is unset — set it to the unpacked release rootfs (`make images` exports it once per release)", envRootfsDir)
	} else if fi, err := os.Stat(p.Rootfs); err != nil || !fi.IsDir() {
		miss("%s=%s is not a directory (%v)", envRootfsDir, p.Rootfs, err)
	} else if env, err := readImageEnv(p.Rootfs); err != nil {
		// A refusal, not a default. Without the image's PATH the agent's own
		// runtime is not on it, and guessing one here would be a SECOND
		// declaration of what ant/Dockerfile already says.
		miss("%s=%s carries no readable %s (%v): re-run `make rootfs`, which captures "+
			"the image ENV the tree cannot", envRootfsDir, p.Rootfs, imageEnvName, err)
	} else {
		p.ImageEnv = env
	}
	checkClamp(p, miss)
	if total, err := hostMemTotal(); err != nil {
		miss("%s", err)
	} else if err := memoryFits(p.Mem, cfg.MaxContainers, total); err != nil {
		miss("%s", err)
	}
	if _, _, err := tapNet(p.TapSubnet, 0); err != nil {
		miss("%s=%s: %v", envTapSubnet, p.TapSubnet, err)
	}
	if len(missing) > 0 {
		return p, fmt.Errorf("%s=%s cannot run turns on this host; %d precondition(s) missing:\n  - %s",
			envSubstrate, SubstrateQEMU, len(missing), strings.Join(missing, "\n  - "))
	}
	return p, nil
}

// checkClamp is the uid clamp's precondition: the daemon that exports the turn
// share must be able to translate every guest id onto the one host id the folder
// already uses, and the mapping it is given must be stated rather than guessed.
//
// Three cheaper mechanisms were measured out on 2026-09-24 and are not retried
// here: the packaged C daemon has no id mapping of any kind, it cannot run in a
// single-id user namespace because its startup setgroups() fails there, and an
// idmapped mount translates the CALLER so it cannot redirect a root daemon's
// writes (spec 6/41 §"The peer credential").
func checkClamp(p qemuPaths, miss func(string, ...any)) {
	fi, err := os.Stat(p.Virtiofsd)
	if err != nil || fi.IsDir() {
		miss("%s=%s is not an executable file (%v)", envVirtiofsd, p.Virtiofsd, err)
		return
	}
	out, _ := exec.Command(p.Virtiofsd, "--help").CombinedOutput()
	if !strings.Contains(string(out), clampUIDFlag) || !strings.Contains(string(out), clampGIDFlag) {
		miss("%s=%s advertises no %s in its --help: it cannot clamp the guest's uid, "+
			"and a guest as uid 0 then writes host-root-owned files into the folder "+
			"(measured 2026-09-24). The image must carry the Rust virtiofsd",
			envVirtiofsd, p.Virtiofsd, clampUIDFlag+" and "+clampGIDFlag)
		return
	}
	uid, _ := container.RunUID()
	if p.UIDMap == "" || p.GIDMap == "" {
		miss("%s and %s must both be set to the mapping that lands every guest id on host uid %d; "+
			"the value is the operator's measurement, not a default this daemon may guess "+
			"(spec 6/41 §\"Open questions — each with the measurement that settles it\", question 2)",
			envUIDMap, envGIDMap, uid)
		return
	}
	// The SHAPE is checked here because the alternative is a daemon that exits on
	// turn one: virtiofsd parses the range itself and a misspelled map is an
	// "invalid value" it reports to a log nobody is reading yet (BUGS `L367`).
	for _, m := range []struct{ env, val string }{{envUIDMap, p.UIDMap}, {envGIDMap, p.GIDMap}} {
		if err := checkMapRange(m.val); err != nil {
			miss("%s=%s %s; the daemon's own --help spells it :namespace_id:host_id:count:, "+
				"for example :%d:%d:1: for the one id the turn runs as",
				m.env, m.val, err, uid, uid)
		}
	}
}

// checkMapRange holds a virtiofsd id map to the spelling the daemon parses:
// `:namespace_id:host_id:count:`, three decimal fields between four colons.
func checkMapRange(v string) error {
	f := strings.Split(v, ":")
	if len(f) != 5 || f[0] != "" || f[4] != "" {
		return fmt.Errorf("is not a range between four colons")
	}
	for _, n := range f[1:4] {
		if n == "" {
			return fmt.Errorf("leaves a field of the range empty")
		}
		if _, err := strconv.ParseUint(n, 10, 32); err != nil {
			return fmt.Errorf("carries %q where the range wants a number", n)
		}
	}
	if f[3] == "0" {
		return fmt.Errorf("maps a count of 0, so no guest id has a host id")
	}
	return nil
}

// readImageEnv reads the agent image's `ENV` set from beside the rootfs tree.
// `make rootfs` writes docker's own `{{json .Config.Env}}`, a JSON array of
// `KEY=value` strings, so the format is docker's and this only splits it.
//
// A key with no `=` is skipped rather than guessed at: docker allows a bare name
// meaning "inherit", and there is nothing to inherit from in a guest.
func readImageEnv(rootfs string) (map[string]string, error) {
	b, err := os.ReadFile(filepath.Join(rootfs, imageEnvName))
	if err != nil {
		return nil, err
	}
	var lines []string
	if err := json.Unmarshal(b, &lines); err != nil {
		return nil, fmt.Errorf("%s: %w", imageEnvName, err)
	}
	if len(lines) == 0 {
		return nil, fmt.Errorf("%s is an empty set, so the image declared nothing", imageEnvName)
	}
	env := map[string]string{}
	for _, l := range lines {
		if k, v, ok := strings.Cut(l, "="); ok {
			env[k] = v
		}
	}
	if len(env) == 0 {
		return nil, fmt.Errorf("%s held %d entries and none was KEY=value", imageEnvName, len(lines))
	}
	return env, nil
}

// memoryFits is the host-budget guard: the guest size and the concurrency cap
// are ONE setting, because `-m` times the cap is what the host must hold. Five
// guests at 4G is 20 GB on a 15 GB host, so the pair is refused at start rather
// than discovered when the second turn touches its pages (spec 6/41 §"The host
// budget").
func memoryFits(mem string, maxContainers int, total int64) error {
	guest, err := parseGuestMem(mem)
	if err != nil {
		return err
	}
	if maxContainers < 1 {
		maxContainers = 1
	}
	if guest*int64(maxContainers) > total {
		return fmt.Errorf("%s=%s × MAX_CONCURRENT_CONTAINERS=%d is %d MiB, over this host's MemTotal of %d MiB: lower either",
			envGuestMem, mem, maxContainers, guest*int64(maxContainers)>>20, total>>20)
	}
	return nil
}

// parseGuestMem reads QEMU's own -m spelling: a count of MiB, or a suffixed
// size. QEMU takes M and G; anything else is a value the VMM would reject later.
func parseGuestMem(mem string) (int64, error) {
	s := strings.TrimSpace(mem)
	if s == "" {
		return 0, fmt.Errorf("%s is empty", envGuestMem)
	}
	mult := int64(1) << 20 // QEMU's bare -m is MiB
	switch s[len(s)-1] {
	case 'M', 'm':
		s = s[:len(s)-1]
	case 'G', 'g':
		s = s[:len(s)-1]
		mult = int64(1) << 30
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s=%q is not a QEMU -m size (e.g. 2G or 2048)", envGuestMem, mem)
	}
	return n * mult, nil
}

// hostMemTotal reads MemTotal from /proc/meminfo, in bytes.
func hostMemTotal() (int64, error) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, fmt.Errorf("read /proc/meminfo: %w", err)
	}
	return parseMemTotal(string(b))
}

// parseMemTotal pulls MemTotal out of a /proc/meminfo body. The kernel writes it
// in kB, always, as the first line.
func parseMemTotal(meminfo string) (int64, error) {
	for _, line := range strings.Split(meminfo, "\n") {
		rest, ok := strings.CutPrefix(line, "MemTotal:")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 1 {
			break
		}
		kb, err := strconv.ParseInt(f[0], 10, 64)
		if err != nil {
			break
		}
		return kb * 1024, nil
	}
	return 0, fmt.Errorf("/proc/meminfo has no readable MemTotal line")
}

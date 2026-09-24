// guest-init is the guest's entire userspace: PID 1 in the initrd arizuko boots
// per turn (spec 6/41 §"The turn" steps 6-9). It mounts what the host exported,
// takes the turn's env and payload over vsock, runs the agent, and powers the
// guest off. Nothing else is in the initrd, so every driver it uses is built
// into the kernel (kernel/arizuko.fragment).
package main

import (
	"encoding/json"
	"fmt"
	"os"
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

// ctlPort and hostCID are the control channel. vsock(7) fixes the host at CID 2.
const (
	ctlPort = 5555
	hostCID = 2
)

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

	fd, err := dialCtl()
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

	// The first frame on ctl is the turn: its environment and its payload.
	_ = writeFrame(fd, frame{Kind: "ready", Probe: probe})
	off()
}

// mountBase mounts what the guest needs before the turn: proc and sys for the
// runtime, devtmpfs for the console, and a sized tmpfs for the overlay upper.
// The upper is RAM inside -m, so its size is explicit: a write past it is
// ENOSPC in the guest, never a host reclaiming under pressure (spec §"The host
// budget").
func mountBase(probe map[string]string) {
	// /dev is NOT in this list. CONFIG_DEVTMPFS_MOUNT=y means the kernel mounts it
	// before it execs init, and mounting over it cost a whole debug cycle on
	// 2026-09-24: the guest booted, ran its retry loop and powered off with an
	// EMPTY console, because re-mounting /dev invalidated the console fd the
	// kernel had already handed PID 1 as stdout.
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
	_ = os.MkdirAll("/turn", 0o755)
	if err := syscall.Mount("turn", "/turn", "virtiofs", 0, ""); err != nil {
		probe["virtiofs_error"] = err.Error()
	} else {
		probe["virtiofs"] = "mounted"
		// Write as uid 0 from inside the guest. The host must see the file owned by
		// the single id virtiofsd's user namespace maps, whatever the guest claims
		// (spec §"The peer credential"). This is the uid clamp's guest half.
		probe["guest_uid"] = itoa(syscall.Getuid())
		if err := os.WriteFile("/turn/from-guest.txt", []byte("written-as-uid-0\n"), 0o644); err != nil {
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

// kmsg reports through the kernel log, which is the one sink proven to reach the
// host before the console is known good: a failed virtiofs mount logs its source
// name, so the name carries the message. It exists to debug the console itself and
// is the only channel that works when the console does not.
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

func dialCtl() (int, error) {
	fd, err := syscall.Socket(afVSOCK, syscall.SOCK_STREAM, 0)
	if err != nil {
		return -1, fmt.Errorf("vsock socket: %w", err)
	}
	sa := sockaddrVM{Family: afVSOCK, Port: ctlPort, CID: hostCID}
	// The host binds before it starts QEMU, so a refusal here is the host not
	// being ready yet rather than a missing listener. Retry briefly, then give up
	// loudly (spec §"The peer credential": a bind error fails the turn).
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, _, errno := syscall.Syscall(syscall.SYS_CONNECT, uintptr(fd),
			uintptr(unsafe.Pointer(&sa)), unsafe.Sizeof(sa))
		if errno == 0 {
			return fd, nil
		}
		err = errno
		if time.Now().After(deadline) {
			return -1, fmt.Errorf("vsock connect cid=%d port=%d: %w", hostCID, ctlPort, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func writeFrame(fd int, f frame) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	_, err = syscall.Write(fd, append(b, '\n'))
	return err
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

// Package sandbox is the KVM/qemu sandbox library: create, start, exec into
// and destroy a VM. Its outbound traffic is filtered by netfilter rules on
// the VM's own tap device, matched against the same allowlist the egress
// proxy uses (egress/match).
//
// This is one half of what crackbox composes; egress is the other. Nothing
// here talks to a proxy, a registry or an admin API (spec 6/24).
package sandbox

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"

	"github.com/kronael/arizuko/sandbox/internal"
	"github.com/kronael/arizuko/substrate"
)

// Profile is what the KVM substrate can do: a VM whose disk image lives under
// the data dir until Destroy, into which Exec runs commands over SSH for as
// long as it is up, and whose outbound traffic netfilter filters on the tap
// by address — an allowlist entry that is an IP or a CIDR becomes a rule, a
// host name becomes nothing (internal/netfilter.go). Nothing in arizuko runs
// it; the declaration exists so the difference from Docker is a value, not a
// paragraph (spec 6/25 §3).
var Profile = substrate.Profile{
	Backend:     substrate.KVM,
	Persistence: substrate.ResidentDisk,
	Sessions:    true,
	Egress:      substrate.EgressAddress,
}

var instanceIDRegex = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,32}$`)

func validateInstanceID(id string) error {
	if !instanceIDRegex.MatchString(id) {
		return fmt.Errorf("instanceID must match ^[a-zA-Z0-9_-]{1,32}$, got: %q", id)
	}
	return nil
}

// Mount is a virtio-fs mount spec passed to Spawn.
type Mount struct {
	Host     string
	Guest    string
	ReadOnly bool
}

// VMConfig holds the configuration for a VM to be spawned.
type VMConfig struct {
	Image     string   // path to base qcow2; empty = default Alpine download
	Memory    string   // e.g. "2G"; empty = default
	CPUs      int      // 0 = default (2)
	Mounts    []Mount  // virtio-fs mounts
	AllowList []string // host allowlist enforced by netfilter on the VM's tap
}

// Handle is a reference to a running VM.
type Handle struct {
	ID string // VM id (matches metadata dir name)
	IP string // VM eth0 IP (post-DHCP)
}

// Host manages VMs within a single InstanceID namespace.
type Host struct {
	instanceID string
	dataDir    string
	mgr        *internal.Manager
}

// New constructs a Host. instanceID must match ^[a-zA-Z0-9_-]{1,32}$.
// dataDir is the root for all VM state; required.
func New(instanceID, dataDir string) (*Host, error) {
	if err := validateInstanceID(instanceID); err != nil {
		return nil, err
	}
	if dataDir == "" {
		return nil, errors.New("dataDir required")
	}
	mgr, err := internal.NewManager(instanceID, dataDir)
	if err != nil {
		return nil, err
	}
	return &Host{instanceID: instanceID, dataDir: dataDir, mgr: mgr}, nil
}

// Spawn creates and starts a VM and returns a Handle with the VM's IP.
// The allowlist becomes netfilter rules on the VM's tap device.
func (h *Host) Spawn(cfg VMConfig) (Handle, error) {
	mounts := make([]internal.Mount, len(cfg.Mounts))
	for i, m := range cfg.Mounts {
		mounts[i] = internal.Mount{Host: m.Host, Guest: m.Guest, ReadOnly: m.ReadOnly}
	}

	id, err := h.mgr.Create(internal.CreateOpts{
		Image:     cfg.Image,
		Memory:    cfg.Memory,
		CPUs:      cfg.CPUs,
		Mounts:    mounts,
		AllowList: cfg.AllowList,
	})
	if err != nil {
		return Handle{}, fmt.Errorf("create vm: %w", err)
	}

	if err := h.mgr.Start(id); err != nil {
		h.mgr.Destroy(id) //nolint:errcheck // rollback of a failed create; the create error is what the caller gets
		return Handle{}, fmt.Errorf("start vm: %w", err)
	}

	meta, err := h.mgr.Get(id)
	if err != nil || meta == nil {
		h.mgr.Destroy(id) //nolint:errcheck // rollback of a failed create; the create error is what the caller gets
		return Handle{}, fmt.Errorf("get vm meta after start: %w", err)
	}

	return Handle{ID: id, IP: meta.IP}, nil
}

// Stop shuts down QEMU and tears the VM's tap + netfilter rules down.
func (h *Host) Stop(handle Handle) error {
	meta, err := h.mgr.Get(handle.ID)
	if err != nil {
		return err
	}
	if meta == nil {
		return fmt.Errorf("vm not found: %s", handle.ID)
	}
	return h.mgr.Stop(handle.ID)
}

// Exec runs a command inside the VM via SSH. It captures stdout and stderr
// separately and returns the exit code. stdin may be nil.
func (h *Host) Exec(handle Handle, cmd []string, stdin io.Reader) (exitCode int, stdout []byte, stderr []byte, err error) {
	meta, err := h.mgr.Get(handle.ID)
	if err != nil || meta == nil {
		return -1, nil, nil, fmt.Errorf("get vm meta: %w", err)
	}

	args := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "ConnectTimeout=5",
	}
	if meta.SSHKeyPath != "" {
		args = append(args, "-i", meta.SSHKeyPath)
	}
	args = append(args, "root@"+handle.IP, "--")
	args = append(args, cmd...)

	c := exec.Command("ssh", args...)
	c.Stdin = stdin

	stdoutPipe, err := c.StdoutPipe()
	if err != nil {
		return -1, nil, nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderrPipe, err := c.StderrPipe()
	if err != nil {
		return -1, nil, nil, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := c.Start(); err != nil {
		return -1, nil, nil, fmt.Errorf("ssh start: %w", err)
	}

	// Read stdout and stderr concurrently to avoid pipe deadlocks.
	stderrDone := make(chan []byte, 1)
	go func() {
		var buf []byte
		tmp := make([]byte, 4096)
		for {
			n, err := stderrPipe.Read(tmp)
			if n > 0 {
				buf = append(buf, tmp[:n]...)
			}
			if err != nil {
				break
			}
		}
		stderrDone <- buf
	}()

	var stdoutBuf []byte
	tmp := make([]byte, 4096)
	for {
		n, err := stdoutPipe.Read(tmp)
		if n > 0 {
			stdoutBuf = append(stdoutBuf, tmp[:n]...)
		}
		if err != nil {
			break
		}
	}
	stderrBuf := <-stderrDone

	if err := c.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode(), stdoutBuf, stderrBuf, nil
		}
		return -1, stdoutBuf, stderrBuf, err
	}
	return c.ProcessState.ExitCode(), stdoutBuf, stderrBuf, nil
}

// List returns Handles for all Running VMs in this Host's namespace.
// State is read from disk via detectState — no in-RAM cache.
func (h *Host) List() ([]Handle, error) {
	entries, err := h.mgr.List()
	if err != nil {
		return nil, err
	}
	out := make([]Handle, len(entries))
	for i, e := range entries {
		out[i] = Handle{ID: e.ID, IP: e.IP}
	}
	return out, nil
}

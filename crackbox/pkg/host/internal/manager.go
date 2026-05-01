package internal

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// Manager handles VM lifecycle for a single instance namespace.
type Manager struct {
	instanceID string
	dataDir    string
	mu         sync.Mutex
}

// NewManager constructs a Manager. instanceID namespaces all VM dirs and
// network resources (bridge, taps, iptables chain). dataDir is the root
// for all persisted state.
func NewManager(instanceID, dataDir string) (*Manager, error) {
	if instanceID == "" {
		return nil, errors.New("instanceID required")
	}
	if dataDir == "" {
		return nil, errors.New("dataDir required")
	}
	return &Manager{instanceID: instanceID, dataDir: dataDir}, nil
}

// Mount is a virtio-fs mount spec.
type Mount struct {
	Host     string
	Guest    string
	ReadOnly bool
}

// CreateOpts are the options for creating a new VM.
type CreateOpts struct {
	Image       string
	Memory      string
	CPUs        int
	Mounts      []Mount
	EgressProxy string
	AllowList   []string
	Name        string
	SSHKeys     string
}

// ListEntry is returned by Manager.List().
type ListEntry struct {
	ID          string
	IP          string
	State       VMState
	EgressProxy string
	AllowList   []string
}

// Create allocates resources and persists initial Meta. Returns the new VM id.
// Does not start QEMU.
func (m *Manager) Create(opts CreateOpts) (string, error) {
	var id string

	err := m.acquireResourceLock(func() error {
		id = GenerateVMID()
		netIndex, err := m.nextNetIndex()
		if err != nil {
			return fmt.Errorf("allocate subnet: %w", err)
		}
		sshPort, err := m.nextSSHPort()
		if err != nil {
			return fmt.Errorf("allocate SSH port: %w", err)
		}

		name := opts.Name
		if name == "" {
			name = id[:8]
		}

		meta := &Meta{
			ID:          id,
			Name:        name,
			NetIndex:    netIndex,
			SSHPort:     sshPort,
			CreatedAt:   time.Now().UnixMilli(),
			SSHKeys:     opts.SSHKeys,
			EgressProxy: opts.EgressProxy,
			AllowList:   opts.AllowList,
		}

		if err := m.saveMeta(meta); err != nil {
			return fmt.Errorf("save meta: %w", err)
		}
		return nil
	})
	if err != nil {
		return "", err
	}

	// Generate per-VM SSH keypair for host→VM Exec access. Best-effort.
	keyPath := filepath.Join(m.vmDir(id), "id_ed25519")
	if err := generateSSHKey(keyPath); err != nil {
		log.Printf("vm %s: generate SSH key: %v (Exec will use default identity)", id[:8], err)
	} else {
		if meta, err := m.loadMeta(id); err == nil && meta != nil {
			meta.SSHKeyPath = keyPath
			if err := m.saveMeta(meta); err != nil {
				log.Printf("vm %s: save SSH key path: %v", id[:8], err)
			}
		}
	}

	return id, nil
}

func generateSSHKey(path string) error {
	out, err := exec.Command("ssh-keygen", "-t", "ed25519", "-f", path, "-N", "", "-q").CombinedOutput()
	if err != nil {
		return fmt.Errorf("ssh-keygen: %s: %w", out, err)
	}
	return nil
}

// Start provisions disk (if needed), sets up networking, starts QEMU, and
// discovers the VM IP via DHCP. On return, IP is persisted in Meta.
func (m *Manager) Start(id string) error {
	meta, err := m.loadMeta(id)
	if err != nil {
		return err
	}
	if meta == nil {
		return fmt.Errorf("vm not found: %s", id)
	}

	vmDir := m.vmDir(id)
	diskImage := filepath.Join(vmDir, "disk.qcow2")

	ctx := context.Background()

	// Convert Meta → VM for qemu/network helpers (which use *VM).
	vm := metaToVM(meta)

	if _, err := os.Stat(diskImage); os.IsNotExist(err) {
		if err := m.provisionOnce(ctx, vm); err != nil {
			return fmt.Errorf("provision: %w", err)
		}
	}

	if err := m.setupNetwork(vm); err != nil {
		return fmt.Errorf("setup network: %w", err)
	}

	if err := m.startQEMU(vm, vmDir); err != nil {
		m.teardownNetwork(vm)
		return fmt.Errorf("start qemu: %w", err)
	}

	// Discover IP — blocks until DHCP lease appears or times out.
	if !m.discoverVMIP(vm) {
		m.stopQEMU(ctx, vm) //nolint:errcheck
		return fmt.Errorf("vm %s: IP discovery timeout", id[:8])
	}

	// Persist discovered IP.
	meta.IP = vm.IP
	if err := m.saveMeta(meta); err != nil {
		log.Printf("vm %s: save meta after IP discovery: %v", id[:8], err)
	}

	// Restore iptables rules now that we have an IP.
	if err := m.RestoreNetworkRules(meta); err != nil {
		log.Printf("vm %s: restore network rules: %v", id[:8], err)
	}

	return nil
}

// Stop gracefully shuts down the QEMU process.
func (m *Manager) Stop(id string) error {
	meta, err := m.loadMeta(id)
	if err != nil {
		return err
	}
	if meta == nil {
		return fmt.Errorf("vm not found: %s", id)
	}

	vm := metaToVM(meta)
	vm.PID = m.readPID(m.vmDir(id))

	ctx := context.Background()
	if err := m.stopQEMU(ctx, vm); err != nil {
		return err
	}

	log.Printf("stopped vm %s (%s)", id[:8], meta.Name)
	return nil
}

// Destroy stops and soft-deletes a VM (marks DeletedAt, cleans iptables).
func (m *Manager) Destroy(id string) error {
	meta, err := m.loadMeta(id)
	if err != nil {
		return err
	}
	if meta == nil {
		return fmt.Errorf("vm not found: %s", id)
	}

	state := m.detectState(meta)
	if state == VMStateRunning || state == VMStateStarting {
		if err := m.Stop(id); err != nil {
			log.Printf("stop vm %s: %v", id, err)
		}
	}

	m.CleanupNetworkRules(meta)

	if err := m.markDeleted(id); err != nil {
		return err
	}

	log.Printf("deleted vm %s (%s)", id[:8], meta.Name)
	return nil
}

// Get returns the persisted Meta for a VM, or nil if not found.
func (m *Manager) Get(id string) (*Meta, error) {
	return m.loadMeta(id)
}

// List scans <dataDir>/<instanceID>/vms/*/meta.yaml on disk, runs
// detectState on each, and returns only Running VMs.
func (m *Manager) List() ([]ListEntry, error) {
	ids, err := m.listVMDirs()
	if err != nil {
		return nil, err
	}

	var out []ListEntry
	for _, id := range ids {
		meta, err := m.loadMeta(id)
		if err != nil || meta == nil {
			continue
		}
		if meta.DeletedAt != 0 {
			continue
		}
		if m.detectState(meta) != VMStateRunning {
			continue
		}
		out = append(out, ListEntry{
			ID:          meta.ID,
			IP:          meta.IP,
			State:       VMStateRunning,
			EgressProxy: meta.EgressProxy,
			AllowList:   meta.AllowList,
		})
	}
	return out, nil
}

// metaToVM creates a VM from persisted Meta for use with qemu/network helpers.
func metaToVM(meta *Meta) *VM {
	return &VM{
		ID:       meta.ID,
		Name:     meta.Name,
		NetIndex: meta.NetIndex,
		SSHPort:  meta.SSHPort,
		IP:       meta.IP,
		SSHKeys:  meta.SSHKeys,
	}
}

// sentinel errors
var errNoIP = errors.New("vm has no IP address")

type errNotFound struct{ id string }

func (e *errNotFound) Error() string { return fmt.Sprintf("vm not found: %s", e.id) }

type errIptables struct {
	msg string
	err error
}

func (e *errIptables) Error() string { return fmt.Sprintf("iptables: %s: %v", e.msg, e.err) }
func (e *errIptables) Unwrap() error { return e.err }

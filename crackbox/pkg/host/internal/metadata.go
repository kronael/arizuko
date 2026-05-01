package internal

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"gopkg.in/yaml.v3"
)

// Meta is the on-disk representation of VM metadata (YAML).
type Meta struct {
	ID          string   `yaml:"id"`
	Name        string   `yaml:"name"`
	NetIndex    int      `yaml:"net_index"`
	SSHPort     int      `yaml:"ssh_port,omitempty"`
	IP          string   `yaml:"ip,omitempty"`
	CreatedAt   int64    `yaml:"created_at"`
	DeletedAt   int64    `yaml:"deleted_at,omitempty"`
	SSHKeys     string   `yaml:"ssh_keys,omitempty"`
	SSHKeyPath  string   `yaml:"ssh_key_path,omitempty"`
	EgressProxy string   `yaml:"egress_proxy,omitempty"`
	AllowList   []string `yaml:"allowlist,omitempty"`
	AllowAll    bool     `yaml:"allow_all,omitempty"`
}

func (m *Manager) vmDir(id string) string {
	return filepath.Join(m.dataDir, m.instanceID, "vms", id)
}

func (m *Manager) metaPath(id string) string {
	return filepath.Join(m.vmDir(id), "meta.yaml")
}

func (m *Manager) saveMeta(meta *Meta) error {
	dir := m.vmDir(meta.ID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := yaml.Marshal(meta)
	if err != nil {
		return err
	}
	return os.WriteFile(m.metaPath(meta.ID), data, 0600)
}

func (m *Manager) loadMeta(id string) (*Meta, error) {
	data, err := os.ReadFile(m.metaPath(id))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var meta Meta
	if err := yaml.Unmarshal(data, &meta); err != nil {
		return nil, err
	}
	return &meta, nil
}

func (m *Manager) detectState(meta *Meta) VMState {
	if meta.DeletedAt > 0 {
		return VMStateDeleted
	}
	pid := m.readPID(m.vmDir(meta.ID))
	if pid > 0 && m.isRunning(pid) {
		return VMStateRunning
	}
	return VMStateStopped
}

func (m *Manager) readPID(vmDir string) int {
	data, err := os.ReadFile(filepath.Join(vmDir, "qemu.pid"))
	if err != nil {
		return 0
	}
	var pid int
	fmt.Sscanf(string(data), "%d", &pid)
	return pid
}

func (m *Manager) listVMDirs() ([]string, error) {
	base := filepath.Join(m.dataDir, m.instanceID, "vms")
	entries, err := os.ReadDir(base)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(base, e.Name(), "meta.yaml")); err == nil {
			ids = append(ids, e.Name())
		}
	}
	return ids, nil
}

func (m *Manager) listAll() ([]*Meta, error) {
	ids, err := m.listVMDirs()
	if err != nil {
		return nil, err
	}
	var metas []*Meta
	for _, id := range ids {
		meta, err := m.loadMeta(id)
		if err != nil || meta == nil {
			continue
		}
		if meta.DeletedAt != 0 {
			continue
		}
		metas = append(metas, meta)
	}
	sort.Slice(metas, func(i, j int) bool {
		return metas[i].CreatedAt > metas[j].CreatedAt
	})
	return metas, nil
}

func (m *Manager) nextNetIndex() (int, error) {
	ids, err := m.listVMDirs()
	if err != nil {
		return 0, err
	}
	used := make(map[int]bool)
	for _, id := range ids {
		meta, err := m.loadMeta(id)
		if err != nil || meta == nil {
			continue
		}
		if meta.DeletedAt == 0 {
			used[meta.NetIndex] = true
		}
	}
	for i := 1; i < MaxNetIndex; i++ {
		if !used[i] {
			return i, nil
		}
	}
	return 0, fmt.Errorf("no available subnet index")
}

func (m *Manager) nextSSHPort() (int, error) {
	ids, err := m.listVMDirs()
	if err != nil {
		return 0, err
	}
	const minSSHPort = 52301
	const maxSSHPort = 52399
	used := make(map[int]bool)
	for _, id := range ids {
		meta, err := m.loadMeta(id)
		if err != nil || meta == nil {
			continue
		}
		if meta.DeletedAt == 0 && meta.SSHPort > 0 {
			used[meta.SSHPort] = true
		}
	}
	for port := minSSHPort; port <= maxSSHPort; port++ {
		if !used[port] {
			return port, nil
		}
	}
	return 0, fmt.Errorf("no available SSH port")
}

func (m *Manager) markDeleted(id string) error {
	meta, err := m.loadMeta(id)
	if err != nil {
		return err
	}
	if meta == nil {
		return fmt.Errorf("vm not found")
	}
	meta.DeletedAt = time.Now().UnixMilli()
	return m.saveMeta(meta)
}

func (m *Manager) cleanupDeleted(retention time.Duration) error {
	ids, err := m.listVMDirs()
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-retention).UnixMilli()
	for _, id := range ids {
		meta, err := m.loadMeta(id)
		if err != nil || meta == nil {
			continue
		}
		if meta.DeletedAt > 0 && meta.DeletedAt < cutoff {
			os.RemoveAll(m.vmDir(id))
		}
	}
	return nil
}

func (m *Manager) isRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}

// acquireResourceLock acquires an exclusive flock for resource allocation,
// preventing TOCTOU races when concurrent Create() calls run.
func (m *Manager) acquireResourceLock(fn func() error) error {
	base := filepath.Join(m.dataDir, m.instanceID, "vms")
	if err := os.MkdirAll(base, 0755); err != nil {
		return fmt.Errorf("mkdir vms: %w", err)
	}
	lockPath := filepath.Join(base, ".resource.lock")
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return fmt.Errorf("open lock file: %w", err)
	}
	defer lockFile.Close()
	if err := unix.Flock(int(lockFile.Fd()), unix.LOCK_EX); err != nil {
		return fmt.Errorf("acquire lock: %w", err)
	}
	defer unix.Flock(int(lockFile.Fd()), unix.LOCK_UN) //nolint:errcheck
	return fn()
}

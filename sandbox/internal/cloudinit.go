package internal

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func (m *Manager) provisionOnce(ctx context.Context, vm *VM) error {
	vmDir := m.vmDir(vm.ID)

	baseImage, err := m.ensureBaseImage(ctx)
	if err != nil {
		return fmt.Errorf("ensure base image: %w", err)
	}

	agentBinary, err := m.ensureAgent(ctx)
	if err != nil {
		return fmt.Errorf("ensure agent: %w", err)
	}

	diskImage := filepath.Join(vmDir, "disk.qcow2")
	out, err := exec.CommandContext(ctx,
		"qemu-img", "create",
		"-f", "qcow2", "-b", baseImage, "-F", "qcow2", diskImage, "10G",
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("create disk: %s: %w", out, err)
	}

	if err := m.createCloudInit(vm, vmDir, agentBinary); err != nil {
		return fmt.Errorf("create cloud-init: %w", err)
	}

	return nil
}

func (m *Manager) cloudinitScriptPath() (string, error) {
	d, err := LibexecDir()
	if err != nil {
		return "", fmt.Errorf("libexec: %w", err)
	}
	return filepath.Join(d, "crackbox-cloudinit"), nil
}

func (m *Manager) createCloudInit(vm *VM, vmDir, agentBinary string) error {
	script, err := m.cloudinitScriptPath()
	if err != nil {
		return err
	}
	shareDir := filepath.Join(m.dataDir, m.instanceID, "share")
	if err := os.MkdirAll(shareDir, 0755); err != nil {
		return fmt.Errorf("mkdir share: %w", err)
	}
	token := GenerateToken()
	out, err := exec.Command(script, vm.ID, vm.Name, token, vmDir, agentBinary, shareDir).
		CombinedOutput()
	if err != nil {
		return fmt.Errorf("crackbox-cloudinit: %s: %w", out, err)
	}
	return nil
}

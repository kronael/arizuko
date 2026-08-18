package internal

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

func (m *Manager) ensureBaseImage(ctx context.Context) (string, error) {
	baseDir := filepath.Join(m.dataDir, m.instanceID, "base")
	imagePath := filepath.Join(baseDir, AlpineImageName)

	absPath, err := filepath.Abs(imagePath)
	if err != nil {
		return "", fmt.Errorf("resolve absolute path: %w", err)
	}
	if _, err := os.Stat(imagePath); err == nil {
		return absPath, nil
	}

	log.Printf("downloading base image: %s", AlpineImageURL)
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return "", err
	}

	tmpPath := imagePath + ".download"
	cmd := exec.CommandContext(ctx, "curl", "-L", "-o", tmpPath, AlpineImageURL)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("download: %w", err)
	}
	if err := os.Rename(tmpPath, imagePath); err != nil {
		os.Remove(tmpPath)
		return "", err
	}

	log.Printf("base image ready: %s", absPath)
	return absPath, nil
}

func (m *Manager) ensureAgent(_ context.Context) (string, error) {
	if _, err := os.Stat("/usr/local/bin/crackbox-agent"); err == nil {
		return "/usr/local/bin/crackbox-agent", nil
	}
	if absPath, err := filepath.Abs("agent/agent"); err == nil {
		if _, err := os.Stat(absPath); err == nil {
			return absPath, nil
		}
	}
	return "", fmt.Errorf("agent binary not found (install with 'make install')")
}

func (m *Manager) startQEMU(vm *VM, vmDir string) error {
	absDir, err := filepath.Abs(vmDir)
	if err != nil {
		return fmt.Errorf("resolve absolute path: %w", err)
	}
	diskImage := filepath.Join(absDir, "disk.qcow2")
	cloudInitISO := filepath.Join(absDir, "cloud-init.iso")
	pidFile := filepath.Join(absDir, "qemu.pid")
	monSocket := filepath.Join(absDir, "qemu.mon")
	serialLog := filepath.Join(absDir, "serial.log")
	tap := m.tapName(vm)
	macAddr := VMIDToMAC(vm.ID)

	cpus := VMCPUs
	mem := VMMemory

	args := []string{
		"-name", vm.Name,
		"-machine", "type=q35,accel=kvm",
		"-cpu", "Nehalem,+ssse3,+sse4.1,+sse4.2,+x2apic,-kvm_pv_eoi,-kvm_asyncpf,-kvm_steal_time",
		"-smp", cpus,
		"-m", mem,
		"-drive", fmt.Sprintf("file=%s,format=qcow2,if=virtio,cache=writethrough,aio=threads", diskImage),
		"-drive", fmt.Sprintf("file=%s,format=raw,if=virtio", cloudInitISO),
		"-netdev", fmt.Sprintf("tap,id=net0,ifname=%s,script=no,downscript=no", tap),
		"-device", fmt.Sprintf("virtio-net-pci,netdev=net0,mac=%s", macAddr),
		"-display", "none",
		"-serial", fmt.Sprintf("file:%s", serialLog),
		"-monitor", fmt.Sprintf("unix:%s,server,nowait", monSocket),
		"-pidfile", pidFile,
		"-daemonize",
	}

	out, err := exec.Command("qemu-system-x86_64", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("qemu: %s: %w", out, err)
	}

	pid, err := m.waitForPIDFile(pidFile)
	if err != nil {
		return fmt.Errorf("read pid: %w", err)
	}
	vm.PID = pid
	return nil
}

func (m *Manager) waitForPIDFile(path string) (int, error) {
	deadline := time.Now().Add(time.Duration(PIDDiscoveryTimeout) * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			var pid int
			if _, err := fmt.Sscanf(string(data), "%d", &pid); err == nil {
				return pid, nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return 0, fmt.Errorf("timeout waiting for pid file")
}

func (m *Manager) stopQEMU(ctx context.Context, vm *VM) error {
	vmDir := m.vmDir(vm.ID)
	monSocket := filepath.Join(vmDir, "qemu.mon")
	pidFile := filepath.Join(vmDir, "qemu.pid")

	conn, err := net.Dial("unix", monSocket)
	if err == nil {
		conn.Write([]byte("system_powerdown\n")) //nolint:errcheck
		conn.Close()

		timeout := time.After(time.Duration(ShutdownTimeout) * time.Second)
		tick := time.NewTicker(time.Second)
		defer tick.Stop()

	graceful:
		for {
			select {
			case <-ctx.Done():
				goto forceKill
			case <-timeout:
				goto forceKill
			case <-tick.C:
				if !m.isRunning(vm.PID) {
					break graceful
				}
			}
		}
		goto cleanup
	}

forceKill:
	if m.isRunning(vm.PID) {
		syscall.Kill(vm.PID, syscall.SIGKILL) //nolint:errcheck
	}

cleanup:
	m.teardownNetwork(vm)
	os.Remove(pidFile)
	os.Remove(monSocket)

	return nil
}

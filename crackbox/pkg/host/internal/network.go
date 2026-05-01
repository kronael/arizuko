package internal

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// H4 returns the 4-hex-char prefix for network resource names.
// = hex(sha256(instanceID)[:2])
func H4(instanceID string) string {
	h := sha256.Sum256([]byte(instanceID))
	return hex.EncodeToString(h[:2])
}

// BridgeName returns the bridge ifname for an instanceID.
// Format: "cbx-<H4>-br" (11 chars).
func BridgeName(instanceID string) string {
	return "cbx-" + H4(instanceID) + "-br"
}

// TapName returns the tap ifname for an instanceID + per-VM index.
// Format: "cbx-<H4>-t<NN>" (12 chars). idx must be 0..255.
func TapName(instanceID string, idx int) string {
	return fmt.Sprintf("cbx-%s-t%02x", H4(instanceID), idx)
}

// IPRangeFor returns the /24 CIDR for an instanceID.
// Format: "10.<H1>.<H2>.0/24" where H1,H2 = sha256(instanceID)[:2].
func IPRangeFor(instanceID string) string {
	h := sha256.Sum256([]byte(instanceID))
	return fmt.Sprintf("10.%d.%d.0/24", h[0], h[1])
}

func (m *Manager) tapScriptPath() string {
	d, err := LibexecDir()
	if err != nil {
		return "crackbox-tap"
	}
	return filepath.Join(d, "crackbox-tap")
}

func (m *Manager) tapName(vm *VM) string {
	return TapName(m.instanceID, vm.NetIndex)
}

func (m *Manager) bridgeName() string {
	return BridgeName(m.instanceID)
}

func (m *Manager) setupNetwork(vm *VM) error {
	tap := m.tapName(vm)
	bridge := m.bridgeName()
	args := []string{"up", tap, bridge}
	if vm.SSHPort > 0 && vm.IP != "" {
		args = append(args, fmt.Sprintf("%d", vm.SSHPort), vm.IP)
	}
	out, err := exec.Command(m.tapScriptPath(), args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("crackbox-tap up: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func (m *Manager) teardownNetwork(vm *VM) {
	tap := m.tapName(vm)
	args := []string{"down", tap}
	if vm.SSHPort > 0 && vm.IP != "" {
		args = append(args, fmt.Sprintf("%d", vm.SSHPort), vm.IP)
	}
	out, err := exec.Command(m.tapScriptPath(), args...).CombinedOutput()
	if err != nil {
		log.Printf("vm %s: crackbox-tap down: %s: %v",
			tap, strings.TrimSpace(string(out)), err)
	}
}

// discoverVMIP discovers VM IP from DHCP lease file.
// Returns true if IP was found within 30 seconds, false on timeout.
func (m *Manager) discoverVMIP(vm *VM) bool {
	macAddr := VMIDToMAC(vm.ID)
	leaseFile := "/var/lib/misc/dnsmasq-crackbox.leases"

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(leaseFile)
		if err != nil {
			time.Sleep(1 * time.Second)
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 3 {
				continue
			}
			if fields[1] == macAddr {
				vm.IP = fields[2]
				log.Printf("vm %s got IP %s via DHCP", vm.ID[:8], vm.IP)
				return true
			}
		}
		time.Sleep(1 * time.Second)
	}
	log.Printf("vm %s: timeout waiting for DHCP lease", vm.ID[:8])
	return false
}

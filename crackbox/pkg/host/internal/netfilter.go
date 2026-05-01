package internal

import (
	"log"
	"net"
	"os/exec"
	"regexp"
	"strings"
)

// domainRegex validates domain names (alphanumeric, hyphens, dots only).
var domainRegex = regexp.MustCompile(
	`^[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?` +
		`(\.[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?)*$`)

// looksLikeDomain returns true if the target appears to be a domain name.
func looksLikeDomain(target string) bool {
	if !strings.Contains(target, ".") {
		return false
	}
	if strings.Contains(target, "/") {
		return false
	}
	if net.ParseIP(target) != nil {
		return false
	}
	return domainRegex.MatchString(target)
}

// looksLikeIP returns true if the target is an IP or CIDR.
func looksLikeIP(target string) bool {
	if net.ParseIP(target) != nil {
		return true
	}
	_, _, err := net.ParseCIDR(target)
	return err == nil
}

// filterChain returns the iptables chain name for this manager's instance.
// Format: "cbx-<H4>" (8 chars).
func (m *Manager) filterChain() string {
	return "cbx-" + H4(m.instanceID)
}

// SetAllowAll enables or disables full internet access for a VM.
func (m *Manager) SetAllowAll(vmID string, allowAll bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	meta, err := m.loadMeta(vmID)
	if err != nil || meta == nil {
		return &errNotFound{vmID}
	}
	if meta.IP == "" {
		return errNoIP
	}

	chain := m.filterChain()
	if allowAll {
		if err := iptablesInsertIfNotExists(chain, "-s", meta.IP, "-j", "ACCEPT"); err != nil {
			return err
		}
		if err := iptablesInsertIfNotExists(chain, "-d", meta.IP, "-j", "ACCEPT"); err != nil {
			return err
		}
	} else {
		iptablesDelete(chain, "-s", meta.IP, "-j", "ACCEPT")
		iptablesDelete(chain, "-d", meta.IP, "-j", "ACCEPT")
	}

	meta.AllowAll = allowAll
	return m.saveMeta(meta)
}

// RestoreNetworkRules applies stored network rules for a VM (called on VM start).
func (m *Manager) RestoreNetworkRules(meta *Meta) error {
	if meta.IP == "" {
		return nil
	}
	if meta.AllowAll {
		if err := m.SetAllowAll(meta.ID, true); err != nil {
			return err
		}
	}
	for _, target := range meta.AllowList {
		if looksLikeIP(target) {
			if err := m.allowIP(meta, target); err != nil {
				return err
			}
		}
		// domains are handled by egred proxy — no iptables rules needed
	}
	return nil
}

// allowIP adds an IP/CIDR to iptables rules for the VM.
func (m *Manager) allowIP(meta *Meta, target string) error {
	if meta.IP == "" {
		return errNoIP
	}
	chain := m.filterChain()
	if err := iptablesInsertIfNotExists(chain, "-s", meta.IP, "-d", target, "-j", "ACCEPT"); err != nil {
		return err
	}
	return iptablesInsertIfNotExists(chain, "-d", meta.IP, "-s", target, "-j", "ACCEPT")
}

// CleanupNetworkRules removes all iptables rules for a VM (called on destroy).
func (m *Manager) CleanupNetworkRules(meta *Meta) {
	if meta.IP == "" {
		return
	}
	chain := m.filterChain()
	iptablesDelete(chain, "-s", meta.IP, "-j", "ACCEPT")
	iptablesDelete(chain, "-d", meta.IP, "-j", "ACCEPT")
	for _, target := range meta.AllowList {
		if looksLikeIP(target) {
			iptablesDelete(chain, "-s", meta.IP, "-d", target, "-j", "ACCEPT")
			iptablesDelete(chain, "-d", meta.IP, "-s", target, "-j", "ACCEPT")
		}
	}
}

func iptablesInsertIfNotExists(chain string, args ...string) error {
	checkArgs := append([]string{"-C", chain}, args...)
	if exec.Command("iptables", checkArgs...).Run() == nil {
		return nil
	}
	insertArgs := append([]string{"-I", chain, "1"}, args...)
	out, err := exec.Command("iptables", insertArgs...).CombinedOutput()
	if err != nil {
		return &errIptables{strings.TrimSpace(string(out)), err}
	}
	return nil
}

func iptablesDelete(chain string, args ...string) {
	deleteArgs := append([]string{"-D", chain}, args...)
	out, err := exec.Command("iptables", deleteArgs...).CombinedOutput()
	if err != nil {
		log.Printf("iptables delete (may be expected): %s", strings.TrimSpace(string(out)))
	}
}

package main

import (
	"net"
	"regexp"
	"strings"
	"sync"
)

// Ported from crackbox/internal/vm/netfilter.go and proxy.go.
// Domain validation + allowlist match logic, kept verbatim where possible.

var domainRegex = regexp.MustCompile(
	`^[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?` +
		`(\.[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?)*$`)

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

func looksLikeIP(target string) bool {
	if net.ParseIP(target) != nil {
		return true
	}
	_, _, err := net.ParseCIDR(target)
	return err == nil
}

// matchHost returns true if host is allowed by the given allowlist.
// Match rules: exact, subdomain, case-insensitive, trailing-dot stripped.
// IP entries in allowlist are skipped here (would be enforced by an IP-aware
// path; egred currently filters by host only).
func matchHost(allowlist []string, host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, allowed := range allowlist {
		if looksLikeIP(allowed) {
			continue
		}
		allowed = strings.ToLower(allowed)
		if host == allowed {
			return true
		}
		if strings.HasSuffix(host, "."+allowed) {
			return true
		}
	}
	return false
}

// Allowlist is a per-source-IP map of approved hosts. Folder→list resolution
// happens in gated; egred only sees IP→list pairs registered at container
// spawn.
type Allowlist struct {
	mu  sync.RWMutex
	ips map[string]ipEntry
}

type ipEntry struct {
	folder    string
	allowlist []string
}

func NewAllowlist() *Allowlist {
	return &Allowlist{ips: map[string]ipEntry{}}
}

func (a *Allowlist) Set(ip, folder string, list []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	cp := make([]string, len(list))
	copy(cp, list)
	a.ips[ip] = ipEntry{folder: folder, allowlist: cp}
}

func (a *Allowlist) Remove(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.ips, ip)
}

func (a *Allowlist) Lookup(ip string) (folder string, list []string, ok bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	e, ok := a.ips[ip]
	if !ok {
		return "", nil, false
	}
	return e.folder, e.allowlist, true
}

func (a *Allowlist) Allow(ip, host string) (folder string, ok bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	e, found := a.ips[ip]
	if !found {
		return "", false
	}
	return e.folder, matchHost(e.allowlist, host)
}

func (a *Allowlist) Snapshot() map[string]ipEntry {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make(map[string]ipEntry, len(a.ips))
	for k, v := range a.ips {
		cp := make([]string, len(v.allowlist))
		copy(cp, v.allowlist)
		out[k] = ipEntry{folder: v.folder, allowlist: cp}
	}
	return out
}

// Package match holds the pure host-matching primitives used by the proxy.
// Ported from crackbox/internal/vm/{proxy,netfilter}.go (the original
// crackbox VM project). No state, no I/O.
package match

import (
	"net"
	"regexp"
	"strings"
)

var domainRegex = regexp.MustCompile(
	`^[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?` +
		`(\.[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?)*$`)

// LooksLikeDomain reports whether target is a syntactically valid domain.
// Domains have dots, no slashes, and don't parse as IP addresses.
func LooksLikeDomain(target string) bool {
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

// LooksLikeIP reports whether target is an IP address or CIDR range.
func LooksLikeIP(target string) bool {
	if net.ParseIP(target) != nil {
		return true
	}
	_, _, err := net.ParseCIDR(target)
	return err == nil
}

// Host returns true if host is allowed by the given allowlist.
// Match rules: exact, subdomain, case-insensitive, trailing-dot stripped.
// IP entries in the allowlist are skipped — host filtering is name-based.
func Host(allowlist []string, host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, allowed := range allowlist {
		if LooksLikeIP(allowed) {
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

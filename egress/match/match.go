// Package match holds the pure host-matching primitives used by the egress proxy and
// the sandbox netfilter rules. No state, no I/O.
//
// The package owns the rule grammar: every boundary that accepts a rule validates with
// ValidEntry/ValidateLists, so a rule that can never match is refused where it is
// written, not silently carried to match time (spec 6/25 1b).
package match

import (
	"fmt"
	"net"
	"regexp"
	"strings"
)

var domainRegex = regexp.MustCompile(
	`^[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?` +
		`(\.[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?)*$`)

// LooksLikeIP reports whether target is an IP address or CIDR range.
func LooksLikeIP(target string) bool {
	if net.ParseIP(target) != nil {
		return true
	}
	_, _, err := net.ParseCIDR(target)
	return err == nil
}

// NormalizeEntry returns entry as the matcher compares it: lowercased,
// trailing dot stripped, one leading "*." glob stripped. A bare domain
// already covers its subdomains, so the glob and its apex are one rule —
// write paths store the normalized form so a rule added as *.example.com
// can be removed as example.com.
func NormalizeEntry(entry string) string {
	entry = strings.ToLower(strings.TrimSuffix(entry, "."))
	return strings.TrimPrefix(entry, "*.")
}

// Host returns true if host matches an entry in list: exact or subdomain,
// case-insensitive, trailing dots stripped, "*.x" equivalent to "x". Entries are
// assumed validated, so Host carries no special cases for entries that cannot name a
// host — no magic "*" and no silent IP skip.
//
// A single-label entry matches EXACTLY and never as a suffix. `localhost` and a
// container service name are legitimate dotless hosts, but so is `com`, and the
// subdomain arm would make that one entry allow every .com host on the internet
// — an allowlist row that reads as one host and grants the whole TLD. sloth's
// main/trading held eight of them (BUGS L209).
func Host(list []string, host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, entry := range list {
		entry = NormalizeEntry(entry)
		if host == entry {
			return true
		}
		if strings.Contains(entry, ".") && strings.HasSuffix(host, "."+entry) {
			return true
		}
	}
	return false
}

// Allowed decides host against the full policy. Deny is evaluated FIRST, so a deny
// entry carves an exception out of any allow — including allowAll — and a denied name
// stays denied for its whole subtree. allowAll is the explicit mode that replaced the
// magic "*" list entry.
func Allowed(allow, deny []string, allowAll bool, host string) bool {
	if Host(deny, host) {
		return false
	}
	return allowAll || Host(allow, host)
}

// ValidEntry explains why entry can never match a proxied host, or returns nil for a
// rule that works. Valid: a bare domain or single label, optionally with one leading
// "*." glob. Everything an operator habitually writes that would silently never match
// — a URL, a host:port, a path, credentials, an IP — names its defect instead of
// reading as protection (spec 6/25 1b).
func ValidEntry(entry string) error {
	if entry == "" {
		return fmt.Errorf("empty entry")
	}
	if entry == "*" {
		return fmt.Errorf(`"*" is not a list entry: allowing every host is the explicit allow-all mode`)
	}
	if strings.Contains(entry, "://") {
		return fmt.Errorf("%q: no scheme — write the bare host", entry)
	}
	if strings.Contains(entry, "@") {
		return fmt.Errorf("%q: no credentials — write the bare host", entry)
	}
	if LooksLikeIP(entry) {
		return fmt.Errorf("%q: an IP entry can never match — filtering is by NAME, and blocked address classes are the address check's job", entry)
	}
	if strings.Contains(entry, "/") {
		return fmt.Errorf("%q: no path — the proxy sees only the host", entry)
	}
	if strings.Contains(entry, ":") {
		return fmt.Errorf("%q: no port — a rule matches every port of its host", entry)
	}
	rest := strings.TrimPrefix(entry, "*.")
	if strings.Contains(rest, "*") {
		return fmt.Errorf("%q: the only wildcard is one leading *. — and a bare domain already covers its subdomains", entry)
	}
	if !domainRegex.MatchString(strings.ToLower(strings.TrimSuffix(rest, "."))) {
		return fmt.Errorf("%q: not a host name", entry)
	}
	return nil
}

// ValidateLists checks every allow and deny entry (ValidEntry), refuses a
// duplicate entry within a list, and refuses a host present in BOTH lists:
// deny wins, so the allow twin is dead and one of the two is a mistake.
// Duplicates compare normalized, so "Example.com." and "*.example.com"
// collide with "example.com".
func ValidateLists(allow, deny []string) error {
	seen := make(map[string]string, len(allow)+len(deny))
	check := func(list []string, name string) error {
		for _, e := range list {
			if err := ValidEntry(e); err != nil {
				return fmt.Errorf("%s list: %w", name, err)
			}
			n := NormalizeEntry(e)
			if prev, dup := seen[n]; dup {
				if prev == name {
					return fmt.Errorf("%s list: %q is listed twice", name, n)
				}
				return fmt.Errorf("%q is in both allow and deny: deny wins, so the allow entry is dead — keep one", n)
			}
			seen[n] = name
		}
		return nil
	}
	if err := check(allow, "allow"); err != nil {
		return err
	}
	return check(deny, "deny")
}

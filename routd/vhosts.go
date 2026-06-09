package routd

// vhosts.go parses WEB_VHOST_ALIASES for the get_web_presence reporting surface
// (spec 5/V). routd never serves vhosts — proxyd does — but it reports a
// folder's aliased canonical host, so it parses the same env. The map is
// host→folder; each side is validated (host syntax + folder syntax) and
// malformed entries are skipped loudly, never silently accepted.

import (
	"log/slog"
	"strings"

	"github.com/kronael/arizuko/groupfolder"
)

// ParseVhostAliases parses `host=world,host2=world2` into a host→folder map.
// Hosts are lowercased. An entry is skipped (with a warn) when it lacks `=`,
// has an empty side, has an invalid hostname, or names an invalid folder —
// silent acceptance would let a typo'd alias misreport a folder's presence.
func ParseVhostAliases(s string) map[string]string {
	m := map[string]string{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		host, world, ok := strings.Cut(part, "=")
		host = strings.ToLower(strings.TrimSpace(host))
		world = strings.TrimSpace(world)
		if !ok || host == "" || world == "" {
			slog.Warn("WEB_VHOST_ALIASES: skipping malformed entry", "entry", part)
			continue
		}
		if !validVhostName(host) {
			slog.Warn("WEB_VHOST_ALIASES: skipping invalid hostname", "host", host)
			continue
		}
		if !groupfolder.IsValidFolder(world) {
			slog.Warn("WEB_VHOST_ALIASES: skipping invalid world folder", "world", world, "host", host)
			continue
		}
		m[host] = world
	}
	return m
}

// validVhostName mirrors ipc.validHostname (unexported): a hostname is letters,
// digits, dot, dash, colon, length-bounded. Kept routd-local to avoid widening
// ipc's surface for one reporting tool.
func validVhostName(h string) bool {
	if h == "" || len(h) > 253 {
		return false
	}
	for _, r := range h {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '.' || r == '-' || r == ':':
		default:
			return false
		}
	}
	return true
}

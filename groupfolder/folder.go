package groupfolder

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
)

func IsRoot(folder string) bool {
	return !strings.Contains(folder, "/")
}

// ParentOf returns the immediate parent folder of folder, or "" if folder is root.
// "main/content" → "main", "main" → "".
func ParentOf(folder string) string {
	i := strings.LastIndex(folder, "/")
	if i < 0 {
		return ""
	}
	return folder[:i]
}

// NameOf returns the last path segment of folder, used as the display label.
// "main/content" → "content", "main" → "main".
func NameOf(folder string) string {
	i := strings.LastIndex(folder, "/")
	if i < 0 {
		return folder
	}
	return folder[i+1:]
}

var reservedFolders = map[string]bool{"share": true, "*": true, "**": true}

type Resolver struct {
	GroupsDir string
	IpcDir    string
}

func IsValidFolder(folder string) bool { return isValidFolder(folder) }

func isValidFolder(folder string) bool {
	if folder == "" || folder != strings.TrimSpace(folder) {
		return false
	}
	if strings.Contains(folder, "..") || strings.ContainsAny(folder, "\\\x00") {
		return false
	}
	for _, seg := range strings.Split(folder, "/") {
		if seg == "" || len(seg) > 128 {
			return false
		}
		if reservedFolders[strings.ToLower(seg)] {
			return false
		}
	}
	return true
}

func ensureWithinBase(base, resolved string) error {
	rel, err := filepath.Rel(base, resolved)
	if err != nil {
		return fmt.Errorf("path escapes base directory: %s", resolved)
	}
	if strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return fmt.Errorf("path escapes base directory: %s", resolved)
	}
	return nil
}

func (r *Resolver) GroupPath(folder string) (string, error) {
	return resolve(r.GroupsDir, folder)
}

func (r *Resolver) IpcPath(folder string) (string, error) {
	return resolve(r.IpcDir, folder)
}

// JidFolder extracts the owner folder from a route-token / web-chat JID by
// prefix grammar alone. web:<folder>[/<suffix>] → the whole rest (the folder
// may itself contain "/", e.g. acme/eng). hook:<folder>/<source>[/...] →
// everything up to the last segment. Returns "" for any other scheme; callers
// that must resolve non-web/hook JIDs go through the routes table.
func JidFolder(jid string) string {
	if rest, ok := strings.CutPrefix(jid, "web:"); ok {
		return rest
	}
	if rest, ok := strings.CutPrefix(jid, "hook:"); ok {
		if i := strings.LastIndexByte(rest, '/'); i > 0 {
			return rest[:i]
		}
		return rest
	}
	return ""
}

// ParseVhostAliases parses `host=world,host2=world2` into a host→world map.
// Hosts are lowercased. An entry is skipped (with a warn) when it lacks `=`,
// has an empty side, has an invalid hostname, or names an invalid world —
// silent acceptance would let a typo'd alias 302 traffic to /pub/<garbage>/.
// Used only where the host label ≠ world name (marinade: fab.krons.cx→atlas);
// the common case is derived from HOSTING_DOMAIN and needs no entry.
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
		if !ValidVhostName(host) {
			slog.Warn("WEB_VHOST_ALIASES: skipping invalid hostname", "host", host)
			continue
		}
		if !IsValidFolder(world) {
			slog.Warn("WEB_VHOST_ALIASES: skipping invalid world folder", "world", world, "host", host)
			continue
		}
		m[host] = world
	}
	return m
}

// ValidVhostName accepts a hostname: letters, digits, dot, dash, colon,
// length-bounded. Mirrors ipc.validHostname (kept here; ipc's is unexported).
func ValidVhostName(h string) bool {
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

// IPC subdirectory conventions.
func IpcInputDir(ipcDir string) string          { return filepath.Join(ipcDir, "input") }
func IpcSocket(ipcDir string) string            { return filepath.Join(ipcDir, "gated.sock") }
func GroupMediaDir(groupDir, day string) string { return filepath.Join(groupDir, "media", day) }

func resolve(base, folder string) (string, error) {
	if !isValidFolder(folder) {
		return "", fmt.Errorf("invalid group folder %q", folder)
	}
	p := filepath.Join(base, folder)
	if err := ensureWithinBase(base, p); err != nil {
		return "", err
	}
	return p, nil
}

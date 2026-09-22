package groupfolder

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
)

// Contains reports whether `child` is `parent` or lives under it. This is the
// FOLDER-CONTAINMENT predicate the whole system tests before letting one folder
// reach another's rows, and it was hand-written at 15 sites — three named local
// helpers (authd folderWithin, routd routeTargetWithin, routd descendant) plus
// twelve inline `x == f || strings.HasPrefix(x, f+"/")` copies. Fifteen copies
// of a security predicate is fifteen chances for one to drift.
//
// The "/" suffix is what makes it containment rather than a prefix test:
// without it "acme" would contain "acmecorp".
//
// An empty parent contains nothing. Callers that mean "no constraint" (the
// operator/service sentinel, a "**" scope) must say so themselves — silently
// treating "" as "everything" here is how a missing folder becomes root.
func Contains(parent, child string) bool { return ContainsSep(parent, child, "/") }

// ContainsSep is Contains with the separator named, and it is the Go half of
// the one containment predicate, and store/groups.go renders the same rule as a
// statement, and every read that answers it in memory answers it here.
//
// The separator is a parameter because a folder opens its children with "/" and
// its topics with "#", and `routes.target` carries both — so a subtree read over
// that column asks the question twice, once per separator (store/routes.go).
//
// An empty parent contains nothing, for the reason Contains states.
func ContainsSep(parent, child, sep string) bool {
	if parent == "" {
		return false
	}
	return child == parent || strings.HasPrefix(child, parent+sep)
}

// ParentOf returns the immediate parent folder of folder, or "" if folder is root.
// "main/content" → "main", "main" → "".
func ParentOf(folder string) string {
	parent, _, found := strings.CutLast(folder, "/")
	if !found {
		return ""
	}
	return parent
}

// NameOf returns the last path segment of folder, used as the display label.
// "main/content" → "content", "main" → "main".
func NameOf(folder string) string {
	_, name, found := strings.CutLast(folder, "/")
	if !found {
		return folder
	}
	return name
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
	for seg := range strings.SplitSeq(folder, "/") {
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

// WebJID is the chat JID a folder's web surface writes into, and the write half
// of the grammar JidFolder reads. `WebJID(f)` and `JidFolder` are inverses for
// every folder, which is the property the test beside them pins.
//
// It exists because seven call sites spelled `"web:" + folder` themselves
// across four packages (BUGS L148). Two spellings of one grammar is how the two
// drift, and this grammar has drifted before: a `"web:"+folder` rebuild on the
// read side sent a suffixed chat's frames to a key nobody held (BUGS L30).
func WebJID(folder string) string { return "web:" + folder }

// HookJID is the chat JID a folder's webhook source delivers into, the write
// half of the hook grammar JidFolder reads: JidFolder(HookJID(f, s)) == f for a
// single-segment source. A mint suffix is appended by the caller, as for WebJID.
func HookJID(folder, source string) string { return "hook:" + folder + "/" + source }

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
	for part := range strings.SplitSeq(s, ",") {
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
// length-bounded. It is the canonical hostname validator.
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

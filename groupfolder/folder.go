package groupfolder

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"unicode/utf8"
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
func Contains(parent, child string) bool {
	if parent == "" {
		return false
	}
	return child == parent || strings.HasPrefix(child, parent+"/")
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

// SQLSubtree renders the SQL twin of Contains: a WHERE fragment matching `col`
// for `folder` itself plus everything below it, separated by sep ("/" between
// folders, "#" before a route fragment). Pair it with SubtreeArgs, which binds
// the three placeholders in order.
//
// It exists because the cascades hand-wrote `col = ? OR col LIKE ?||'/%'` at
// five sites, and LIKE is the wrong predicate for containment. `_` and `%` are
// wildcards on every engine: measured on sqlite 3.51.2 and PostgreSQL 17.11,
// `'aXb' LIKE 'a_b'` is true on both, so deleting folder `a_b` also deleted
// `axb`'s rows. Case folding is an engine difference, not a general fact —
// sqlite's LIKE folds ASCII and Postgres's does not, so `'Coach' LIKE 'coach%'`
// is true on sqlite and false on Postgres. A live instance HAS carried a
// `Coach`/`coach` pair (BUGS L66), and it leaked on sqlite only. substr
// comparison is exact, case-sensitive and has no metacharacters at all, which
// is why it is the predicate on every engine.
func SQLSubtree(col, sep string) string {
	return "(" + col + " = ? OR substr(" + col + ", 1, ?) = ?)"
}

// SubtreeArgs returns the three bind values SQLSubtree expects. The length is a
// RUNE count because sqlite's substr() counts characters, not bytes.
func SubtreeArgs(folder, sep string) []any {
	prefix := folder + sep
	return []any{folder, utf8.RuneCountInString(prefix), prefix}
}

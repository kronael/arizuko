package routd

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
)

var jidSanitizeRe = regexp.MustCompile(`[^a-z0-9_\-/]`)

// escOriginRe extracts the escalation origin (folder + jid) from a delegated
// prompt's <escalation_origin/> tag. When present, the child's reply routes
// back to that worker jid instead of the original chat (mirrors gateway).
var escOriginRe = regexp.MustCompile(`<escalation_origin\s[^/]*folder="([^"]+)"[^/]*jid="([^"]+)"[^/]*/>`)

// escalationWorker returns the escalation-origin return address carried in a
// delegated prompt, or "" when the prompt is a plain delegation. Faithful port
// of gateway.escalationWorker — including its m[1] (the folder capture group,
// not jid); replicating exactly so routd and gated behave identically.
func escalationWorker(prompt string) string {
	m := escOriginRe.FindStringSubmatch(prompt)
	if m == nil {
		return ""
	}
	return m[1]
}

func spawnFolderName(parentFolder, childJID string) string {
	s := strings.ToLower(strings.ReplaceAll(childJID, ":", "_"))
	s = jidSanitizeRe.ReplaceAllString(s, "")
	return parentFolder + "/" + s
}

// spawnFromPrototype materializes a child group under parentFolder/<sanitized
// childJID> by copying the parent's prototype/ dir, then registers the group +
// a room-matched route. Port of gateway.spawnFromPrototype. Honors the
// parent's max_children gate and rolls the group row back if the route insert
// fails so a route-less orphan is never left behind.
func (l *Loop) spawnFromPrototype(parentFolder, childJID string) (core.Group, error) {
	protoDir := filepath.Join(l.groupsDir, parentFolder, "prototype")
	if _, err := os.Stat(protoDir); err != nil {
		return core.Group{}, fmt.Errorf("no prototype dir: %w", err)
	}

	if parent, ok := l.db.GroupByFolder(parentFolder); ok {
		if err := auth.CheckSpawnAllowed(parent, l.db.AllGroups()); err != nil {
			return core.Group{}, err
		}
	}

	childFolder := spawnFolderName(parentFolder, childJID)
	childDir := filepath.Join(l.groupsDir, childFolder)
	if err := chanlib.CopyDirNoSymlinks(protoDir, childDir); err != nil {
		return core.Group{}, fmt.Errorf("copy prototype: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(childDir, "logs"), 0o755); err != nil {
		return core.Group{}, err
	}

	child := core.Group{Folder: childFolder, AddedAt: time.Now().UTC()}
	if err := l.db.PutGroup(child); err != nil {
		return core.Group{}, err
	}
	match := "room=" + core.JidRoom(childJID)
	if _, err := l.db.AddRoute(core.Route{Seq: 0, Match: match, Target: childFolder}); err != nil {
		_ = l.db.DeleteGroup(childFolder)
		return core.Group{}, fmt.Errorf("add route for %s: %w", childFolder, err)
	}
	return child, nil
}

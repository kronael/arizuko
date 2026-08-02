package auth

import (
	"fmt"
	"strings"

	"github.com/kronael/arizuko/core"
)

// Identity is a folder's non-elevated authz coordinate. 5/33 decision 2: the path
// carries ZERO authorization (no tier, no world rank) — only its own name and the
// root predicate. Authority is the caller's acl rows (auth.Authorize); containment
// is a scope-glob on the target.
type Identity struct {
	Folder string
	// IsRoot is the explicit root predicate (5/33 decision 1: root is a grant/
	// elevation, not a folder position). The empty "" folder is the operator/service
	// sentinel; a per-turn /root elevation sets it explicitly on the resolved id.
	IsRoot bool
}

// Resolve returns the non-elevated identity of a folder. The empty "" folder is the
// operator/service sentinel (REST + service faces carry it: no folder = act on
// anything → root). Every NAMED folder resolves to a plain, non-root identity;
// authority comes from its acl rows, never its depth (5/33: tiers dissolved).
func Resolve(folder string) Identity {
	return Identity{Folder: folder, IsRoot: folder == ""}
}

func WorldOf(folder string) string {
	if before, _, ok := strings.Cut(folder, "/"); ok {
		return before
	}
	return folder
}

func isInWorld(a, b string) bool {
	return WorldOf(a) == WorldOf(b)
}

func IsDirectChild(parent, child string) bool {
	if !strings.HasPrefix(child, parent+"/") {
		return false
	}
	return !strings.Contains(child[len(parent)+1:], "/")
}

// CheckSpawnAllowed: MaxChildren<0 = unlimited, 0 = disabled.
func CheckSpawnAllowed(parent core.Group, groups map[string]core.Group) error {
	if parent.Config.MaxChildren < 0 {
		return nil
	}
	if parent.Config.MaxChildren == 0 {
		return fmt.Errorf("spawning disabled (max_children=0)")
	}
	n := 0
	for _, g := range groups {
		if IsDirectChild(parent.Folder, g.Folder) {
			n++
		}
	}
	if n >= parent.Config.MaxChildren {
		return fmt.Errorf("max_children limit reached (%d)", parent.Config.MaxChildren)
	}
	return nil
}

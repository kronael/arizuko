package auth

import (
	"fmt"
	"strings"

	"github.com/kronael/arizuko/core"
)

type Identity struct {
	Folder string
	Tier   int
	World  string
}

// Resolve returns the NON-elevated identity of a folder. Tier 0 is reserved for
// root: an operator /root elevation (an explicit Identity{Tier: 0}) OR the empty
// folder "" — the operator/service sentinel the REST + service faces carry (no
// folder = act on anything). A bare, NAMED top-level world ("main") resolves to
// tier 1, not 0, so a tenant folder no longer picks up the tier-0 `*` grant by
// depth (grants.DeriveRules). The floor is what demotes a named world; "" stays
// the operator identity.
func Resolve(folder string) Identity {
	tier := min(strings.Count(folder, "/"), 3)
	if folder != "" && tier < 1 {
		tier = 1
	}
	return Identity{
		Folder: folder,
		Tier:   tier,
		World:  WorldOf(folder),
	}
}

func WorldOf(folder string) string {
	if idx := strings.Index(folder, "/"); idx != -1 {
		return folder[:idx]
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

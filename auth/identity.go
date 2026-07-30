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
	// IsRoot is the explicit root predicate (spec 4/R decision 1: root is a grant/
	// elevation, not a folder position). Today it is set == (Tier==0) so the ~10
	// root-bypass sites read `id.IsRoot` instead of `id.Tier==0` — behavior is
	// identical now, but when tier is removed only the SETTING of IsRoot changes
	// (to "holds the root grant"), not the call sites.
	IsRoot bool
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
		IsRoot: tier == 0, // "" sentinel resolves root; a named world floors to 1
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

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

func Resolve(folder string) Identity {
	return Identity{
		Folder: folder,
		Tier:   min(strings.Count(folder, "/"), 3),
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

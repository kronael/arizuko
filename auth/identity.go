package auth

import "strings"

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

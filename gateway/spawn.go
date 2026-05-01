package gateway

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/onvos/arizuko/auth"
	"github.com/onvos/arizuko/chanlib"
	"github.com/onvos/arizuko/core"
)

var jidSanitizeRe = regexp.MustCompile(`[^a-z0-9_\-/]`)

func spawnFolderName(parentFolder, childJID string) string {
	s := strings.ToLower(strings.ReplaceAll(childJID, ":", "_"))
	s = jidSanitizeRe.ReplaceAllString(s, "")
	return parentFolder + "/" + s
}

func (g *Gateway) spawnFromPrototype(parentFolder, childJID string) (core.Group, error) {
	protoDir := filepath.Join(g.cfg.GroupsDir, parentFolder, "prototype")
	if _, err := os.Stat(protoDir); err != nil {
		return core.Group{}, fmt.Errorf("no prototype dir: %w", err)
	}

	if parent, ok := g.store.GroupByFolder(parentFolder); ok {
		if err := auth.CheckSpawnAllowed(parent, g.store.AllGroups()); err != nil {
			return core.Group{}, err
		}
	}

	childFolder := spawnFolderName(parentFolder, childJID)
	childDir := filepath.Join(g.cfg.GroupsDir, childFolder)
	if err := chanlib.CopyDirNoSymlinks(protoDir, childDir); err != nil {
		return core.Group{}, fmt.Errorf("copy prototype: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(childDir, "logs"), 0o755); err != nil {
		return core.Group{}, err
	}

	child := core.Group{
		Name:    childJID,
		Folder:  childFolder,
		Parent:  parentFolder,
		AddedAt: time.Now(),
	}
	if err := g.store.PutGroup(child); err != nil {
		return core.Group{}, err
	}
	match := "room=" + core.JidRoom(childJID)
	g.store.AddRoute(core.Route{Seq: 0, Match: match, Target: childFolder})
	return child, nil
}

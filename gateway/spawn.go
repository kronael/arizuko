package gateway

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/onvos/arizuko/auth"
	"github.com/onvos/arizuko/core"
)

var jidSanitizeRe = regexp.MustCompile(`[^a-z0-9_\-/]`)

func spawnFolderName(parentFolder, childJID string) string {
	s := strings.ToLower(strings.ReplaceAll(childJID, ":", "_"))
	s = jidSanitizeRe.ReplaceAllString(s, "")
	return parentFolder + "/" + s
}

func (g *Gateway) spawnFromPrototype(parentJID, parentFolder, childJID string) (core.Group, error) {
	protoDir := filepath.Join(g.cfg.GroupsDir, parentFolder, "prototype")
	if _, err := os.Stat(protoDir); err != nil {
		return core.Group{}, fmt.Errorf("no prototype dir: %w", err)
	}

	g.mu.Lock()
	if parent, ok := g.groups[parentJID]; ok && parent.Config.MaxChildren >= 0 {
		if parent.Config.MaxChildren == 0 {
			g.mu.Unlock()
			return core.Group{}, fmt.Errorf("spawning disabled (max_children=0)")
		}
		n := 0
		for _, gr := range g.groups {
			if auth.IsDirectChild(parentFolder, gr.Folder) {
				n++
			}
		}
		if n >= parent.Config.MaxChildren {
			g.mu.Unlock()
			return core.Group{}, fmt.Errorf("max_children limit reached (%d)", parent.Config.MaxChildren)
		}
	}
	g.mu.Unlock()

	childFolder := spawnFolderName(parentFolder, childJID)
	childDir := filepath.Join(g.cfg.GroupsDir, childFolder)
	if err := copyDirNoSymlinks(protoDir, childDir); err != nil {
		return core.Group{}, fmt.Errorf("copy prototype: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(childDir, "logs"), 0o755); err != nil {
		return core.Group{}, err
	}

	child := core.Group{
		JID:     childJID,
		Name:    childJID,
		Folder:  childFolder,
		Parent:  parentFolder,
		AddedAt: time.Now(),
		State:   "active",
	}
	if err := g.store.PutGroup(childJID, child); err != nil {
		return core.Group{}, err
	}
	g.mu.Lock()
	g.groups[childJID] = child
	g.mu.Unlock()
	return child, nil
}

func copyDirNoSymlinks(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target)
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	_, cpErr := io.Copy(out, in)
	if closeErr := out.Close(); cpErr == nil {
		cpErr = closeErr
	}
	return cpErr
}

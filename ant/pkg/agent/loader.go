// Package agent loads ant-folders. An ant-folder is a directory holding
// the agent's identity, skills, memory, secrets, MCP wiring, and
// workspace. Layout matches the standalone-ant spec; absent files are
// fine (a fresh folder has only workspace/).
package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrNotFound is returned when the supplied path does not point at a
// directory.
var ErrNotFound = errors.New("ant folder not found")

// Folder is the resolved absolute paths to every well-known location
// inside an ant-folder. Paths are returned even when the file/dir
// does not exist on disk yet — the loader's job is layout, not
// presence checks. Callers verify presence per their own contract.
type Folder struct {
	Root      string
	Soul      string
	ClaudeMD  string
	Skills    string
	Diary     string
	Secrets   string
	MCP       string
	Workspace string
}

// LoadFolder validates that path is a directory and returns the
// resolved layout. ErrNotFound wraps any underlying stat error if the
// path does not exist or is not a directory.
func LoadFolder(path string) (*Folder, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %w", path, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, abs)
		}
		return nil, fmt.Errorf("stat %s: %w", abs, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: %s is not a directory", ErrNotFound, abs)
	}
	return &Folder{
		Root:      abs,
		Soul:      filepath.Join(abs, "SOUL.md"),
		ClaudeMD:  filepath.Join(abs, "CLAUDE.md"),
		Skills:    filepath.Join(abs, "skills"),
		Diary:     filepath.Join(abs, "diary"),
		Secrets:   filepath.Join(abs, "secrets"),
		MCP:       filepath.Join(abs, "MCP.json"),
		Workspace: filepath.Join(abs, "workspace"),
	}, nil
}

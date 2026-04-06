package groupfolder

import (
	"fmt"
	"path/filepath"
	"strings"
)

func IsRoot(folder string) bool {
	return !strings.Contains(folder, "/")
}

var reservedFolders = map[string]bool{"share": true}

type Resolver struct {
	GroupsDir string
	IpcDir    string
}

func isValidFolder(folder string) bool {
	if folder == "" || folder != strings.TrimSpace(folder) {
		return false
	}
	if strings.Contains(folder, "..") || strings.Contains(folder, `\`) {
		return false
	}
	for _, seg := range strings.Split(folder, "/") {
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

// IPC subdirectory conventions.
func IpcInputDir(ipcDir string) string  { return filepath.Join(ipcDir, "input") }
func IpcSocket(ipcDir string) string    { return filepath.Join(ipcDir, "gated.sock") }
func IpcSidecars(ipcDir string) string  { return filepath.Join(ipcDir, "sidecars") }
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

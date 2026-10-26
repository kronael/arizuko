package container

import (
	"log/slog"
	"os"
	"path/filepath"
)

// CodexROMounts are the shared-credential files the mount renderer overmounts
// RO into the group's ~/.codex. Declared once: the renderer reads it to build
// the mount list, SeedCodexMountTargets reads it to own the targets first.
var CodexROMounts = []string{"auth.json", "config.toml"}

// SeedCodexMountTargets creates the RO overmount targets under a group's
// .codex/ for RunUID. Existing nonempty files remain untouched.
func SeedCodexMountTargets(codexDir string) {
	uid, _ := RunUID()
	for _, name := range CodexROMounts {
		p := filepath.Join(codexDir, name)
		if fi, err := os.Lstat(p); err == nil {
			if !fi.Mode().IsRegular() || fi.Size() > 0 || isOwnedByRunUID(fi) {
				continue
			}
			if e := os.Remove(p); e != nil {
				slog.Warn("codex mount target: stale root-owned stub not replaceable",
					"path", p, "err", e)
				continue
			}
		}
		f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			if !os.IsExist(err) {
				slog.Warn("codex mount target", "path", p, "err", err)
			}
			continue
		}
		if err := f.Chown(uid, -1); err != nil {
			slog.Warn("codex mount target: ownership not set",
				"path", p, "uid", uid, "err", err)
		}
		// Empty stub; close failure cannot lose credential bytes.
		_ = f.Close()
	}
}

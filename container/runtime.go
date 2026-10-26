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
// .codex/ so the mount point exists before the docker daemon needs it.
//
// The group's .codex/ is itself a bind mount, so a target docker has to
// materialize lands on the HOST inside it — created by the daemon, hence
// root-owned, inside a tree runed (uid 1000) then chowns. That EPERM was 50
// false warnings per restart on krons, which is exactly how a real root-owned
// path would go unread.
//
// An existing empty root-owned target is replaced: runed owns the directory,
// so it may unlink there even though it may not chown the file. Only a
// zero-length file is touched — a real credential is never removed.
func SeedCodexMountTargets(codexDir string) {
	for _, name := range CodexROMounts {
		p := filepath.Join(codexDir, name)
		if fi, err := os.Lstat(p); err == nil {
			if !fi.Mode().IsRegular() || fi.Size() > 0 || ownedByContainerUID(fi) {
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
		_ = f.Close()
	}
}

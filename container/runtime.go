package container

import (
	"log/slog"
	"os"
	"path/filepath"

	"github.com/kronael/arizuko/core"
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
		f.Close()
	}
}

// AppSrcDir is where MaterializeAppSrc stages the agent-visible copy of `ant/`,
// relative to the data dir.
const AppSrcDir = "app-src"

// MaterializeAppSrc copies runed's own `ant/` — the copy baked into the image at
// /opt/arizuko — onto the data dir, so agent containers can bind-mount it.
//
// A bind mount reads the HOST filesystem, so runed cannot hand a spawned sibling
// a path that exists only inside runed's image; it has to land the bytes on
// shared disk first. Before this, buildMounts mounted cfg.HostAppDir straight
// through, which meant routd and runed read the release while the AGENT — the
// only path that rewrites a live group's skills — read the developer's working
// tree. Both were called /opt/arizuko and held different files (BUGS M1).
//
// Overwrite every start: the image is the truth and nothing else may write here.
func MaterializeAppSrc(cfg *core.Config) string {
	dst := filepath.Join(cfg.ProjectRoot, AppSrcDir)
	src := cfg.EffectiveAppSrcDir()
	if _, err := os.Stat(src); err != nil {
		slog.Warn("app source missing; agents keep the previous copy", "src", src, "err", err)
		return dst
	}
	cpDirOverwrite(src, dst)
	warnChown(dst)
	return dst
}

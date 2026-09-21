package container

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/kronael/arizuko/core"
)

const Bin = "docker"

// CodexROMounts are the shared-credential files buildMounts overmounts RO into
// the group's ~/.codex. Declared once: buildMounts reads it to build the mount
// list, SeedCodexMountTargets reads it to own the targets first.
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

// unreadableCodexCreds names the shared codex credentials that the agent's uid
// certainly cannot open. The RO overmount carries the HOST file's owner and
// mode into the container, and `HOST_CODEX_DIR` is normally an operator's own
// ~/.codex at mode 0600, so the agent's uid gets EACCES on a file docker
// mounted successfully. codex then says "not logged in", which reads as an
// expired login and is a permission fault on the host.
//
// Only the certain case is named. A file the uid owns with the user-read bit,
// or one carrying the world-read bit, is reachable. A group-readable file may
// be reachable through a supplementary group this process cannot enumerate, so
// it is left alone: a wrong accusation here would train the operator to ignore
// the line that matters.
func unreadableCodexCreds(hostCodexDir string, uid int) []string {
	var out []string
	for _, name := range CodexROMounts {
		p := filepath.Join(hostCodexDir, name)
		fi, err := os.Stat(p)
		if err != nil {
			continue // absent is the mount's own failure to report, not this one
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok {
			continue
		}
		mode := fi.Mode().Perm()
		if mode&0o044 != 0 || (int(st.Uid) == uid && mode&0o400 != 0) {
			continue
		}
		out = append(out, p)
	}
	return out
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

func StopContainerArgs(name string) []string {
	return []string{"stop", name}
}

func EnsureRunning() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, Bin, "info").CombinedOutput()
	if err != nil {
		return fmt.Errorf("container runtime unreachable: %w\n%s", err, out)
	}
	return nil
}

func CleanupOrphans(instance, image string) {
	prefix := "arizuko-"
	if instance != "" {
		prefix = "arizuko-" + instance + "-"
	}
	// Single docker ps call with both filters: name-prefix AND image.
	// Previously each filter ran in its own call and results were unioned
	// (OR), so on a host running multiple instances the image filter
	// would match peers and kill them.
	args := []string{"ps", "--filter=name=" + prefix, "--format", "{{.Names}}"}
	if image != "" {
		args = append(args, "--filter=ancestor="+image)
	}
	out, err := exec.Command(Bin, args...).Output()
	if err != nil {
		slog.Warn("failed to list containers for cleanup", "err", err)
		return
	}
	var orphans []string
	for name := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		if name != "" {
			orphans = append(orphans, name)
		}
	}

	for _, name := range orphans {
		_ = exec.Command(Bin, StopContainerArgs(name)...).Run()
	}
	if len(orphans) > 0 {
		slog.Info("stopped orphaned containers", "count", len(orphans), "names", orphans)
	}
}

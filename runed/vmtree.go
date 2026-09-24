package runed

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/kronael/arizuko/core"
	"golang.org/x/sys/unix"
)

// The turn dir's fixed names. One dir per turn holds everything the guest is
// served from, so the reap knows what to remove without remembering it.
const (
	// treeDirName is the staging tree, and it is virtiofsd's source for the
	// `turn` share.
	treeDirName = "tree"
	// treeManifestName sits at the tree root — inside the share, so the guest
	// reads the same declaration the host wrote, rather than a second copy on
	// the control channel. No family mounts at /mounts.json, so it collides
	// with nothing the tree mirrors.
	treeManifestName = "mounts.json"
	rootSockName     = "root.sock"
	turnSockName     = "turn.sock"
	qemuPidName      = "qemu.pid"
)

// treeEntry is one family in the staging tree. Path is the guest path, and it is
// ALSO the tree-relative path: the tree mirrors the guest's own layout, so there
// is no second naming scheme for the two sides to disagree about. An encoding
// that flattened the paths would have to escape, and `/mnt/a-b` and `/mnt/a/b`
// would collide the moment an operator declared both.
type treeEntry struct {
	// Host is the path on the host that this family's bytes come from.
	Host string `json:"-"`
	// Path is where the guest sees it, which is where the host stages it.
	Path string `json:"path"`
	// RO is enforced on the HOST: the bind is remounted read-only before
	// virtiofsd starts, so every write the daemon attempts is EROFS rather
	// than a mode the guest kernel could choose to ignore.
	RO bool `json:"ro"`
}

// treeManifest is what the host declares and guest-init reads. The guest
// bind-mounts each entry from /turn onto its own Path, in the order given, and
// mounts a tmpfs at each Tmpfs path.
type treeManifest struct {
	Mounts []treeEntry `json:"mounts"`
	// Tmpfs are the guest-local paths that are NOT in the share. A unix socket
	// does not cross virtio-fs — a bind-mounted socket answers "Operation not
	// supported" — so the agent's ipc dir is a guest tmpfs and its socket is
	// bridged over vsock instead (spec 6/41 §"The mechanisms").
	Tmpfs []string `json:"tmpfs"`
}

// treePlan turns the rendered mount table into the staging tree's plan. Pure:
// every path decision the guest sees is made here, so one Turn always yields one
// tree.
//
// Sorted by guest path, which puts a parent before its children
// (/home/node before /home/node/.codex before /home/node/.codex/auth.json) —
// the order both the host binds and the guest mounts in, and the layering the
// mount renderer already relies on.
func treePlan(mounts []core.Mount) treeManifest {
	var m treeManifest
	for _, mt := range mounts {
		if mt.Container == ipcMount {
			m.Tmpfs = append(m.Tmpfs, mt.Container)
			continue
		}
		m.Mounts = append(m.Mounts, treeEntry{Host: mt.Host, Path: mt.Container, RO: mt.RO})
	}
	sort.SliceStable(m.Mounts, func(i, j int) bool { return m.Mounts[i].Path < m.Mounts[j].Path })
	slices.Sort(m.Tmpfs)
	return m
}

// turnTree is one turn's staging tree on disk.
type turnTree struct {
	// dir is the tree root, which is virtiofsd's source for `turn`.
	dir string
}

// treeFor names a turn dir's tree without touching the disk, so the disk scan
// can tear down a tree whose plan it never had.
func treeFor(turnDir string) *turnTree { return &turnTree{dir: filepath.Join(turnDir, treeDirName)} }

// stageTargets creates the tree's mount points and writes the manifest. It makes
// no mount, so it needs no privilege — which is what lets a test assert the
// shape of the tree without a hypervisor or root.
//
// A bind target must match its source's kind: the two codex credentials are
// single FILES overmounted read-only, and a directory target for a file source
// is ENOTDIR at mount time.
func (t *turnTree) stageTargets(plan treeManifest) error {
	if err := os.MkdirAll(t.dir, 0o755); err != nil {
		return fmt.Errorf("staging tree: %w", err)
	}
	for _, e := range plan.Mounts {
		target := t.target(e.Path)
		fi, err := os.Stat(e.Host)
		if err != nil {
			// A source the renderer named and the host does not have: the
			// Docker substrate would materialize it as root, which is the
			// failure buildMounts' mkSlot exists to avoid. Refuse instead.
			return fmt.Errorf("staging tree: mount source %s (guest %s): %w", e.Host, e.Path, err)
		}
		if fi.IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("staging tree: target %s: %w", target, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("staging tree: target parent %s: %w", target, err)
		}
		f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return fmt.Errorf("staging tree: file target %s: %w", target, err)
		}
		f.Close()
	}
	b, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(t.dir, treeManifestName), append(b, '\n'), 0o644)
}

// target is the tree path for a guest path.
func (t *turnTree) target(guestPath string) string {
	return filepath.Join(t.dir, strings.TrimPrefix(guestPath, "/"))
}

// bindAll binds every family into the tree, read-only where the grant says so.
// It runs in runed's own mount namespace — runed is a compose service, so its
// container's namespace IS the private one, which is why this needs SYS_ADMIN
// and no unshare.
//
// Read-only is a bind plus a remount: MS_RDONLY on the initial MS_BIND is
// ignored by the kernel, so a single call would leave every "ro" family
// writable while reading as protected.
func (t *turnTree) bindAll(plan treeManifest) error {
	for _, e := range plan.Mounts {
		target := t.target(e.Path)
		if err := unix.Mount(e.Host, target, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
			return fmt.Errorf("bind %s -> %s: %w", e.Host, target, err)
		}
		if !e.RO {
			continue
		}
		if err := unix.Mount("", target, "", unix.MS_REMOUNT|unix.MS_BIND|unix.MS_RDONLY, ""); err != nil {
			return fmt.Errorf("remount ro %s: %w", target, err)
		}
	}
	return nil
}

// teardown unmounts everything under the tree and then removes it.
//
// The mount list comes from /proc/self/mountinfo rather than from the plan, so a
// disk scan after a runed crash tears down a tree whose plan died with the
// process that made it. Deepest first: a parent cannot unmount while a child is
// mounted inside it.
func (t *turnTree) teardown() error {
	if t == nil || t.dir == "" {
		return nil
	}
	if _, err := os.Stat(t.dir); os.IsNotExist(err) {
		return nil
	}
	for range 2 {
		f, err := os.Open("/proc/self/mountinfo")
		if err != nil {
			return fmt.Errorf("staging tree teardown: %w", err)
		}
		mounts := mountedUnder(f, t.dir)
		f.Close()
		if len(mounts) == 0 {
			break
		}
		for _, m := range mounts {
			if err := unix.Unmount(m, 0); err != nil {
				// Lazy is the fallback, not the first try: a MNT_DETACH that
				// nobody re-checks leaves the bind alive under a dir the
				// removal below would then delete THROUGH.
				if err2 := unix.Unmount(m, unix.MNT_DETACH); err2 != nil {
					return fmt.Errorf("staging tree teardown: unmount %s: %w", m, err)
				}
			}
		}
	}
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return fmt.Errorf("staging tree teardown: %w", err)
	}
	left := mountedUnder(f, t.dir)
	f.Close()
	if len(left) > 0 {
		return fmt.Errorf("staging tree %s still has %d mounts (%s); tree kept",
			t.dir, len(left), strings.Join(left, ", "))
	}
	return t.remove()
}

// remove deletes the emptied tree. It removes directories, the manifest and
// zero-length files — exactly what stageTargets created — and refuses on
// anything else.
//
// This is the check that makes the teardown safe rather than merely ordered: a
// bind that is still mounted shows the GROUP'S OWN FILES here, and a recursive
// delete would delete them on the host. A refusal that keeps a tree costs an
// operator one directory; the alternative costs a folder its home.
func (t *turnTree) remove() error {
	var dirs []string
	var foreign []string
	err := filepath.WalkDir(t.dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			dirs = append(dirs, p)
			return nil
		}
		if p == filepath.Join(t.dir, treeManifestName) {
			return os.Remove(p)
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		if fi.Size() != 0 || !fi.Mode().IsRegular() {
			foreign = append(foreign, p)
			return nil
		}
		return os.Remove(p)
	})
	if err != nil {
		return fmt.Errorf("staging tree %s: %w", t.dir, err)
	}
	if len(foreign) > 0 {
		return fmt.Errorf("staging tree %s holds %d file(s) it did not create (%s); tree kept",
			t.dir, len(foreign), strings.Join(foreign, ", "))
	}
	// Deepest first, so every dir is empty when it is removed.
	slices.Reverse(dirs)
	for _, d := range dirs {
		if err := os.Remove(d); err != nil {
			return fmt.Errorf("staging tree %s: %w", d, err)
		}
	}
	return nil
}

// mountedUnder returns every mount point at or under dir, deepest first, read
// from a mountinfo stream. Field 5 is the mount point (proc(5)
// /proc/<pid>/mountinfo); the kernel escapes space, tab, newline and backslash
// as octal there.
func mountedUnder(r io.Reader, dir string) []string {
	var out []string
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 5 {
			continue
		}
		mp := unescapeMountinfo(f[4])
		if mp == dir || strings.HasPrefix(mp, dir+"/") {
			out = append(out, mp)
		}
	}
	if err := sc.Err(); err != nil {
		slog.Error("read mountinfo", "err", err)
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

// unescapeMountinfo undoes the octal escapes the kernel writes in a mount point.
func unescapeMountinfo(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			var v int
			if _, err := fmt.Sscanf(s[i+1:i+4], "%03o", &v); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

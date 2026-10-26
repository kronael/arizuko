package container

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
)

var (
	safeNameRe  = regexp.MustCompile(`[^a-zA-Z0-9-]`)
	skillNameRe = regexp.MustCompile(`^[a-z0-9-]+$`)
)

func SanitizeFolder(folder string) string {
	s := strings.ReplaceAll(folder, "/", "-")
	s = safeNameRe.ReplaceAllString(s, "-")
	if len(s) > 40 {
		s = s[:40]
	}
	return strings.Trim(s, "-")
}

func SetupGroup(cfg *core.Config, folder, seedDir string) error {
	return setupGroup(cfg, folder, func(groupDir string) error {
		if seedDir == "" {
			return nil
		}
		// Fatal, unlike the copy warning below: a product shipping an asset no
		// consumer reads is an authoring error, and seeding it reports success
		// for a payload that then does nothing (spec 5/28-packages).
		if err := CheckProductAssets(seedDir, seedDir); err != nil {
			return err
		}
		if err := SeedProduct(groupDir, seedDir); err != nil {
			slog.Warn("setup group: copy seed dir", "folder", folder, "err", err)
		}
		return nil
	})
}

func ComposeGroup(
	cfg *core.Config, folder string,
	mix []Product, prior map[string]map[string]string,
) ([]Blended, error) {
	var out []Blended
	err := setupGroup(cfg, folder, func(groupDir string) error {
		var berr error
		out, berr = BlendProducts(groupDir, mix, prior)
		return berr
	})
	return out, err
}

func setupGroup(cfg *core.Config, folder string,
	seed func(groupDir string) error) error {
	r := &groupfolder.Resolver{GroupsDir: cfg.GroupsDir, IpcDir: cfg.IpcDir}
	groupDir, err := r.GroupPath(folder)
	if err != nil {
		return fmt.Errorf("invalid group folder: %w", err)
	}
	if err := os.MkdirAll(groupDir, 0o755); err != nil {
		return fmt.Errorf("mkdir group: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(groupDir, "logs"), 0o755); err != nil {
		return fmt.Errorf("mkdir logs: %w", err)
	}
	if err := seed(groupDir); err != nil {
		return err
	}
	// Per-group web slots — bind-mounted into ~/public_html and ~/private_html
	// at agent spawn time. Pre-create AND chown them HERE: seedGroupDir's chownR
	// walks cfg.GroupsDir/<folder>, and these live under cfg.WebDir — a different
	// tree no chown ever reached. A slot the container uid cannot write is
	// read-only to the agent, which reports it as a broken mount (BUGS F62/F63).
	// Spec 5/V-web-vhosts.
	if cfg.WebDir != "" {
		for _, slot := range []string{
			filepath.Join(cfg.WebDir, "pub", folder),
			filepath.Join(cfg.WebDir, "priv", folder),
		} {
			// Fatal: an absent slot is materialized by the docker daemon at
			// spawn — as root, on a tree the agent owns.
			if err := os.MkdirAll(slot, 0o755); err != nil {
				return fmt.Errorf("mkdir web slot %s: %w", slot, err)
			}
			warnChown(slot)
		}
	}
	return seedGroupDir(cfg, folder)
}

func seedGroupDir(cfg *core.Config, folder string) error {
	groupDir := filepath.Join(cfg.GroupsDir, folder)
	claudeDir := filepath.Join(groupDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		return err
	}
	seedSkills(cfg, claudeDir, folder)
	// Host uid may differ from container node=1000; chown so ant can write.
	warnChown(groupDir)
	return nil
}

const containerUID = 1000

// RunUID reports the uid the agent runs as INSIDE the guest, and whether the
// substrate has to be told it. runed as root or as 1000 leaves the image's own
// uid alone; any other host uid is passed through so the mounted group home
// stays writable. Declared once because two readers need it: the substrate
// passes the flag, the mount renderer asks which uid must be able to open a
// host file.
func RunUID() (uid int, explicit bool) {
	if host := os.Getuid(); host > 0 && host != containerUID {
		return host, true
	}
	return containerUID, false
}

func isOwnedByRunUID(fi os.FileInfo) bool {
	uid, _ := RunUID()
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == uid
}

// warnChown gives the container uid ownership of root and reports failure.
// Not fatal — a host whose uid is neither root nor 1000 (any dev checkout)
// legitimately cannot chown, and refusing to create the group there would be
// worse than the warning. Loud is the point: the discarded EPERM is how a
// root-owned ~/public_html survived from May to August unnoticed.
func warnChown(root string) {
	if err := chownR(root, containerUID, containerUID); err != nil {
		slog.Warn("chown failed — the agent (uid 1000) may not be able to write this path",
			"path", root, "uid", containerUID, "err", err)
	}
}

// chownR walks root and gives every entry to uid:gid. It returns the first
// failure rather than discarding it: runed runs as uid 1000, so an Lchown of a
// root-owned path returns EPERM and the tree silently stays unwritable.
//
// A WALK error is a failure too, and the louder one: it means a directory runed
// cannot read, so the whole subtree under it is skipped with no Lchown even
// attempted. Discarding it left F62's exact silence one level up.
//
// Both keep walking and return nil, so one unreadable corner does not stop the
// rest of the tree being chowned; the first error still reaches the caller.
func chownR(root string, uid, gid int) error {
	var firstErr error
	keep := func(e error) {
		if firstErr == nil {
			firstErr = e
		}
	}
	if err := filepath.WalkDir(root, func(p string, _ os.DirEntry, err error) error {
		if err != nil {
			keep(err)
			return nil
		}
		if e := os.Lchown(p, uid, gid); e != nil {
			keep(e)
		}
		return nil
	}); err != nil {
		keep(err)
	}
	return firstErr
}

func seedSkills(cfg *core.Config, claudeDir, folder string) {
	jsonDst := filepath.Join(claudeDir, ".claude.json")
	if _, err := os.Stat(jsonDst); os.IsNotExist(err) {
		userID := fmt.Sprintf("%x", sha256.Sum256([]byte("arizuko:"+folder)))
		data, _ := json.MarshalIndent(map[string]any{
			"firstStartTime": time.Now().Format(time.RFC3339),
			"userID":         userID,
		}, "", "  ")
		if err := os.WriteFile(jsonDst, append(data, '\n'), 0o644); err != nil {
			slog.Warn("first-start marker not written; the agent re-runs onboarding every turn",
				"path", jsonDst, "folder", folder, "err", err)
		}
	}
}

func WriteGatewayCaps(groupDir string, cfg *core.Config) {
	var b strings.Builder
	fmt.Fprintf(&b, "[voice]\nenabled = %v\nmodel = %q\n\n",
		cfg.VoiceEnabled, cfg.WhisperModel)
	fmt.Fprintf(&b, "[video]\nenabled = %v\n\n", cfg.VideoEnabled)
	fmt.Fprintf(&b, "[media]\nenabled = %v\nmax_size_mb = %d\n\n",
		cfg.MediaEnabled, cfg.MediaMaxBytes/(1024*1024))
	if cfg.WebHost != "" {
		fmt.Fprintf(&b, "[web]\nenabled = true\nhost = %q\n", cfg.WebHost)
	} else {
		fmt.Fprintf(&b, "[web]\nenabled = false\n")
	}
	p := filepath.Join(groupDir, ".gateway-caps")
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		slog.Error("gateway caps not written; the agent reads the previous turn's capabilities",
			"path", p, "err", err)
	}
}

// CopySession duplicates a Claude Code session jsonl from src uuid
// to dst uuid under a group's `.claude/projects/-home-node/` dir.
// Slug is fixed (`-home-node`) because every container mounts groupDir
// at $HOME=/home/node — Claude Code slugifies that path identically
// across folders. Pure file op: rename-after-write for atomicity.
// Returns nil with WARN log when src is missing (caller gets a fresh
// session, no parent context). Used by spec 5/F-topic-lineage fork path.
func CopySession(groupDir, srcUUID, dstUUID string) (bool, error) {
	projDir := filepath.Join(groupDir, ".claude", "projects", "-home-node")
	src := filepath.Join(projDir, srcUUID+".jsonl")
	dst := filepath.Join(projDir, dstUUID+".jsonl")
	data, err := os.ReadFile(src)
	if err != nil {
		if os.IsNotExist(err) {
			slog.Warn("CopySession: parent session file missing",
				"src", src, "groupDir", groupDir)
			return false, nil
		}
		return false, fmt.Errorf("read src: %w", err)
	}
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		return false, fmt.Errorf("mkdir: %w", err)
	}
	// Restamp the transcript to the child id. Every line carries
	// "sessionId":"<src>"; a copy that still names the parent is a transcript
	// two topics claim, and Claude Code appends the child's new turns to a file
	// whose history says otherwise. The token is exact (a UUID inside a fixed
	// JSON key), so a byte replace beats re-encoding 1 MB of JSONL.
	data = bytes.ReplaceAll(data,
		[]byte(`"sessionId":"`+srcUUID+`"`),
		[]byte(`"sessionId":"`+dstUUID+`"`))
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return false, fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return false, fmt.Errorf("rename: %w", err)
	}
	return true, nil
}

func cpDirOverwrite(src, dst string) { cpDirImpl(src, dst, false) }

func cpDirImpl(src, dst string, skipExisting bool) {
	// ERROR, the level seedSettings uses for the same symptom: a destination
	// the run uid cannot create is the "typing but no reply" outage — the agent
	// spawns without the files this copy was meant to place, and answers nothing.
	if err := os.MkdirAll(dst, 0o755); err != nil {
		slog.Error("cpDir: mkdir failed — the group home is not writable by the run uid, "+
			"so the agent starts without these files; chown the home to the run uid",
			"path", dst, "err", err)
		return
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		slog.Warn("cpDir: readdir failed", "path", src, "err", err)
		return
	}
	for _, e := range entries {
		sp := filepath.Join(src, e.Name())
		dp := filepath.Join(dst, e.Name())
		// Lstat: copying symlink targets would leak arbitrary host files.
		fi, err := os.Lstat(sp)
		if err != nil {
			slog.Warn("cpDir: lstat failed", "path", sp, "err", err)
			continue
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			slog.Warn("cpDir: skipping symlink", "path", sp)
			continue
		}
		if fi.IsDir() {
			cpDirImpl(sp, dp, skipExisting)
			continue
		}
		if skipExisting {
			if _, err := os.Stat(dp); err == nil {
				continue
			}
		}
		if data, err := os.ReadFile(sp); err != nil {
			slog.Warn("cpDir: read failed", "path", sp, "err", err)
		} else {
			// Unlink an existing dst before writing: a prior root-owned copy
			// (from a past sudo op) can't be truncate-opened by the uid-1000
			// daemon, but the uid-1000-owned dir lets us unlink + recreate it.
			// Without this, cpDirOverwrite silently fails on root-owned files
			// and the styles stay frozen — the staleness it was meant to cure.
			_ = os.Remove(dp)
			if err := os.WriteFile(dp, data, 0o644); err != nil {
				slog.Error("cpDir: write failed — the agent starts without this file; "+
					"chown the group home to the run uid",
					"path", dp, "err", err)
			}
		}
	}
}

func writeSnapshot(folders *groupfolder.Resolver, folder, filename string, v any) {
	ipcDir, err := folders.IpcPath(folder)
	if err != nil {
		slog.Warn("cannot write snapshot", "folder", folder, "file", filename, "err", err)
		return
	}
	if err := os.MkdirAll(ipcDir, 0o755); err != nil {
		slog.Warn("cannot write snapshot: no ipc dir",
			"folder", folder, "file", filename, "dir", ipcDir, "err", err)
		return
	}
	data, _ := json.MarshalIndent(v, "", "  ")
	if err := os.WriteFile(filepath.Join(ipcDir, filename), data, 0o644); err != nil {
		slog.Warn("snapshot not written; the agent reads the previous one",
			"folder", folder, "file", filename, "err", err)
	}
}

func WriteTasksSnapshot(
	folders *groupfolder.Resolver,
	folder string, isRoot bool,
	tasks []core.Task,
) {
	if !isRoot {
		var f []core.Task
		for _, t := range tasks {
			if t.Owner == folder {
				f = append(f, t)
			}
		}
		tasks = f
	}
	writeSnapshot(folders, folder, "current_tasks.json", tasks)
}

func WriteGroupsSnapshot(
	folders *groupfolder.Resolver,
	folder string, isRoot bool,
	groups []core.Group,
) {
	if !isRoot {
		groups = nil
	}
	writeSnapshot(folders, folder, "available_groups.json", struct {
		Groups   []core.Group `json:"groups"`
		LastSync string       `json:"lastSync"`
	}{groups, time.Now().Format(time.RFC3339)})
}

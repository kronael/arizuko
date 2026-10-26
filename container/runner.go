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
	"github.com/kronael/arizuko/store"
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

// SeedOutputStyles refreshes the per-surface output-style files on EVERY spawn.
// cpDirOverwrite, not cpDirFresh: these are platform-managed content, and
// cpDirFresh skipped existing files, so a style tweak only ever reached freshly
// created groups and silently regressed everywhere else. A group's own CUSTOM
// style file is left untouched (cpDirImpl only copies src entries).
func SeedOutputStyles(cfg *core.Config, claudeDir string) {
	src := filepath.Join(cfg.EffectiveAppSrcDir(), "ant", "output-styles")
	dst := filepath.Join(claudeDir, "output-styles")
	cpDirOverwrite(src, dst)
}

// SeedMigrateSkill force-refreshes the platform's self-update bootstrap skill on
// every spawn. migrate is the ONLY thing that syncs skills, so a stale copy can
// never update itself and deadlocks every future migration. Cost 11 days of
// frozen skills on the live split. Unlike stock skills it is platform-owned —
// always overwritten, like output-styles. MIGRATION_VERSION lives under self/.
func SeedMigrateSkill(cfg *core.Config, claudeDir string) {
	src := filepath.Join(cfg.EffectiveAppSrcDir(), "ant", "skills", "migrate")
	dst := filepath.Join(claudeDir, "skills", "migrate")
	cpDirOverwrite(src, dst)
}

// SetupGroup borrows database; nil selects core before a store exists.
// It acquires the instance package lock; callers must not already hold it.
func SetupGroup(cfg *core.Config, database *store.Store, folder, seedDir string) error {
	return setupGroup(cfg, database, folder, func(groupDir string) error {
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

// ComposeGroup borrows database and acquires the package lock like SetupGroup.
// prior protects locally edited product assets.
func ComposeGroup(
	cfg *core.Config, database *store.Store, folder string,
	mix []Product, prior map[string]map[string]string,
) ([]Blended, error) {
	var out []Blended
	err := setupGroup(cfg, database, folder, func(groupDir string) error {
		var berr error
		out, berr = BlendProducts(groupDir, mix, prior)
		return berr
	})
	return out, err
}

func setupGroup(cfg *core.Config, database *store.Store, folder string,
	seed func(groupDir string) error) error {
	r := &groupfolder.Resolver{GroupsDir: cfg.GroupsDir, IpcDir: cfg.IpcDir}
	groupDir, err := r.GroupPath(folder)
	if err != nil {
		return fmt.Errorf("invalid group folder: %w", err)
	}
	lock, err := LockPackages(cfg.ProjectRoot)
	if err != nil {
		return err
	}
	// The lock covers initialization and the new group's filesystem setup.
	defer lock.Close() //nolint:errcheck
	selected, err := ReadPackageSelection(cfg.ProjectRoot, folder)
	if err != nil {
		return err
	}
	if err := CheckPackageSelection(cfg, folder, database, selected); err != nil {
		return err
	}
	if err := EnsureCorePackage(cfg, database); err != nil {
		return err
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

// ownedByContainerUID reports whether a stat already names the agent uid, so a
// caller can skip a repair it does not need. A platform without a Unix stat
// answers false: repairing there is harmless, claiming ownership is not.
func ownedByContainerUID(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == containerUID
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

// PackageSkillsDir is the instance-wide tree `arizuko packages install` writes a
// package's `skills/<name>/` into, and `remove` deletes from. Nothing else
// writes it, which is what makes the tree upstream-managed rather than the
// operator's (spec 5/28-packages skills asset kind).
func PackageSkillsDir(cfg *core.Config) string {
	return filepath.Join(filepath.Dir(cfg.GroupsDir), "skills")
}

// SeedPackageSkills layers the instance's package skills into one group's
// `~/.claude/skills/` and returns how many it wrote. Two callers: group setup,
// and `arizuko generate` — which is what reaches a group that already existed
// when the package was installed (BUGS L47).
//
// OVERWRITE, unlike the stock skills seedSkills lays down first, and the two
// trees are why. A stock skill is `ours`: the operator may edit it, `/migrate`
// 3-way merges the next release against `.merge-base`, and cpDirFresh must not
// clobber the edit. A package skill has no `.merge-base` entry and no operator
// authoring path — the package owns the directory NAME and `remove` deletes the
// whole dir — so it is upstream-managed (spec 5/28-packages, `managed: sync`) and a fix in the
// package has to reach the group. Before this it could not: seedSkills ran only
// at group creation, and cpDirFresh skipped every file that already existed, so
// a package's skill froze at whatever content existed when each group was made.
//
// Two things the overwrite must NOT take with it, both already operator-owned:
// a `.disabled` marker (the same escape hatch stock skills have, surfaced by
// dashd's per-group skill toggle), and a name a stock skill already holds — the
// package layer sits on top of the `ours` tree, so overwriting there would let a
// package edit a shipped skill.
func SeedPackageSkills(cfg *core.Config, folder string) (int, error) {
	r := &groupfolder.Resolver{GroupsDir: cfg.GroupsDir, IpcDir: cfg.IpcDir}
	groupDir, err := r.GroupPath(folder)
	if err != nil {
		return 0, fmt.Errorf("invalid group folder: %w", err)
	}
	src := PackageSkillsDir(cfg)
	pkgs, err := os.ReadDir(src)
	if os.IsNotExist(err) {
		return 0, nil // no package ships a skill on this instance
	}
	if err != nil {
		return 0, fmt.Errorf("read package skills %s: %w", src, err)
	}
	stock := map[string]bool{}
	if entries, serr := os.ReadDir(filepath.Join(cfg.EffectiveAppSrcDir(), "ant", "skills")); serr == nil {
		for _, e := range entries {
			stock[e.Name()] = true
		}
	}
	dst := filepath.Join(groupDir, ".claude", "skills")
	var n int
	for _, e := range pkgs {
		if !e.IsDir() || !skillNameRe.MatchString(e.Name()) {
			continue
		}
		if stock[e.Name()] {
			slog.Warn("package skill shadows a shipped skill; not seeded",
				"skill", e.Name(), "folder", folder)
			continue
		}
		d := filepath.Join(dst, e.Name())
		if _, err := os.Stat(filepath.Join(d, ".disabled")); err == nil {
			continue
		}
		cpDirOverwrite(filepath.Join(src, e.Name()), d)
		// generate usually runs as root while the agent runs as uid 1000; a
		// skill file it cannot read is a skill it does not have.
		warnChown(d)
		n++
	}
	return n, nil
}

func seedSkills(cfg *core.Config, claudeDir, folder string) {
	src := filepath.Join(cfg.EffectiveAppSrcDir(), "ant", "skills")
	dst := filepath.Join(claudeDir, "skills")
	base := filepath.Join(claudeDir, ".merge-base", "skills")

	entries, err := os.ReadDir(src)
	if err != nil {
		return
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if !skillNameRe.MatchString(e.Name()) {
			slog.Warn("skipping skill with invalid name",
				"name", e.Name())
			continue
		}
		d := filepath.Join(dst, e.Name())
		if _, err := os.Stat(filepath.Join(d, ".disabled")); err == nil {
			// Disabled by operator: don't seed; remove SKILL.md so
			// Claude Code stops indexing it, and drop the stale
			// merge-base so re-enable starts from a fresh sync.
			if err := os.Remove(filepath.Join(d, "SKILL.md")); err != nil && !os.IsNotExist(err) {
				slog.Warn("disabled skill keeps its SKILL.md; the agent still indexes it",
					"skill", e.Name(), "folder", folder, "err", err)
			}
			if err := os.RemoveAll(filepath.Join(base, e.Name())); err != nil {
				slog.Warn("stale merge-base kept; a re-enable 3-way merges against it",
					"skill", e.Name(), "folder", folder, "err", err)
			}
			continue
		}
		s := filepath.Join(src, e.Name())
		// ours: preserve operator edits — only write missing files.
		cpDirFresh(s, d)
		// merge-base: full mirror of upstream — wipe then copy.
		baseDir := filepath.Join(base, e.Name())
		if err := os.RemoveAll(baseDir); err != nil {
			slog.Warn("merge-base not wiped; it keeps files this release dropped",
				"skill", e.Name(), "folder", folder, "err", err)
		}
		cpDirOverwrite(s, baseDir)
	}

	if _, err := SeedPackageSkills(cfg, folder); err != nil {
		slog.Warn("seed package skills", "folder", folder, "err", err)
	}

	mdSrc := filepath.Join(cfg.EffectiveAppSrcDir(), "ant", "CLAUDE.md")
	mdDst := filepath.Join(claudeDir, "CLAUDE.md")
	mdBase := filepath.Join(claudeDir, ".merge-base", "CLAUDE.md")
	if data, err := os.ReadFile(mdSrc); err == nil {
		// ours: only write if missing — preserve operator edits.
		if _, err := os.Stat(mdDst); os.IsNotExist(err) {
			if err := os.WriteFile(mdDst, data, 0o644); err != nil {
				slog.Error("CLAUDE.md not written; the agent starts without it",
					"path", mdDst, "folder", folder, "err", err)
			}
		}
		// merge-base: always refresh to mirror upstream.
		if err := os.MkdirAll(filepath.Dir(mdBase), 0o755); err != nil {
			slog.Warn("merge-base dir", "path", filepath.Dir(mdBase), "folder", folder, "err", err)
		} else if err := os.WriteFile(mdBase, data, 0o644); err != nil {
			slog.Warn("merge-base CLAUDE.md not refreshed; /migrate merges against the older one",
				"path", mdBase, "folder", folder, "err", err)
		}
	}

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

func MigrationVersion(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	var v int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &v); err != nil {
		// 0, the same answer an absent file gives. Which way that fails depends on
		// WHICH file: a garbled upstream MIGRATION_VERSION makes checkMigrationVersion
		// return early and no group migrates at all, so the value has to be named.
		slog.Warn("migration version file is not a number", "path", path, "err", err)
		return 0
	}
	return v
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

// cpDirFresh copies src→dst, skipping files that already exist in dst.
// Used to seed operator-owned `ours` without clobbering live edits.
func cpDirFresh(src, dst string) { cpDirImpl(src, dst, true) }

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

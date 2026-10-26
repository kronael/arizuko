package container

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
	"golang.org/x/sys/unix"
)

const CorePackageName = "core"

const corePackageSource = "ant"

var corePackageRoots = []string{ManifestFile, "CLAUDE.md", "skills", "output-styles"}

type PackageSkill struct {
	Name string
	Dir  string
}

type PackageInventory struct {
	Core   string
	Skills []PackageSkill
}

// ReadPackageInventory requires the package lock. Only active system and exact
// folder selections supply files; every returned directory is an immutable path.
func ReadPackageInventory(dataDir, folder string) (PackageInventory, error) {
	if folder == "" {
		return PackageInventory{}, fmt.Errorf("package inventory requires an exact folder")
	}
	system, err := ReadPackageSelection(dataDir, "")
	if err != nil {
		return PackageInventory{}, err
	}
	group, err := ReadPackageSelection(dataDir, folder)
	if err != nil {
		return PackageInventory{}, err
	}
	return readPackageInventory(dataDir, PackageAdoptionSelection{System: system, Group: group})
}

func readPackageInventory(dataDir string, selected PackageAdoptionSelection) (PackageInventory, error) {
	var inventory PackageInventory
	names := map[string]bool{}
	for i, snapshots := range [][]PackageSnapshot{selected.System, selected.Group} {
		for _, snapshot := range snapshots {
			if i == 1 && snapshot.Name == CorePackageName {
				return inventory, fmt.Errorf("group selects required system package core")
			}
			key := "package:" + snapshot.Name
			if names[key] {
				return inventory, fmt.Errorf("duplicate package %q", snapshot.Name)
			}
			names[key] = true
			dir, err := ResolvePackage(dataDir, snapshot)
			if err != nil {
				return inventory, err
			}
			if snapshot.Name == CorePackageName {
				if _, err := inspectPackage(dir, corePackageRoots); err != nil {
					return inventory, err
				}
				inventory.Core = dir
			}
			skills, err := ReadPackageSkills(dir)
			if err != nil {
				return inventory, err
			}
			for _, name := range skills {
				if !skillNameRe.MatchString(name) || names["skill:"+name] {
					return inventory, fmt.Errorf("duplicate or invalid packaged skill %q", name)
				}
				names["skill:"+name] = true
				inventory.Skills = append(inventory.Skills, PackageSkill{Name: name, Dir: filepath.Join(dir, "skills", name)})
			}
		}
	}
	if inventory.Core == "" {
		return inventory, fmt.Errorf("required system package core is missing; run packages upgrade core")
	}
	slices.SortFunc(inventory.Skills, func(a, b PackageSkill) int { return strings.Compare(a.Name, b.Name) })
	return inventory, nil
}

func openPackageHome(groupsDir, folder string) (*os.Root, string, error) {
	if groupsDir == "" || folder == "" {
		return nil, "", fmt.Errorf("package home requires configured groups directory and exact folder")
	}
	if _, err := packageTarget(folder); err != nil {
		return nil, "", err
	}
	if err := checkPackagePath(groupsDir); err != nil {
		return nil, "", err
	}
	abs, err := filepath.Abs(groupsDir)
	if err != nil {
		return nil, "", err
	}
	base := abs
	for {
		_, err := os.Lstat(base)
		if !errors.Is(err, fs.ErrNotExist) {
			if err != nil {
				return nil, "", err
			}
			break
		}
		base = filepath.Dir(base)
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return nil, "", err
	}
	rel, err := filepath.Rel(base, filepath.Join(abs, folder))
	if err != nil {
		return nil, "", errors.Join(err, root.Close())
	}
	remaining := filepath.ToSlash(rel)
	for remaining != "." && remaining != "" {
		part, rest, _ := strings.Cut(remaining, "/")
		if _, err := root.Lstat(part); errors.Is(err, fs.ErrNotExist) {
			return root, remaining, nil
		} else if err != nil {
			return nil, "", errors.Join(err, root.Close())
		}
		root, err = openPackageChild(root, part)
		if err != nil {
			return nil, "", err
		}
		remaining = rest
	}
	return root, ".", nil
}

func openPackageChild(root *os.Root, name string) (*os.Root, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, errors.Join(err, root.Close())
	}
	if !info.IsDir() {
		return nil, errors.Join(fmt.Errorf("package home is not a real directory: %s",
			filepath.Join(root.Name(), name)), root.Close())
	}
	child, err := root.OpenRoot(name)
	if err != nil {
		return nil, errors.Join(err, root.Close())
	}
	opened, err := child.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.Join(fmt.Errorf("package home directory changed: %s",
			filepath.Join(root.Name(), name)), err, child.Close(), root.Close())
	}
	if err := root.Close(); err != nil {
		return nil, errors.Join(err, child.Close())
	}
	return child, nil
}

func preparePackageHome(groupsDir, folder string) (*os.Root, error) {
	root, remaining, err := openPackageHome(groupsDir, folder)
	if err != nil {
		return nil, err
	}
	for remaining != "." && remaining != "" {
		part, rest, _ := strings.Cut(remaining, "/")
		if err := root.Mkdir(part, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
			return nil, errors.Join(err, root.Close())
		}
		root, err = openPackageChild(root, part)
		if err != nil {
			return nil, err
		}
		if os.Geteuid() == 0 {
			if err := root.Chown(".", containerUID, containerUID); err != nil {
				return nil, errors.Join(err, root.Close())
			}
		}
		remaining = rest
	}
	return root, nil
}

func checkMountTarget(root *os.Root, rel string, directory bool) error {
	if err := checkPackageDirs(root, path.Dir(rel)); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("unsafe mount target %s: %w", filepath.Join(root.Name(), rel), err)
	}
	info, err := root.Lstat(rel)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !directory {
		if !info.Mode().IsRegular() || info.Size() != 0 {
			return fmt.Errorf("target is not an empty regular file: %s", filepath.Join(root.Name(), rel))
		}
		return nil
	}
	if !info.IsDir() {
		return fmt.Errorf("target is not a real directory: %s", filepath.Join(root.Name(), rel))
	}
	dir, err := root.Open(rel)
	if err != nil {
		return err
	}
	entries, err := dir.ReadDir(-1)
	if err := errors.Join(err, dir.Close()); err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() != ".disabled" || !entry.Type().IsRegular() {
			return fmt.Errorf("writable skill payload at %s", filepath.Join(root.Name(), rel, entry.Name()))
		}
	}
	return nil
}

func packageFileTargets(inventory PackageInventory) ([]string, error) {
	targets := []string{packageFileTarget("CLAUDE.md")}
	entries, err := os.ReadDir(filepath.Join(inventory.Core, "output-styles"))
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			return nil, fmt.Errorf("core output style is not a regular file: %s", entry.Name())
		}
		targets = append(targets, packageFileTarget(path.Join("output-styles", entry.Name())))
	}
	return targets, nil
}

func packageFileTarget(source string) string {
	return path.Join(".claude", source)
}

func packageSkillTargets(name string) [2]string {
	return [2]string{path.Join(".claude/skills", name), path.Join("skills", name)}
}

// CheckPackageMountTargets is read-only and requires the package lock. Managed
// targets accept only empty mount structure, with a regular disable marker.
func CheckPackageMountTargets(cfg *core.Config, folder string, inventory PackageInventory) (map[string]bool, error) {
	root, home, err := openPackageHome(cfg.GroupsDir, folder)
	if err != nil {
		return nil, err
	}
	// Closing a directory handle has no pending writes.
	defer root.Close() //nolint:errcheck
	disabled := map[string]bool{}
	for _, skill := range inventory.Skills {
		targets := packageSkillTargets(skill.Name)
		rel := path.Join(home, targets[0])
		if err := checkMountTarget(root, rel, true); err != nil {
			return nil, fmt.Errorf("%w; run ordinary packages install/upgrade to adopt proven copies", err)
		}
		marker := path.Join(rel, ".disabled")
		if _, err := root.Lstat(marker); err == nil {
			disabled[skill.Name] = true
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		if err := checkLegacySkill(root, path.Join(home, targets[1])); err != nil {
			return nil, err
		}
	}
	data, err := openPackageRoot(cfg.ProjectRoot)
	if err != nil {
		return nil, err
	}
	// Closing a directory handle has no pending writes.
	defer data.Close() //nolint:errcheck
	for _, skill := range inventory.Skills {
		targets := packageSkillTargets(skill.Name)
		if err := checkLegacySkill(data, targets[1]); err != nil {
			return nil, err
		}
	}
	targets, err := packageFileTargets(inventory)
	if err != nil {
		return nil, err
	}
	for _, target := range targets {
		if err := checkMountTarget(root, path.Join(home, target), false); err != nil {
			return nil, fmt.Errorf("%w; run ordinary packages install/upgrade to adopt proven copies", err)
		}
	}
	return disabled, nil
}

func checkLegacySkill(root *os.Root, rel string) error {
	if err := checkPackageDirs(root, path.Dir(rel)); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("unsafe skill path %s: %w", filepath.Join(root.Name(), rel), err)
	}
	if _, err := root.Lstat(rel); err == nil {
		return fmt.Errorf("writable skill collision at %s; run ordinary packages install/upgrade to adopt proven copies",
			filepath.Join(root.Name(), rel))
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// PreparePackageMountTargets requires the package lock through source resolution
// and complete preflight. It creates only empty targets in the writable home.
func PreparePackageMountTargets(cfg *core.Config, folder string, inventory PackageInventory) (map[string]bool, string, error) {
	disabled, err := CheckPackageMountTargets(cfg, folder, inventory)
	if err != nil {
		return nil, "", err
	}
	data, err := openPackageRoot(cfg.ProjectRoot)
	if err != nil {
		return nil, "", err
	}
	// Closing a directory handle has no pending writes.
	defer data.Close() //nolint:errcheck
	const empty = "packages/.mount-empty"
	if err := checkPackageDirs(data, "packages"); err != nil {
		return nil, "", err
	}
	if info, err := data.Lstat(empty); err == nil {
		if !info.IsDir() || info.Mode().Perm() != 0o555 {
			return nil, "", fmt.Errorf("empty skill mask is not a sealed directory: %s", filepath.Join(data.Name(), empty))
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, "", err
	} else if err := data.Mkdir(empty, 0o555); err != nil {
		return nil, "", err
	} else {
		if err := data.Chmod(empty, 0o555); err != nil {
			return nil, "", err
		}
		if os.Geteuid() == 0 {
			if err := data.Chown(empty, containerUID, containerUID); err != nil {
				return nil, "", err
			}
		}
	}
	file, err := data.Open(empty)
	if err != nil {
		return nil, "", err
	}
	entries, err := file.ReadDir(-1)
	if err := errors.Join(err, file.Close()); err != nil {
		return nil, "", err
	}
	if len(entries) != 0 {
		return nil, "", fmt.Errorf("empty skill mask contains entries: %s", filepath.Join(data.Name(), empty))
	}
	root, err := preparePackageHome(cfg.GroupsDir, folder)
	if err != nil {
		return nil, "", err
	}
	// Closing a directory handle has no pending writes.
	defer root.Close() //nolint:errcheck
	for _, skill := range inventory.Skills {
		if err := packageDirs(root, path.Join(".claude/skills", skill.Name)); err != nil {
			return nil, "", err
		}
	}
	targets, err := packageFileTargets(inventory)
	if err != nil {
		return nil, "", err
	}
	for _, target := range targets {
		rel := target
		if err := packageDirs(root, path.Dir(rel)); err != nil {
			return nil, "", err
		}
		file, err := root.OpenFile(rel, os.O_CREATE|os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0o644)
		if err != nil {
			return nil, "", err
		}
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() != 0 {
			return nil, "", errors.Join(fmt.Errorf("mount target changed: %s", filepath.Join(root.Name(), rel)),
				err, file.Close())
		}
		if os.Geteuid() == 0 {
			err = file.Chown(containerUID, containerUID)
		}
		if err := errors.Join(err, file.Close()); err != nil {
			return nil, "", err
		}
	}
	return disabled, filepath.Join(data.Name(), empty), nil
}

// SetPackageSkillDisabled requires the package lock and a preflighted inventory.
// Existing marker contents are preserved; only marker presence selects the mask.
func SetPackageSkillDisabled(cfg *core.Config, folder, skill string, disable bool) error {
	if !skillNameRe.MatchString(skill) {
		return fmt.Errorf("invalid skill name %q", skill)
	}
	root, home, err := openPackageHome(cfg.GroupsDir, folder)
	if err != nil {
		return err
	}
	dir := path.Join(home, ".claude/skills", skill)
	if err := checkMountTarget(root, dir, true); err != nil {
		return errors.Join(err, root.Close())
	}
	if err := root.Close(); err != nil {
		return err
	}
	root, err = preparePackageHome(cfg.GroupsDir, folder)
	if err != nil {
		return err
	}
	// Closing a directory handle has no pending writes.
	defer root.Close() //nolint:errcheck
	dir = path.Join(".claude/skills", skill)
	marker := path.Join(dir, ".disabled")
	if !disable {
		err := root.Remove(marker)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := packageDirs(root, dir); err != nil {
		return err
	}
	if info, err := root.Lstat(marker); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("disable marker is not a regular file: %s", filepath.Join(root.Name(), marker))
		}
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	file, err := root.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0o644)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return errors.Join(fmt.Errorf("disable marker is not a regular file: %s",
			filepath.Join(root.Name(), marker)), err, file.Close())
	}
	if os.Geteuid() == 0 {
		err = file.Chown(containerUID, containerUID)
	}
	return errors.Join(err, file.Close())
}

// EnsureCorePackage requires the instance package lock and borrows database.
// A nil database selects filesystem state before the instance store exists.
// Completion repairs only core's receipt; unrelated incomplete packages refuse.
func EnsureCorePackage(cfg *core.Config, database *store.Store) error {
	if err := CheckPendingPackageAdoption(database, nil); err != nil {
		return err
	}
	snapshots, receipts, err := PrepareCorePackages(cfg, database)
	if err != nil {
		return err
	}
	return ActivatePackages(cfg.ProjectRoot, "", database, snapshots, receipts)
}

// PrepareCorePackages requires the package lock and borrows database. It prepares
// immutable artifacts without changing selections or installed receipts.
func PrepareCorePackages(cfg *core.Config, database *store.Store) ([]PackageSnapshot, []store.InstalledPackage, error) {
	dataDir := cfg.ProjectRoot
	if err := PreparePackagePaths(dataDir); err != nil {
		return nil, nil, err
	}
	selected, err := ReadPackageSelection(dataDir, "")
	if err != nil {
		return nil, nil, err
	}
	var receipt store.InstalledPackage
	if database != nil {
		var found bool
		receipt, found, err = database.InstalledPackage("", CorePackageName)
		if err != nil {
			return nil, nil, err
		}
		if found && receipt.Source != corePackageSource {
			return nil, nil, fmt.Errorf("required core receipt claims source %q; restore the release source %q before upgrading core",
				receipt.Source, corePackageSource)
		}
	}
	snapshot, err := prepareCorePackage(cfg)
	if err != nil {
		return nil, nil, err
	}
	snapshots := []PackageSnapshot{snapshot}
	receipt.Source, receipt.Revision = corePackageSource, snapshot.Digest
	receipts := []store.InstalledPackage{receipt}
	for _, other := range selected {
		if other.Name == CorePackageName {
			continue
		}
		if database != nil {
			receipt, found, err := database.InstalledPackage("", other.Name)
			if err != nil {
				return nil, nil, err
			}
			if !found || receipt.Source == "" || receipt.Revision == "" || receipt.InstalledAt == "" ||
				!slices.Equal(receipt.Manifest[PackageSourceArtifact], []string{other.Digest}) ||
				receipt.AssetHashes[PackageSourceArtifact] != other.Digest ||
				HasPendingSettings(receipt.AssetHashes) {
				return nil, nil, fmt.Errorf("incomplete package operation: active %s has no matching source receipt; "+
					"retry its install before upgrading core", other.Name)
			}
			dir, err := ResolvePackage(dataDir, other)
			if err != nil {
				return nil, nil, err
			}
			names, err := ReadPackageSkills(dir)
			if err != nil {
				return nil, nil, err
			}
			if !slices.Equal(receipt.Manifest["skill"], names) {
				return nil, nil, fmt.Errorf("incomplete package operation: active %s has no matching skill receipt; "+
					"retry its install before upgrading core", other.Name)
			}
			receipts = append(receipts, receipt)
		}
		snapshots = append(snapshots, other)
	}
	if err := checkPackageSelection(cfg, "", database, snapshots, snapshot); err != nil {
		return nil, nil, err
	}
	return snapshots, receipts, nil
}

func prepareCorePackage(cfg *core.Config) (PackageSnapshot, error) {
	if cfg.EffectiveAppSrcDir() == "" {
		return PackageSnapshot{}, fmt.Errorf("required core source is not configured; " +
			"set APP_SRC_DIR to the selected release")
	}
	source := filepath.Join(cfg.EffectiveAppSrcDir(), corePackageSource)
	snapshot, err := preparePackage(cfg.ProjectRoot, source,
		corePackageRoots)
	if err != nil {
		return snapshot, fmt.Errorf("required core source %s: %w; repair the selected release before retrying", source, err)
	}
	return snapshot, nil
}

// CheckPackageSelection validates canonical package and skill names against the
// exact target and system selection. System changes check every registered group.
// It requires the package lock and reserves the configured release's core skills.
// Writable-home ownership is a separate preflight before payload writes or mounts.
func CheckPackageSelection(cfg *core.Config, target string, database *store.Store,
	snapshots []PackageSnapshot) error {
	if _, err := packageTarget(target); err != nil {
		return err
	}
	required, err := prepareCorePackage(cfg)
	if err != nil {
		return err
	}
	return checkPackageSelection(cfg, target, database, snapshots, required)
}

func checkPackageSelection(cfg *core.Config, target string, database *store.Store,
	snapshots []PackageSnapshot, required PackageSnapshot) error {
	seen, err := readPackageNames(cfg.ProjectRoot, snapshots)
	if err != nil {
		return err
	}
	if target != "" && seen["package:"+CorePackageName] != "" {
		return fmt.Errorf("core is required system-wide; use packages upgrade core")
	}
	if target == "" {
		seen["package:"+required.Name] = required.Name
		if err := addPackageSkills(cfg.ProjectRoot, required, seen); err != nil {
			return err
		}
	}
	folders := []string{""}
	if target == "" {
		folders = nil
		if database != nil {
			var err error
			folders, err = database.GroupFolders(0)
			if err != nil {
				return err
			}
		}
	}
	for _, folder := range folders {
		selected, err := ReadPackageSelection(cfg.ProjectRoot, folder)
		if err != nil {
			return err
		}
		other, err := readPackageNames(cfg.ProjectRoot, selected)
		if err != nil {
			return err
		}
		if folder == "" {
			other["package:"+required.Name] = required.Name
			if err := addPackageSkills(cfg.ProjectRoot, required, other); err != nil {
				return err
			}
		} else if other["package:"+CorePackageName] != "" {
			return fmt.Errorf("group %s selects required system package core; "+
				"remove its group selection before upgrading core", folder)
		}
		for name, owner := range other {
			if _, found := seen[name]; found {
				return fmt.Errorf("%s also belongs to target %q package %s", name, folder, owner)
			}
		}
	}
	return nil
}

func readPackageNames(dataDir string, snapshots []PackageSnapshot) (map[string]string, error) {
	names := map[string]string{}
	for _, snapshot := range snapshots {
		key := "package:" + snapshot.Name
		if _, found := names[key]; found {
			return nil, fmt.Errorf("duplicate package %q", snapshot.Name)
		}
		names[key] = snapshot.Name
		if err := addPackageSkills(dataDir, snapshot, names); err != nil {
			return nil, err
		}
	}
	return names, nil
}

func addPackageSkills(dataDir string, snapshot PackageSnapshot, names map[string]string) error {
	dir, err := ResolvePackage(dataDir, snapshot)
	if err != nil {
		return err
	}
	skills, err := ReadPackageSkills(dir)
	if err != nil {
		return err
	}
	for _, skill := range skills {
		key := "skill:" + skill
		if owner, found := names[key]; found && owner != snapshot.Name {
			return fmt.Errorf("skill %q is supplied by both %s and %s", skill, owner, snapshot.Name)
		}
		names[key] = snapshot.Name
	}
	return nil
}

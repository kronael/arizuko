package container

import (
	"fmt"
	"path/filepath"
	"slices"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

const CorePackageName = "core"

const corePackageSource = "ant"

// EnsureCorePackage requires the instance package lock and borrows database.
// A nil database selects filesystem state before the instance store exists.
// Completion repairs only core's receipt; unrelated incomplete packages refuse.
func EnsureCorePackage(cfg *core.Config, database *store.Store) error {
	dataDir := cfg.ProjectRoot
	selected, err := ReadPackageSelection(dataDir, "")
	if err != nil {
		return err
	}
	var receipt store.InstalledPackage
	if database != nil {
		var found bool
		receipt, found, err = database.InstalledPackage("", CorePackageName)
		if err != nil {
			return err
		}
		if found && receipt.Source != corePackageSource {
			return fmt.Errorf("required core receipt claims source %q; restore the release source %q before upgrading core",
				receipt.Source, corePackageSource)
		}
	}
	snapshot, err := prepareCorePackage(cfg)
	if err != nil {
		return err
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
				return err
			}
			if !found || receipt.Source == "" || receipt.Revision == "" || receipt.InstalledAt == "" ||
				!slices.Equal(receipt.Manifest[PackageSourceArtifact], []string{other.Digest}) ||
				receipt.AssetHashes[PackageSourceArtifact] != other.Digest ||
				HasPendingSettings(receipt.AssetHashes) {
				return fmt.Errorf("incomplete package operation: active %s has no matching source receipt; "+
					"retry its install before upgrading core", other.Name)
			}
			dir, err := ResolvePackage(dataDir, other)
			if err != nil {
				return err
			}
			names, err := ReadPackageSkills(dir)
			if err != nil {
				return err
			}
			if !slices.Equal(receipt.Manifest["skill"], names) {
				return fmt.Errorf("incomplete package operation: active %s has no matching skill receipt; "+
					"retry its install before upgrading core", other.Name)
			}
			receipts = append(receipts, receipt)
		}
		snapshots = append(snapshots, other)
	}
	if err := checkPackageSelection(cfg, "", database, snapshots, snapshot); err != nil {
		return err
	}
	return ActivatePackages(dataDir, "", database, snapshots, receipts)
}

func prepareCorePackage(cfg *core.Config) (PackageSnapshot, error) {
	if cfg.EffectiveAppSrcDir() == "" {
		return PackageSnapshot{}, fmt.Errorf("required core source is not configured; " +
			"set APP_SRC_DIR to the selected release")
	}
	source := filepath.Join(cfg.EffectiveAppSrcDir(), corePackageSource)
	snapshot, err := preparePackage(cfg.ProjectRoot, source,
		[]string{ManifestFile, "CLAUDE.md", "skills", "output-styles"})
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

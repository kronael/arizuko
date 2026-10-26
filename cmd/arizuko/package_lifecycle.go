package main

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/store"
)

const manifestArtifact = container.PackageSourceArtifact

type packageCandidate struct {
	snapshot container.PackageSnapshot
	dir      string
	source   string
	revision string
}

func lockPackageOperation(dataDir string) *os.File {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		die("Failed: prepare package data directory: %v", err)
	}
	lock, err := container.LockPackages(dataDir)
	if err != nil {
		die("Failed: lock packages: %v", err)
	}
	return lock
}

func preparePackageCandidate(dataDir, dir, source, revision string) (packageCandidate, error) {
	snapshot, err := container.PreparePackage(dataDir, dir)
	if err != nil {
		return packageCandidate{}, fmt.Errorf("prepare %s: %w", source, err)
	}
	if snapshot.Name == container.CorePackageName {
		return packageCandidate{}, fmt.Errorf("core is required system-wide from the configured release; use packages upgrade core")
	}
	artifact, err := container.ResolvePackage(dataDir, snapshot)
	if revision == "local" {
		revision = snapshot.Digest
	}
	return packageCandidate{snapshot, artifact, source, revision}, err
}

func packageSkills(dir string) ([]string, error) {
	return container.ReadPackageSkills(dir)
}

func checkPackageCandidates(dataDir, target string, rdb *routd.DB, candidates []packageCandidate) error {
	if target != "" && (target == "." || !groupfolder.IsValidFolder(target)) {
		return fmt.Errorf("invalid package group %q", target)
	}
	seen := map[string]string{}
	selected := make([]container.PackageSnapshot, len(candidates))
	for i, candidate := range candidates {
		selected[i] = candidate.snapshot
		names, err := packageSkills(candidate.dir)
		if err != nil {
			return err
		}
		for _, name := range names {
			seen["skill:"+name] = candidate.source
		}
		if target != "" {
			files, _ := readFragmentAssets(candidate.dir, candidate.snapshot.Name)
			if len(files) > 0 {
				return fmt.Errorf("group package %s supplies system service/resource payloads", candidate.snapshot.Name)
			}
			if err := container.CheckProductAssets(candidate.snapshot.Name, candidate.dir); err != nil {
				return err
			}
		}
	}
	cfg, err := core.LoadConfigFrom(dataDir)
	if err != nil {
		return err
	}
	if err := container.CheckPackageSelection(cfg, target, packageStore(rdb), selected); err != nil {
		return err
	}
	if target == "" {
		for key := range seen {
			name, skill := strings.CutPrefix(key, "skill:")
			if !skill {
				continue
			}
			path := filepath.Join(dataDir, "skills", name)
			if _, err := os.Lstat(path); err == nil {
				return fmt.Errorf("packaged skill %q conflicts with %s; resolve the copied or custom skill first", name, path)
			} else if !os.IsNotExist(err) {
				return err
			}
		}
		return checkSystemPackageCollisions(cfg, rdb, seen)
	}
	return checkCustomPackageCollisions(cfg, target, seen)
}

func checkCustomPackageCollisions(cfg *core.Config, folder string, seen map[string]string) error {
	for key := range seen {
		name, skill := strings.CutPrefix(key, "skill:")
		if !skill {
			continue
		}
		for _, rel := range []string{filepath.Join(".claude", "skills", name), filepath.Join("skills", name)} {
			path := filepath.Join(cfg.GroupsDir, folder, rel)
			if _, err := os.Lstat(path); err == nil {
				return fmt.Errorf("packaged skill %q conflicts with %s; resolve the copied or custom skill first", name, path)
			} else if !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}

func checkSystemPackageCollisions(cfg *core.Config, rdb *routd.DB, seen map[string]string) error {
	folders, err := packageStore(rdb).GroupFolders(0)
	if err != nil {
		return err
	}
	for _, folder := range folders {
		if err := checkCustomPackageCollisions(cfg, folder, seen); err != nil {
			return err
		}
	}
	return nil
}

func activatePackageCandidates(dataDir, target string, rdb *routd.DB,
	candidates []packageCandidate, receipts []routd.InstalledPackage) error {
	selected := make([]container.PackageSnapshot, len(candidates))
	adapted := slices.Clone(receipts)
	for i, candidate := range candidates {
		selected[i] = candidate.snapshot
		adapted[i].Source, adapted[i].Revision = candidate.source, candidate.revision
	}
	return container.ActivatePackages(dataDir, target, store.NewOver(rdb.Obj()), selected, adapted)
}

func existingPackageCandidates(dataDir, target string, rdb *routd.DB) ([]packageCandidate, []routd.InstalledPackage, error) {
	return readPackageCandidates(dataDir, target, "", rdb)
}

func readPackageCandidates(dataDir, target, removing string,
	rdb *routd.DB) ([]packageCandidate, []routd.InstalledPackage, error) {
	records, err := rdb.InstalledPackages()
	if err != nil {
		return nil, nil, err
	}
	for _, record := range records {
		if record.Folder == target && record.Name != removing && container.HasPendingSettings(record.AssetHashes) {
			return nil, nil, fmt.Errorf("incomplete package operation: %s has pending settings; retry its apply or removal", record.Name)
		}
	}
	selected, err := container.ReadPackageSelection(dataDir, target)
	if err != nil {
		return nil, nil, err
	}
	var candidates []packageCandidate
	var receipts []routd.InstalledPackage
	for _, snapshot := range selected {
		receipt, found, err := rdb.InstalledPackage(target, snapshot.Name)
		if err != nil {
			return nil, nil, err
		}
		matching := found && slices.Equal(receipt.Manifest[manifestArtifact], []string{snapshot.Digest}) &&
			receipt.AssetHashes[manifestArtifact] == snapshot.Digest && !container.HasPendingSettings(receipt.AssetHashes)
		recovering := found && snapshot.Name == removing && container.HasPendingSettings(receipt.AssetHashes)
		if !matching && !recovering {
			return nil, nil, fmt.Errorf("incomplete package operation: active %s has no matching receipt; retry its install or products apply", snapshot.Name)
		}
		dir, err := container.ResolvePackage(dataDir, snapshot)
		if err != nil {
			return nil, nil, err
		}
		candidates = append(candidates, packageCandidate{snapshot, dir, receipt.Source, receipt.Revision})
		receipts = append(receipts, receipt)
	}
	return candidates, receipts, nil
}

func checkPendingSettings(dataDir, target string, records []routd.InstalledPackage,
	candidates []packageCandidate) error {
	for _, record := range records {
		if record.Folder != target || !container.HasPendingSettings(record.AssetHashes) {
			continue
		}
		digest := record.AssetHashes[container.PendingSettingsArtifact]
		source := record.AssetHashes[container.PendingSettingsSource]
		if digest == "" || source == "" {
			return fmt.Errorf("incomplete package operation: %s has unbound pending settings", record.Name)
		}
		_, err := container.ResolvePackage(dataDir, container.PackageSnapshot{Name: record.Name, Digest: digest})
		if err != nil {
			return fmt.Errorf("incomplete package operation: %s pending settings: %w", record.Name, err)
		}
		for _, candidate := range candidates {
			if candidate.snapshot.Name == record.Name &&
				(candidate.snapshot.Digest != digest || sha256hex([]byte(candidate.source)) != source) {
				return fmt.Errorf("incomplete package operation: %s has pending settings; retry the same source or remove it", record.Name)
			}
		}
	}
	return nil
}

func prepareSettingsReceipts(target string, rdb *routd.DB,
	candidates []packageCandidate, blended []container.Blended) error {
	for i, candidate := range candidates {
		pending := map[string]string{}
		for key, hash := range blended[i].AssetHashes {
			if strings.HasPrefix(key, container.PendingSettingPrefix) {
				pending[key] = hash
			}
		}
		if len(pending) == 0 {
			continue
		}
		record, _, err := rdb.InstalledPackage(target, candidate.snapshot.Name)
		if err != nil {
			return err
		}
		record.Folder, record.Name = target, candidate.snapshot.Name
		if record.Source == "" {
			record.Source = candidate.source
		}
		if record.AssetHashes == nil {
			record.AssetHashes = map[string]string{}
		}
		pending[container.PendingSettingsArtifact] = candidate.snapshot.Digest
		pending[container.PendingSettingsSource] = sha256hex([]byte(candidate.source))
		maps.Copy(record.AssetHashes, pending)
		if err := rdb.PutInstalledPackage(record); err != nil {
			return fmt.Errorf("incomplete package operation: %s settings preparation failed; payloads unchanged: %w", record.Name, err)
		}
	}
	return nil
}

func installManagedPackage(dataDir string, rdb *routd.DB, candidate packageCandidate, bound []string) error {
	if err := container.CheckPendingPackageAdoption(packageStore(rdb), nil); err != nil {
		return err
	}
	selected, err := container.ReadPackageSelection(dataDir, "")
	if err != nil {
		return err
	}
	var candidates []packageCandidate
	var receipts []routd.InstalledPackage
	for _, snapshot := range selected {
		if snapshot.Name == candidate.snapshot.Name {
			continue
		}
		dir, err := container.ResolvePackage(dataDir, snapshot)
		if err != nil {
			return err
		}
		receipt, found, err := rdb.InstalledPackage("", snapshot.Name)
		if err != nil {
			return err
		}
		if !found || receipt.AssetHashes[manifestArtifact] != snapshot.Digest {
			return fmt.Errorf("incomplete package operation for %s: repair its receipt first", snapshot.Name)
		}
		candidates = append(candidates, packageCandidate{snapshot, dir, receipt.Source, receipt.Revision})
		receipts = append(receipts, receipt)
	}
	candidates = append(candidates, candidate)
	if err := checkPackageCandidates(dataDir, "", rdb, candidates); err != nil {
		return err
	}
	name := candidate.snapshot.Name
	receipt, _, err := rdb.InstalledPackage("", name)
	if err != nil {
		return err
	}
	svcDir := filepath.Join(dataDir, "services")
	if dirty := dirtyAssets(svcDir, receipt); len(dirty) > 0 {
		return fmt.Errorf("%s has locally edited assets: %s", name, strings.Join(dirty, ", "))
	}
	files, hashes := readFragmentAssets(candidate.dir, name)
	var frags []pkgFragment
	if len(receipt.Manifest[manifestArtifact]) == 0 {
		frags = parsePkgManifests(name, candidate.dir, files, bound, "")
	}
	if err := checkSourcedFiles(svcDir, name, candidate.dir, files, receipt.AssetHashes); err != nil {
		return err
	}
	if len(files) > 0 {
		provisionPkgEnv(dataDir, filepath.Join(candidate.dir, name+".yaml"), name)
		if err := os.MkdirAll(svcDir, 0o755); err != nil {
			return err
		}
		for _, file := range files {
			data, err := os.ReadFile(filepath.Join(candidate.dir, file))
			if err != nil {
				return err
			}
			if err := writeFileAtomic(filepath.Join(svcDir, file), data); err != nil {
				return err
			}
		}
	}
	if receipt.Manifest == nil {
		receipt.Manifest = map[string][]string{}
	}
	if len(frags) > 0 && receipt.AssetHashes[manifestArtifact] != candidate.snapshot.Digest {
		rows, err := installPkgRows(dataDir, rdb, name, frags, pkgManifestDigest(candidate.dir, files))
		if err != nil {
			dropFiles(svcDir, files)
			return err
		}
		maps.Copy(receipt.Manifest, rows)
	}
	for key := range receipt.AssetHashes {
		if file, owned := strings.CutPrefix(key, "file:"); owned {
			if _, shipped := hashes[key]; !shipped {
				if err := os.Remove(filepath.Join(svcDir, file)); err != nil && !os.IsNotExist(err) {
					return err
				}
			}
		}
	}
	receipt.Manifest[manifestFiles] = files
	receipt.AssetHashes = hashes
	receipts = append(receipts, receipt)
	return activatePackageCandidates(dataDir, "", rdb, candidates, receipts)
}

func removeManagedPackage(dataDir, target, name string, rdb *routd.DB) error {
	if err := container.CheckPendingPackageAdoption(packageStore(rdb), nil); err != nil {
		return err
	}
	if name == container.CorePackageName {
		return fmt.Errorf("core is required system-wide and cannot be removed; use packages upgrade core")
	}
	candidates, receipts, err := readPackageCandidates(dataDir, target, name, rdb)
	if err != nil {
		return err
	}
	var removed routd.InstalledPackage
	for i, candidate := range candidates {
		if candidate.snapshot.Name == name {
			removed = receipts[i]
			candidates = slices.Delete(candidates, i, i+1)
			receipts = slices.Delete(receipts, i, i+1)
			break
		}
	}
	if removed.Name == "" {
		var found bool
		removed, found, err = rdb.InstalledPackage(target, name)
		if err != nil || !found || (len(removed.Manifest[manifestArtifact]) == 0 &&
			!container.HasPendingSettings(removed.AssetHashes)) {
			return fmt.Errorf("package %s is not active", name)
		}
	}
	if err := checkPendingSettings(dataDir, target, []routd.InstalledPackage{removed}, nil); err != nil {
		return err
	}
	if target == "" {
		if dirty := dirtyAssets(filepath.Join(dataDir, "services"), removed); len(dirty) > 0 {
			return fmt.Errorf("%s has locally edited assets: %s", name, strings.Join(dirty, ", "))
		}
	}
	if target != "" {
		cfg, err := core.LoadConfigFrom(dataDir)
		if err != nil {
			return err
		}
		groupDir := filepath.Join(cfg.GroupsDir, target)
		mix, _ := readProductMix(groupDir)
		index := -1
		for i, entry := range mix.Product {
			if entry.Source == removed.Source ||
				sha256hex([]byte(entry.Source)) == removed.AssetHashes[container.PendingSettingsSource] {
				if index >= 0 {
					return fmt.Errorf("source %s occurs more than once; edit %s explicitly", removed.Source, container.ManifestFile)
				}
				index = i
			}
		}
		if index >= 0 {
			path := filepath.Join(groupDir, container.ManifestFile)
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			updated, err := editProductSelection(data, index, "")
			if err != nil {
				return err
			}
			if err := writeFileAtomic(path, updated); err != nil {
				return err
			}
		}
		prior := map[string]map[string]string{removed.Name: removed.AssetHashes}
		products := make([]container.Product, len(candidates))
		for i, candidate := range candidates {
			prior[receipts[i].Name] = receipts[i].AssetHashes
			products[i] = container.Product{Name: candidate.snapshot.Name,
				Dir: candidate.dir, Snapshot: &candidate.snapshot}
		}
		settings, err := container.BlendProductSettings(groupDir, products, prior)
		if err != nil {
			return err
		}
		for i, settings := range settings {
			receipts[i].Manifest["setting"] = settings.Manifest["setting"]
			for key := range receipts[i].AssetHashes {
				if strings.HasPrefix(key, "setting:") {
					delete(receipts[i].AssetHashes, key)
				}
			}
			maps.Copy(receipts[i].AssetHashes, settings.AssetHashes)
		}
	}
	if err := activatePackageCandidates(dataDir, target, rdb, candidates, receipts); err != nil {
		return err
	}
	if target == "" {
		removePkgRows(dataDir, rdb, removed)
		for _, file := range removed.Manifest[manifestFiles] {
			if err := os.Remove(filepath.Join(dataDir, "services", file)); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("incomplete package removal: %s is inactive: %w", name, err)
			}
		}
	}
	_, err = rdb.DeleteInstalledPackage(target, name)
	if err != nil {
		return fmt.Errorf("incomplete package removal: %s is inactive; retry removal: %w", name, err)
	}
	return nil
}

func parsePackageGroup(args []string) ([]string, string) {
	var rest []string
	group := ""
	for i := 0; i < len(args); i++ {
		flag, value, inline := strings.Cut(args[i], "=")
		if flag != "--group" {
			rest = append(rest, args[i])
			continue
		}
		if !inline {
			i++
			if i == len(args) {
				die("Failed: --group needs one exact folder")
			}
			value = args[i]
		}
		if group != "" || value == "" || value == "." || !groupfolder.IsValidFolder(value) || strings.ContainsAny(value, "*?[\\\r\n") {
			die("Failed: --group needs one exact folder, got %q", value)
		}
		group = value
	}
	return rest, group
}

func isManifestSource(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, container.ManifestFile))
	if err == nil {
		return true
	}
	if !errors.Is(err, os.ErrNotExist) {
		die("Failed: read source manifest: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "skills")); err == nil {
		die("Failed: skill package %s needs %s with a package name", dir, container.ManifestFile)
	}
	return false
}

package main

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/store"
)

type packageCandidate struct {
	name, dir, source, revision string
}

func preparePackageCandidate(dir, source, revision string) (packageCandidate, error) {
	manifest, err := container.ReadProductManifest(dir)
	if err == nil && !pkgNameRE.MatchString(manifest.Name) {
		err = fmt.Errorf("%s declares invalid package name %q", container.ManifestFile, manifest.Name)
	}
	if err == nil && manifest.Name == container.CorePackageName {
		err = fmt.Errorf("core comes from the configured release")
	}
	return packageCandidate{manifest.Name, dir, source, revision}, err
}

func installManagedPackage(dataDir, target string, rdb *routd.DB,
	candidate packageCandidate, bound []string) error {
	records, err := rdb.InstalledPackages()
	if err != nil {
		return err
	}
	for _, record := range records {
		if record.Name == candidate.name && record.Source != candidate.source {
			return fmt.Errorf("package %s already uses source %s", record.Name, record.Source)
		}
	}
	files, hashes := readFragmentAssets(candidate.dir, candidate.name)
	manifest, err := container.ReadProductManifest(candidate.dir)
	if err != nil {
		return err
	}
	hasRows := slices.ContainsFunc(manifest.Documents, func(document resreg.ManifestDocument) bool {
		return len(document.Rows) > 0
	})
	if target != "" && (len(files) > 0 || hasRows) {
		return fmt.Errorf("group packages cannot install services or resources")
	}
	record, _, err := rdb.InstalledPackage(target, candidate.name)
	if err != nil {
		return err
	}
	svcDir := filepath.Join(dataDir, "services")
	if dirty := dirtyAssets(svcDir, record); target == "" && len(dirty) > 0 {
		return fmt.Errorf("locally edited service assets: %s", strings.Join(dirty, ", "))
	}
	if err := checkSourcedFiles(svcDir, candidate.name, candidate.dir, files, record.AssetHashes); err != nil {
		return err
	}
	var fragments []pkgFragment
	var applied map[string][]string
	if target == "" {
		if record.Name == "" {
			fragments = parsePkgManifests(candidate.name, candidate.dir, []string{container.ManifestFile}, bound, "")
		}
		if len(files) > 0 {
			provisionPkgEnv(dataDir, filepath.Join(candidate.dir, container.ManifestFile), candidate.name)
		}
	}
	record.Folder, record.Source, record.Revision = target, candidate.source, candidate.revision
	_, err = container.InstallPackage(dataDir, candidate.dir, true, store.NewOver(rdb.Obj()), record)
	if err != nil {
		return err
	}
	record, _, err = rdb.InstalledPackage(target, candidate.name)
	if err != nil {
		return err
	}
	if target == "" {
		if len(fragments) > 0 {
			rows, err := installPkgRows(dataDir, rdb, candidate.name, fragments,
				pkgManifestDigest(candidate.dir, []string{container.ManifestFile}))
			if err != nil {
				return err
			}
			maps.Copy(record.Manifest, rows)
			applied = rows
		}
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
	for _, file := range record.Manifest[manifestFiles] {
		if !slices.Contains(files, file) {
			if err := os.Remove(filepath.Join(svcDir, file)); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	record.Manifest[manifestFiles] = files
	record.AssetHashes = hashes
	if err := rdb.PutInstalledPackage(record); err != nil {
		return err
	}
	reportPackageInstall(candidate.name, candidate.revision, files, applied, fragments)
	return nil
}

func removeManagedPackage(dataDir, target, name string, rdb *routd.DB) error {
	if name == container.CorePackageName {
		return fmt.Errorf("core is required and cannot be removed")
	}
	record, found, err := rdb.InstalledPackage(target, name)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("package %s is not installed", name)
	}
	if target == "" {
		removePkgRows(dataDir, rdb, record)
		for _, file := range record.Manifest[manifestFiles] {
			if err := os.Remove(filepath.Join(dataDir, "services", file)); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	} else {
		cfg, err := core.LoadConfigFrom(dataDir)
		if err != nil {
			return err
		}
		dir := filepath.Join(cfg.GroupsDir, target)
		mix, _ := readProductMix(dir)
		mix.Packages = slices.DeleteFunc(mix.Packages, func(entry container.ProductSource) bool {
			return entry.Source == record.Source
		})
		if err := writeProductMix(dir, mix); err != nil {
			return err
		}
	}
	_, err = rdb.DeleteInstalledPackage(target, name)
	return err
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

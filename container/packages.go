package container

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"time"

	"github.com/kronael/arizuko/store"
)

const CorePackageName = "core"

var packageNameRE = regexp.MustCompile(PackageNamePattern)

func ReadPackageSkills(dir string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(dir, "skills"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if !skillNameRe.MatchString(entry.Name()) {
			return nil, fmt.Errorf("invalid packaged skill %q", entry.Name())
		}
		if _, err := os.Stat(filepath.Join(dir, "skills", entry.Name(), "SKILL.md")); err != nil {
			return nil, err
		}
		names = append(names, entry.Name())
	}
	return names, nil
}

func InstallPackage(dataDir, source string, build bool,
	database *store.Store, record store.InstalledPackage) (ProductManifest, error) {
	manifest, err := ReadProductManifest(source)
	if err != nil {
		return manifest, err
	}
	if !packageNameRE.MatchString(manifest.Name) {
		return manifest, fmt.Errorf("invalid package name %q", manifest.Name)
	}
	if _, err := ReadPackageSkills(source); err != nil {
		return manifest, err
	}
	dir := filepath.Join(dataDir, "packages", manifest.Name)
	if err := os.RemoveAll(dir); err != nil {
		return manifest, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return manifest, err
	}
	for _, name := range []string{ManifestFile, "CLAUDE.md", "skills", "hooks", "bin", ".claude", ".codex"} {
		path := filepath.Join(source, name)
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return manifest, err
		}
		if info.IsDir() {
			err = os.CopyFS(filepath.Join(dir, name), os.DirFS(path))
		} else {
			err = copyPackageFile(path, filepath.Join(dir, name), info.Mode())
		}
		if err != nil {
			return manifest, err
		}
	}
	if build && manifest.Install != "" {
		command := exec.Command("sh", "-c", manifest.Install)
		command.Dir = source
		command.Env = append(os.Environ(), "PREFIX="+dir)
		command.Stdout, command.Stderr = os.Stdout, os.Stderr
		if err := command.Run(); err != nil {
			return manifest, fmt.Errorf("install %s: %w", manifest.Name, err)
		}
	}
	names, err := ReadPackageSkills(dir)
	if err != nil {
		return manifest, err
	}
	if names == nil {
		names = []string{}
	}
	if record.Manifest == nil {
		record.Manifest = map[string][]string{}
	}
	record.Name = manifest.Name
	record.Manifest["skill"] = names
	record.InstalledAt = time.Now().UTC().Format(time.RFC3339)
	return manifest, database.PutInstalledPackage(record)
}

func copyPackageFile(source, dest string, mode fs.FileMode) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return os.WriteFile(dest, data, mode.Perm())
}

type PackageSkill struct {
	Name string
	Dir  string
}

type PackageInventory struct {
	Core     string
	Packages []Product
	Skills   []PackageSkill
}

func ReadPackageInventory(dataDir string, selected []string) (PackageInventory, error) {
	var inventory PackageInventory
	seen := map[string]bool{}
	skills := map[string]int{}
	for _, name := range selected {
		if !packageNameRE.MatchString(name) {
			return inventory, fmt.Errorf("invalid package %q", name)
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		dir := filepath.Join(dataDir, "packages", name)
		manifest, err := ReadProductManifest(dir)
		if err != nil {
			return inventory, err
		}
		if manifest.Name != name {
			return inventory, fmt.Errorf("package %q declares name %q", name, manifest.Name)
		}
		inventory.Packages = append(inventory.Packages, Product{Name: name, Dir: dir})
		if name == CorePackageName {
			inventory.Core = dir
		}
		names, err := ReadPackageSkills(dir)
		if err != nil {
			return inventory, err
		}
		for _, skill := range names {
			asset := PackageSkill{skill, filepath.Join(dir, "skills", skill)}
			if index, exists := skills[skill]; exists {
				inventory.Skills[index] = asset
				continue
			}
			skills[skill] = len(inventory.Skills)
			inventory.Skills = append(inventory.Skills, asset)
		}
	}
	if inventory.Core == "" {
		return inventory, fmt.Errorf("required core package is missing")
	}
	return inventory, nil
}

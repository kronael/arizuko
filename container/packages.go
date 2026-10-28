package container

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"

	"github.com/kronael/arizuko/chanlib"
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

func InstallPackage(dataDir, source string, build bool) (ProductManifest, []string, error) {
	manifest, err := ReadProductManifest(source)
	if err != nil {
		return manifest, nil, err
	}
	if !packageNameRE.MatchString(manifest.Name) {
		return manifest, nil, fmt.Errorf("invalid package name %q", manifest.Name)
	}
	if _, err := ReadPackageSkills(source); err != nil {
		return manifest, nil, err
	}
	dir := filepath.Join(dataDir, "packages", manifest.Name)
	if err := os.RemoveAll(dir); err != nil {
		return manifest, nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return manifest, nil, err
	}
	for _, name := range []string{ManifestFile, "CLAUDE.md", "skills", "hooks", "bin", ".claude", ".codex"} {
		path := filepath.Join(source, name)
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return manifest, nil, err
		}
		if info.IsDir() {
			err = os.CopyFS(filepath.Join(dir, name), os.DirFS(path))
		} else {
			err = chanlib.CopyFile(path, filepath.Join(dir, name))
		}
		if err != nil {
			return manifest, nil, err
		}
	}
	if build && manifest.Install != "" {
		command := exec.Command("sh", "-c", manifest.Install)
		command.Dir = source
		command.Env = append(os.Environ(), "PREFIX="+dir)
		command.Stdout, command.Stderr = os.Stdout, os.Stderr
		if err := command.Run(); err != nil {
			return manifest, nil, fmt.Errorf("install %s: %w", manifest.Name, err)
		}
	}
	names, err := ReadPackageSkills(dir)
	if err != nil {
		return manifest, nil, err
	}
	if names == nil {
		names = []string{}
	}
	return manifest, names, nil
}

// PackageAsset is one path an installed package mounts into a turn:
// skills/<name>, hooks/<name> or bin/<name>.
type PackageAsset struct {
	Path    string
	Package string
}

// PackageInventory is what a turn mounts from the selected packages, in
// selection order. A later package's asset replaces an earlier one's at the
// same path, and Overrides names each replacement.
type PackageInventory struct {
	Core      string
	Packages  []Product
	Assets    []PackageAsset
	Overrides []string
}

func ReadPackageInventory(dataDir string, selected []string) (PackageInventory, error) {
	var inventory PackageInventory
	seen := map[string]bool{}
	index := map[string]int{}
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
		skills, err := ReadPackageSkills(dir)
		if err != nil {
			return inventory, err
		}
		var paths []string
		for _, skill := range skills {
			paths = append(paths, "skills/"+skill)
		}
		for _, kind := range []string{"hooks", "bin"} {
			entries, err := os.ReadDir(filepath.Join(dir, kind))
			if err != nil && !os.IsNotExist(err) {
				return inventory, err
			}
			for _, entry := range entries {
				paths = append(paths, kind+"/"+entry.Name())
			}
		}
		for _, path := range paths {
			if i, ok := index[path]; ok {
				inventory.Overrides = append(inventory.Overrides,
					fmt.Sprintf("%s overrides %s on %s", name, inventory.Assets[i].Package, path))
				inventory.Assets[i].Package = name
				continue
			}
			index[path] = len(inventory.Assets)
			inventory.Assets = append(inventory.Assets, PackageAsset{path, name})
		}
	}
	if inventory.Core == "" {
		return inventory, fmt.Errorf("required core package is missing")
	}
	return inventory, nil
}

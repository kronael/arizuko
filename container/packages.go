package container

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/kronael/arizuko/chanlib"
)

const CorePackageName = "core"

var packageNameRE = regexp.MustCompile(PackageNamePattern)

// ReadPackageSkills lists the skill directories of an installed package. A
// directory without SKILL.md is an error. InstallPackage reads a source tree
// through git instead: in a checkout only tracked directories count.
func ReadPackageSkills(dir string) ([]string, error) {
	return readSkills(dir, nil)
}

func readSkills(dir string, tracked map[string]bool) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(dir, "skills"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() || (tracked != nil && !tracked[entry.Name()]) {
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

// trackedSkillDirs returns the skill directories git tracks under dir, or nil
// when every directory counts: dir is not in a git work tree, or git is not
// installed and no checkout encloses dir (the runtime image has no git). Any
// other git failure is an error.
func trackedSkillDirs(dir string) (map[string]bool, error) {
	cmd := exec.Command("git", "-C", dir, "ls-files", "-z", "--", "skills")
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.Is(err, exec.ErrNotFound):
		if isInCheckout(dir) {
			return nil, fmt.Errorf("list tracked skills in checkout %s: %w", dir, err)
		}
		return nil, nil
	case errors.As(err, &exitErr) && strings.Contains(string(exitErr.Stderr), "not a git repository"):
		return nil, nil
	case exitErr != nil:
		return nil, fmt.Errorf("list tracked skills in %s: %w: %s", dir, err, bytes.TrimSpace(exitErr.Stderr))
	default:
		return nil, fmt.Errorf("list tracked skills in %s: %w", dir, err)
	}
	tracked := map[string]bool{}
	for _, path := range strings.Split(string(out), "\x00") {
		if name, _, found := strings.Cut(strings.TrimPrefix(path, "skills/"), "/"); found {
			tracked[name] = true
		}
	}
	if len(tracked) == 0 {
		return nil, nil
	}
	return tracked, nil
}

func isInCheckout(dir string) bool {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return true
	}
	for {
		if _, err := os.Lstat(filepath.Join(abs, ".git")); err == nil {
			return true
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return false
		}
		abs = parent
	}
}

func InstallPackage(dataDir, source string, build bool) (ProductManifest, []string, error) {
	manifest, err := ReadProductManifest(source)
	if err != nil {
		return manifest, nil, err
	}
	if !packageNameRE.MatchString(manifest.Name) {
		return manifest, nil, fmt.Errorf("invalid package name %q", manifest.Name)
	}
	tracked, err := trackedSkillDirs(source)
	if err != nil {
		return manifest, nil, err
	}
	sourceSkills, err := readSkills(source, tracked)
	if err != nil {
		return manifest, nil, err
	}
	dir := filepath.Join(dataDir, "packages", manifest.Name)
	if err := os.RemoveAll(dir); err != nil {
		return manifest, nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return manifest, nil, err
	}
	for _, name := range []string{ManifestFile, "CLAUDE.md", "hooks", "bin", ".claude", ".codex"} {
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
	for _, name := range sourceSkills {
		if err := os.CopyFS(filepath.Join(dir, "skills", name), os.DirFS(filepath.Join(source, "skills", name))); err != nil {
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
	if os.Geteuid() == 0 {
		if err := chownR(filepath.Dir(dir), containerUID, containerUID); err != nil {
			return manifest, nil, fmt.Errorf("hand packages to uid %d: %w", containerUID, err)
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

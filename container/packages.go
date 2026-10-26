package container

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/kronael/arizuko/groupfolder"
	"golang.org/x/sys/unix"
)

var packageNameRE = regexp.MustCompile(PackageNamePattern)

const packageLock = ".packages.lock"

// PackagePaths are shared writable paths below the instance data directory.
// The final path is a regular lock file whose inode must remain stable.
func PackagePaths() []string {
	return []string{"packages", "package-selections", "package-active", packageLock}
}

// PreparePackagePaths requires the package lock. It prepares mutable parents
// before compose binds them into daemons; immutable artifacts remain sealed.
func PreparePackagePaths(dataDir string) error {
	root, err := openPackageRoot(dataDir)
	if err != nil {
		return err
	}
	// Closing a directory handle has no pending writes.
	defer root.Close() //nolint:errcheck
	for _, dir := range PackagePaths() {
		if dir == packageLock {
			continue
		}
		if err := packageDirs(root, dir); err != nil {
			return err
		}
	}
	return nil
}

type PackageSnapshot struct {
	Name   string
	Digest string
}

// LockPackages covers preparation, selection and receipt reconciliation for all
// targets in one instance. Close releases the lock. Other package functions do
// not acquire it; callers also hold it while resolving system and group paths.
func LockPackages(dataDir string) (*os.File, error) {
	root, err := openPackageRoot(dataDir)
	if err != nil {
		return nil, err
	}
	// Closing a directory handle has no pending writes.
	defer root.Close() //nolint:errcheck
	file, err := root.OpenFile(packageLock,
		os.O_CREATE|os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0o644)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = fmt.Errorf("package lock is not a regular file")
	}
	if err == nil {
		err = unix.Flock(int(file.Fd()), unix.LOCK_EX)
	}
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return file, nil
}

// PreparePackage copies and seals source bytes before publishing their digest.
// Failed preparation never changes an active selection. The caller holds the
// instance package lock; sourceDir is a trusted, explicitly resolved source.
func PreparePackage(dataDir, sourceDir string) (PackageSnapshot, error) {
	return preparePackage(dataDir, sourceDir, nil)
}

func preparePackage(dataDir, sourceDir string, roots []string) (PackageSnapshot, error) {
	var snapshot PackageSnapshot
	if err := checkPackagePath(sourceDir); err != nil {
		return snapshot, err
	}
	name, err := inspectPackage(sourceDir, roots)
	if err != nil {
		return snapshot, err
	}
	source, err := os.OpenRoot(sourceDir)
	if err != nil {
		return snapshot, err
	}
	// Closing a directory handle has no pending writes.
	defer source.Close() //nolint:errcheck
	digest, err := digestPackage(source, ".", roots)
	if err != nil {
		return snapshot, err
	}
	snapshot = PackageSnapshot{Name: name, Digest: digest}
	root, err := openPackageRoot(dataDir)
	if err != nil {
		return snapshot, err
	}
	// Closing a directory handle has no pending writes.
	defer root.Close() //nolint:errcheck
	if _, err := root.Lstat(path.Join("packages", name, digest)); err == nil {
		_, err = resolvePackage(root, snapshot)
		return snapshot, err
	} else if !errors.Is(err, fs.ErrNotExist) {
		return snapshot, err
	}
	parent := path.Join("packages", name)
	if err := packageDirs(root, parent); err != nil {
		return snapshot, err
	}
	stage := path.Join(parent, ".prepare-"+rand.Text())
	if err := root.Mkdir(stage, 0o700); err != nil {
		return snapshot, err
	}
	err = fs.WalkDir(source.FS(), ".", func(rel string, entry fs.DirEntry, err error) error {
		if err != nil || rel == "." {
			return err
		}
		if isExcludedPackageEntry(rel, roots) {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if err := checkPackageEntry(rel, entry); err != nil {
			return err
		}
		target := path.Join(stage, rel)
		if entry.IsDir() {
			return root.Mkdir(target, 0o700)
		}
		input, err := source.OpenFile(rel, os.O_RDONLY|unix.O_NOFOLLOW, 0)
		if err != nil {
			return err
		}
		info, err := input.Stat()
		if err != nil || !info.Mode().IsRegular() {
			return errors.Join(fmt.Errorf("invalid source file %s", rel), err, input.Close())
		}
		output, err := root.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY,
			0o444|info.Mode().Perm()&0o111)
		if err != nil {
			return errors.Join(err, input.Close())
		}
		_, copyErr := io.Copy(output, input)
		modeErr := output.Chmod(0o444 | info.Mode().Perm()&0o111)
		return errors.Join(copyErr, modeErr, input.Close(), output.Close())
	})
	if err != nil {
		return snapshot, err
	}
	snapshot.Name, err = inspectPackage(filepath.Join(root.Name(), stage), roots)
	if err != nil {
		return snapshot, err
	}
	if snapshot.Name != name {
		return snapshot, fmt.Errorf("source package name changed during preparation")
	}
	snapshot.Digest, err = digestPackage(root, stage, nil)
	if err != nil {
		return snapshot, err
	}
	if err := sealPackage(root, stage); err != nil {
		return snapshot, err
	}
	target := path.Join("packages", snapshot.Name, snapshot.Digest)
	if _, err := root.Lstat(target); errors.Is(err, fs.ErrNotExist) {
		if err := root.Rename(stage, target); err != nil {
			return snapshot, err
		}
	} else if err != nil {
		return snapshot, err
	}
	_, err = resolvePackage(root, snapshot)
	return snapshot, err
}

// ResolvePackage returns a stable artifact path, never a source or active link.
// The caller holds the instance package lock until all paths are resolved.
func ResolvePackage(dataDir string, snapshot PackageSnapshot) (string, error) {
	root, err := openPackageRoot(dataDir)
	if err != nil {
		return "", err
	}
	// Closing a directory handle has no pending writes.
	defer root.Close() //nolint:errcheck
	return resolvePackage(root, snapshot)
}

// SelectPackages replaces one exact target's complete selection. Empty target
// means system-wide. Empty snapshots withdraw membership without deleting bytes.
// The caller holds the instance lock through selection and receipt repair.
func SelectPackages(dataDir, target string, snapshots []PackageSnapshot) error {
	key, err := packageTarget(target)
	if err != nil {
		return err
	}
	root, err := openPackageRoot(dataDir)
	if err != nil {
		return err
	}
	// Closing a directory handle has no pending writes.
	defer root.Close() //nolint:errcheck
	ordered := slices.Clone(snapshots)
	slices.SortFunc(ordered, func(a, b PackageSnapshot) int {
		return strings.Compare(a.Name, b.Name)
	})
	for i, snapshot := range ordered {
		if i > 0 && ordered[i-1].Name == snapshot.Name {
			return fmt.Errorf("duplicate selected package %q", snapshot.Name)
		}
		if _, err := resolvePackage(root, snapshot); err != nil {
			return err
		}
	}
	selection := path.Join("package-selections", key,
		packageSelectionDigest(ordered))
	if err := packageDirs(root, path.Dir(selection)); err != nil {
		return err
	}
	if _, err := root.Lstat(selection); errors.Is(err, fs.ErrNotExist) {
		stage := path.Join(path.Dir(selection), ".prepare-"+rand.Text())
		if err := root.Mkdir(stage, 0o700); err != nil {
			return err
		}
		for _, snapshot := range ordered {
			artifact := path.Join("packages", snapshot.Name, snapshot.Digest)
			reference, err := filepath.Rel(stage, artifact)
			if err != nil {
				return err
			}
			if err := root.Symlink(reference, path.Join(stage, snapshot.Name)); err != nil {
				return err
			}
		}
		if err := root.Chmod(stage, 0o555); err != nil {
			return err
		}
		if err := root.Rename(stage, selection); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if _, err := readPackageSelection(root, selection); err != nil {
		return err
	}
	if err := packageDirs(root, "package-active"); err != nil {
		return err
	}
	active := path.Join("package-active", key)
	if info, err := root.Lstat(active); err == nil {
		if info.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("active package selection is not a reference: %s", active)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	reference, err := filepath.Rel("package-active", selection)
	if err != nil {
		return err
	}
	if current, err := root.Readlink(active); err == nil && current == reference {
		return nil
	}
	pending := path.Join("package-active", ".select-"+rand.Text())
	if err := root.Symlink(reference, pending); err != nil {
		return err
	}
	return root.Rename(pending, active)
}

// ReadPackageSelection enumerates only the exact target, ordered by name.
// ResolvePackage turns each identity into a stable path under the same lock.
func ReadPackageSelection(dataDir, target string) ([]PackageSnapshot, error) {
	key, err := packageTarget(target)
	if err != nil {
		return nil, err
	}
	root, err := openPackageRoot(dataDir)
	if err != nil {
		return nil, err
	}
	// Closing a directory handle has no pending writes.
	defer root.Close() //nolint:errcheck
	if err := checkPackageDirs(root, "package-active"); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	reference, err := root.Readlink(path.Join("package-active", key))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	selection := path.Clean(path.Join("package-active", reference))
	if path.Dir(selection) != path.Join("package-selections", key) ||
		!isPackageDigest(path.Base(selection)) {
		return nil, fmt.Errorf("invalid package selection reference %q", reference)
	}
	expected, err := filepath.Rel("package-active", selection)
	if err != nil || reference != expected {
		return nil, fmt.Errorf("noncanonical package selection reference %q", reference)
	}
	return readPackageSelection(root, selection)
}

// WithdrawGroupPackages validates every exact configured target before any
// withdrawal. The caller holds the instance package lock through group purge.
// Immutable selections and artifacts remain available to already resolved turns.
func WithdrawGroupPackages(dataDir string, folders []string) error {
	selected := make([][]PackageSnapshot, len(folders))
	for i, folder := range folders {
		if folder == "" {
			return fmt.Errorf("group package withdrawal requires an exact folder")
		}
		var err error
		selected[i], err = ReadPackageSelection(dataDir, folder)
		if err != nil {
			return fmt.Errorf("preflight packages for %s: %w", folder, err)
		}
	}
	for i, folder := range folders {
		if len(selected[i]) == 0 {
			continue
		}
		if err := SelectPackages(dataDir, folder, nil); err != nil {
			return fmt.Errorf("withdraw packages for %s: %w; group purge did not start; retry deletion", folder, err)
		}
	}
	return nil
}

func readPackageSelection(root *os.Root, selection string) ([]PackageSnapshot, error) {
	if err := checkPackageDirs(root, selection); err != nil {
		return nil, err
	}
	dir, err := root.Open(selection)
	if err != nil {
		return nil, err
	}
	entries, err := dir.ReadDir(-1)
	if err := errors.Join(err, dir.Close()); err != nil {
		return nil, err
	}
	slices.SortFunc(entries, func(a, b fs.DirEntry) int {
		return strings.Compare(a.Name(), b.Name())
	})
	var snapshots []PackageSnapshot
	for _, entry := range entries {
		if !packageNameRE.MatchString(entry.Name()) || entry.Type()&os.ModeSymlink == 0 {
			return nil, fmt.Errorf("invalid package reference %q", entry.Name())
		}
		reference, err := root.Readlink(path.Join(selection, entry.Name()))
		if err != nil {
			return nil, err
		}
		artifact := path.Clean(path.Join(selection, reference))
		snapshot := PackageSnapshot{Name: entry.Name(), Digest: path.Base(artifact)}
		expected, err := filepath.Rel(selection,
			path.Join("packages", snapshot.Name, snapshot.Digest))
		if err != nil || reference != expected {
			return nil, fmt.Errorf("invalid artifact reference %q", reference)
		}
		if _, err := resolvePackage(root, snapshot); err != nil {
			return nil, err
		}
		snapshots = append(snapshots, snapshot)
	}
	if packageSelectionDigest(snapshots) != path.Base(selection) {
		return nil, fmt.Errorf("incomplete package selection %s", selection)
	}
	return snapshots, nil
}

func packageSelectionDigest(snapshots []PackageSnapshot) string {
	hash := sha256.New()
	for _, snapshot := range snapshots {
		// Hash writes cannot fail.
		_, _ = fmt.Fprintf(hash, "%s\x00%s\x00", snapshot.Name, snapshot.Digest)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func resolvePackage(root *os.Root, snapshot PackageSnapshot) (string, error) {
	if !packageNameRE.MatchString(snapshot.Name) || !isPackageDigest(snapshot.Digest) {
		return "", fmt.Errorf("invalid package identity %q/%q", snapshot.Name, snapshot.Digest)
	}
	rel := path.Join("packages", snapshot.Name, snapshot.Digest)
	if err := checkPackageDirs(root, rel); err != nil {
		return "", err
	}
	dir := filepath.Join(root.Name(), rel)
	name, err := inspectPackage(dir, nil)
	if err != nil {
		return "", err
	}
	if name != snapshot.Name {
		return "", fmt.Errorf("artifact name %q does not match %q", name, snapshot.Name)
	}
	digest, err := digestPackage(root, rel, nil)
	if err != nil {
		return "", err
	}
	if digest != snapshot.Digest {
		return "", fmt.Errorf("package artifact content does not match %s", rel)
	}
	return dir, nil
}

func inspectPackage(dir string, roots []string) (string, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", err
	}
	// Closing a directory handle has no pending writes.
	defer root.Close() //nolint:errcheck
	for _, name := range roots {
		info, err := root.Lstat(name)
		if err != nil {
			return "", fmt.Errorf("required core entry %s: %w", name, err)
		}
		if name == "skills" && !info.IsDir() {
			return "", fmt.Errorf("required core entry %s is not a directory", name)
		}
		if (name == ManifestFile || name == "CLAUDE.md") && !info.Mode().IsRegular() {
			return "", fmt.Errorf("required core entry %s is not a regular file", name)
		}
	}
	err = fs.WalkDir(root.FS(), ".", func(rel string, entry fs.DirEntry, err error) error {
		if err != nil || rel == "." {
			return err
		}
		if isExcludedPackageEntry(rel, roots) {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if err := checkPackageEntry(rel, entry); err != nil {
			return err
		}
		if rel == "skills" && !entry.IsDir() {
			return fmt.Errorf("package skills is not a directory")
		}
		if path.Dir(rel) != "skills" {
			return nil
		}
		if !entry.IsDir() || !skillNameRe.MatchString(entry.Name()) {
			return fmt.Errorf("invalid package skill %q", rel)
		}
		info, err := root.Lstat(path.Join(rel, "SKILL.md"))
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("skill %s requires a regular SKILL.md", rel)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	manifest, err := ReadProductManifest(dir)
	if err != nil {
		return "", err
	}
	if !packageNameRE.MatchString(manifest.Name) {
		return "", fmt.Errorf("invalid package name %q in %s", manifest.Name, ManifestFile)
	}
	if roots != nil && manifest.Name != CorePackageName {
		return "", fmt.Errorf("required core source declares %q, expected %q", manifest.Name, CorePackageName)
	}
	return manifest.Name, nil
}

func isExcludedPackageEntry(rel string, roots []string) bool {
	if path.Base(rel) == ".git" {
		return true
	}
	first, _, _ := strings.Cut(rel, "/")
	return rel != "." && roots != nil && !slices.Contains(roots, first)
}

func checkPackageEntry(rel string, entry fs.DirEntry) error {
	if !fs.ValidPath(rel) || strings.ContainsAny(rel, "\\\x00\r\n") {
		return fmt.Errorf("invalid package path %q", rel)
	}
	if !entry.IsDir() && !entry.Type().IsRegular() {
		return fmt.Errorf("package path is not a regular file or directory: %s", rel)
	}
	return nil
}

func digestPackage(root *os.Root, rel string, roots []string) (string, error) {
	dir, err := root.OpenRoot(rel)
	if err != nil {
		return "", err
	}
	// Closing a directory handle has no pending writes.
	defer dir.Close() //nolint:errcheck
	hash := sha256.New()
	err = fs.WalkDir(dir.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if isExcludedPackageEntry(name, roots) {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if err := checkPackageEntry(name, entry); err != nil {
			return err
		}
		if entry.IsDir() {
			// Hash writes cannot fail.
			_, _ = fmt.Fprintf(hash, "d\x00%s\x00", name)
			return nil
		}
		// Hash writes cannot fail.
		_, _ = fmt.Fprintf(hash, "f\x00%s\x00%d\x00%d\x00", name,
			info.Mode().Perm()&0o111, info.Size())
		file, err := dir.OpenFile(name, os.O_RDONLY|unix.O_NOFOLLOW, 0)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(hash, file)
		return errors.Join(copyErr, file.Close())
	})
	return hex.EncodeToString(hash.Sum(nil)), err
}

func sealPackage(root *os.Root, rel string) error {
	dir, err := root.OpenRoot(rel)
	if err != nil {
		return err
	}
	// Closing a directory handle has no pending writes.
	defer dir.Close() //nolint:errcheck
	var dirs []string
	err = fs.WalkDir(dir.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			dirs = append(dirs, name)
		}
		return nil
	})
	if err != nil {
		return err
	}
	slices.Reverse(dirs)
	for _, name := range dirs {
		if err := dir.Chmod(name, 0o555); err != nil {
			return err
		}
	}
	return nil
}

func packageTarget(target string) (string, error) {
	if target == "" {
		return "system", nil
	}
	if target == "." || !groupfolder.IsValidFolder(target) || !fs.ValidPath(target) ||
		strings.ContainsAny(target, "\r\n") {
		return "", fmt.Errorf("invalid package target %q", target)
	}
	digest := sha256.Sum256([]byte(target))
	return "group-" + hex.EncodeToString(digest[:]), nil
}

func isPackageDigest(digest string) bool {
	if len(digest) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(digest)
	return err == nil && hex.EncodeToString(decoded) == digest
}

func checkPackagePath(dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	for current := abs; current != filepath.Dir(current); current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("package directory is not a real directory: %s", current)
		}
	}
	return nil
}

func openPackageRoot(dataDir string) (*os.Root, error) {
	if dataDir == "" {
		return nil, fmt.Errorf("package data directory is not configured")
	}
	if err := checkPackagePath(dataDir); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, err
	}
	return os.OpenRoot(abs)
}

func packageDirs(root *os.Root, rel string) error {
	err := checkPackageDirs(root, rel)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err != nil {
		if err := root.MkdirAll(rel, 0o755); err != nil {
			return err
		}
		if err := checkPackageDirs(root, rel); err != nil {
			return err
		}
	}
	if os.Geteuid() == 0 {
		for current := rel; current != "."; current = path.Dir(current) {
			if err := root.Chown(current, containerUID, containerUID); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkPackageDirs(root *os.Root, rel string) error {
	for current := ""; rel != ""; {
		part, rest, _ := strings.Cut(rel, "/")
		rel = rest
		current = path.Join(current, part)
		info, err := root.Lstat(current)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("package directory is not a real directory: %s", current)
		}
	}
	return nil
}

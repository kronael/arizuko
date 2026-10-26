package container

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func snapshotSource(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	writeSnapshotFile(t, dir, ManifestFile, "name = \""+name+"\"\n")
	writeSnapshotFile(t, dir, "skills/triage/SKILL.md", body)
	return dir
}

func writeSnapshotFile(t *testing.T, dir, name, body string) {
	t.Helper()
	target := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func lockSnapshotPackages(t *testing.T, dataDir string) *os.File {
	t.Helper()
	lock, err := LockPackages(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := lock.Close(); err != nil && !errors.Is(err, fs.ErrClosed) {
			t.Error(err)
		}
	})
	t.Cleanup(func() {
		err := filepath.WalkDir(dataDir, func(name string, entry fs.DirEntry, err error) error {
			if err != nil || !entry.IsDir() {
				return err
			}
			return os.Chmod(name, 0o755)
		})
		if err != nil {
			t.Error(err)
		}
	})
	return lock
}

func prepareSnapshot(t *testing.T, dataDir, source string) PackageSnapshot {
	t.Helper()
	snapshot, err := PreparePackage(dataDir, source)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func selectedSnapshots(t *testing.T, dataDir, target string) []PackageSnapshot {
	t.Helper()
	snapshots, err := ReadPackageSelection(dataDir, target)
	if err != nil {
		t.Fatal(err)
	}
	return snapshots
}

// A malformed subtree refuses before withdrawal; an IO failure leaves visible,
// retryable progress while immutable snapshots remain available.
func TestPackageGroupWithdrawal(t *testing.T) {
	for _, failure := range []string{"preflight", "partial"} {
		t.Run(failure, func(t *testing.T) {
			root := t.TempDir()
			lockSnapshotPackages(t, root)
			snapshot := prepareSnapshot(t, root, snapshotSource(t, "probe", "retained"))
			for _, folder := range []string{"team", "team/child", "keeper", ""} {
				if err := SelectPackages(root, folder, []PackageSnapshot{snapshot}); err != nil {
					t.Fatal(err)
				}
			}
			key, err := packageTarget("team/child")
			if err != nil {
				t.Fatal(err)
			}
			active := filepath.Join(root, "package-active", key)
			reference, err := os.Readlink(active)
			if err != nil {
				t.Fatal(err)
			}
			obstruction := filepath.Join(root, "package-selections", key, packageSelectionDigest(nil))
			if failure == "preflight" {
				if err := os.Remove(active); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("../invalid", active); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(obstruction, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := WithdrawGroupPackages(root, []string{"team", "team/child"}); err == nil {
				t.Fatal("failure was hidden")
			}
			want := 0
			if failure == "preflight" {
				want = 1
				if err := os.Remove(active); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(reference, active); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Remove(obstruction); err != nil {
				t.Fatal(err)
			}
			if got := selectedSnapshots(t, root, "team"); len(got) != want {
				t.Fatalf("unexpected partial state: %v", got)
			}
			if err := WithdrawGroupPackages(root, []string{"team", "team/child"}); err != nil {
				t.Fatal(err)
			}
			for _, folder := range []string{"team", "team/child"} {
				if got := selectedSnapshots(t, root, folder); len(got) != 0 {
					t.Fatalf("orphan selection %s: %v", folder, got)
				}
			}
			for _, folder := range []string{"keeper", ""} {
				if got := selectedSnapshots(t, root, folder); !slices.Equal(got, []PackageSnapshot{snapshot}) {
					t.Fatalf("unrelated selection %s: %v", folder, got)
				}
			}
		})
	}
}

func snapshotBytes(t *testing.T, dir, want string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "skills/triage/SKILL.md"))
	if err != nil || string(data) != want {
		t.Fatalf("selected skill = %q, %v; want %q", data, err, want)
	}
}

// A mutable source changes before and after selection; prepared bytes stay fixed.
func TestPackageSnapshotSourceIsolation(t *testing.T) {
	dataDir := t.TempDir()
	lockSnapshotPackages(t, dataDir)
	source := snapshotSource(t, "declared-name", "first source bytes")
	writeSnapshotFile(t, source, ".git/config", "repository secret")
	writeSnapshotFile(t, source, "assets/arizuko.toml", "ordinary asset")
	snapshot := prepareSnapshot(t, dataDir, source)
	artifact, err := ResolvePackage(dataDir, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if artifact != filepath.Join(dataDir, "packages", "declared-name", snapshot.Digest) {
		t.Fatalf("artifact is not under configured package storage: %s", artifact)
	}
	if snapshot.Name != "declared-name" {
		t.Fatalf("name = %q", snapshot.Name)
	}
	writeSnapshotFile(t, source, "skills/triage/SKILL.md", "edited before activation")
	if err := SelectPackages(dataDir, "corp/eng", []PackageSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	writeSnapshotFile(t, source, "skills/triage/SKILL.md", "edited after activation")
	selected := selectedSnapshots(t, dataDir, "corp/eng")
	if !reflect.DeepEqual(selected, []PackageSnapshot{snapshot}) {
		t.Fatalf("selection = %+v", selected)
	}
	snapshotBytes(t, artifact, "first source bytes")
	if _, err := os.Lstat(filepath.Join(artifact, ".git")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("repository metadata entered artifact: %v", err)
	}
	for name, want := range map[string]string{
		ManifestFile:          "name = \"declared-name\"\n",
		"assets/arizuko.toml": "ordinary asset",
	} {
		data, err := os.ReadFile(filepath.Join(artifact, name))
		if err != nil || string(data) != want {
			t.Fatalf("artifact %s = %q, %v", name, data, err)
		}
	}
	err = filepath.WalkDir(artifact, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().Perm()&0o222 != 0 {
			t.Errorf("artifact path is writable: %s", name)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Repeating preparation and selection keeps the artifact, reference and directory set.
func TestPackageSnapshotIdempotent(t *testing.T) {
	dataDir := t.TempDir()
	lockSnapshotPackages(t, dataDir)
	source := snapshotSource(t, "stable", "same bytes")
	first := prepareSnapshot(t, dataDir, source)
	if err := SelectPackages(dataDir, "", []PackageSnapshot{first}); err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, dataDir)
	writeSnapshotFile(t, source, ".git/config", "ignored change")
	second := prepareSnapshot(t, dataDir, source)
	if first != second {
		t.Fatalf("same source changed identity: %+v != %+v", first, second)
	}
	if err := SelectPackages(dataDir, "", []PackageSnapshot{second}); err != nil {
		t.Fatal(err)
	}
	if after := snapshotTree(t, dataDir); !reflect.DeepEqual(before, after) {
		t.Fatalf("repeat changed filesystem:\nbefore %v\nafter %v", before, after)
	}
}

func snapshotTree(t *testing.T, dir string) []string {
	t.Helper()
	var tree []string
	err := filepath.WalkDir(dir, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, name)
		if err != nil {
			return err
		}
		tree = append(tree, rel)
		if entry.Type()&os.ModeSymlink != 0 {
			reference, err := os.Readlink(name)
			if err != nil {
				return err
			}
			tree = append(tree, "->"+reference)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

// Replacement and removal keep old resolved paths; exact targets do not inherit.
func TestPackageSelectionRetainsResolvedPaths(t *testing.T) {
	dataDir := t.TempDir()
	lockSnapshotPackages(t, dataDir)
	source := snapshotSource(t, "updates", "old bytes")
	old := prepareSnapshot(t, dataDir, source)
	other := prepareSnapshot(t, dataDir, snapshotSource(t, "alpha", "other bytes"))
	if err := SelectPackages(dataDir, "", []PackageSnapshot{other}); err != nil {
		t.Fatal(err)
	}
	if err := SelectPackages(dataDir, "corp/eng", []PackageSnapshot{old, other}); err != nil {
		t.Fatal(err)
	}
	if got := selectedSnapshots(t, dataDir, "corp/eng"); !reflect.DeepEqual(got, []PackageSnapshot{other, old}) {
		t.Fatalf("selection order = %+v", got)
	}
	resolved, err := ResolvePackage(dataDir, old)
	if err != nil {
		t.Fatal(err)
	}
	writeSnapshotFile(t, source, "skills/triage/SKILL.md", "new bytes")
	current := prepareSnapshot(t, dataDir, source)
	if current.Digest == old.Digest {
		t.Fatal("changed source kept old identity")
	}
	if err := SelectPackages(dataDir, "corp/eng", []PackageSnapshot{current}); err != nil {
		t.Fatal(err)
	}
	snapshotBytes(t, resolved, "old bytes")
	currentPath, err := ResolvePackage(dataDir, current)
	if err != nil {
		t.Fatal(err)
	}
	snapshotBytes(t, currentPath, "new bytes")
	if err := SelectPackages(dataDir, "corp/eng", nil); err != nil {
		t.Fatal(err)
	}
	if got := selectedSnapshots(t, dataDir, "corp/eng"); len(got) != 0 {
		t.Fatalf("removal left selection: %+v", got)
	}
	snapshotBytes(t, resolved, "old bytes")
	snapshotBytes(t, currentPath, "new bytes")
	for _, target := range []string{"corp", "corp/eng/sub", "unregistered"} {
		if got := selectedSnapshots(t, dataDir, target); len(got) != 0 {
			t.Fatalf("target %q inherited %+v", target, got)
		}
	}
	if got := selectedSnapshots(t, dataDir, ""); !reflect.DeepEqual(got, []PackageSnapshot{other}) {
		t.Fatalf("group update changed system target: %+v", got)
	}
}

// Invalid source input refuses before writes and keeps the selected reference.
func TestPackageSnapshotSourceRefusal(t *testing.T) {
	cases := []struct {
		name string
		edit func(*testing.T, string)
	}{
		{"missing manifest", func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, ManifestFile)); err != nil {
				t.Fatal(err)
			}
		}},
		{"malformed manifest", func(t *testing.T, dir string) {
			writeSnapshotFile(t, dir, ManifestFile, "name = [")
		}},
		{"nameless manifest", func(t *testing.T, dir string) {
			writeSnapshotFile(t, dir, ManifestFile, "brand = 'no name'\n")
		}},
		{"nested selection", func(t *testing.T, dir string) {
			writeSnapshotFile(t, dir, ManifestFile, "name='bad'\n[[product]]\nsource='/elsewhere'\n")
		}},
		{"unsafe name", func(t *testing.T, dir string) {
			writeSnapshotFile(t, dir, ManifestFile, "name='../escape'\n")
		}},
		{"unsafe filename", func(t *testing.T, dir string) {
			writeSnapshotFile(t, dir, "assets/back\\slash", "invalid")
		}},
		{"file symlink", func(t *testing.T, dir string) {
			if err := os.Symlink("SKILL.md", filepath.Join(dir, "skills/triage/link")); err != nil {
				t.Fatal(err)
			}
		}},
		{"directory symlink", func(t *testing.T, dir string) {
			if err := os.Symlink(t.TempDir(), filepath.Join(dir, "assets")); err != nil {
				t.Fatal(err)
			}
		}},
		{"manifest symlink", func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, ManifestFile)); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("skills/triage/SKILL.md", filepath.Join(dir, ManifestFile)); err != nil {
				t.Fatal(err)
			}
		}},
		{"unsafe skill name", func(t *testing.T, dir string) {
			writeSnapshotFile(t, dir, "skills/UPPER/SKILL.md", "invalid skill")
		}},
		{"missing skill entry", func(t *testing.T, dir string) {
			writeSnapshotFile(t, dir, "skills/no-entry/helper.md", "no SKILL.md")
		}},
		{"special file", func(t *testing.T, dir string) {
			if err := unix.Mkfifo(filepath.Join(dir, "pipe"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			dataDir := t.TempDir()
			lockSnapshotPackages(t, dataDir)
			good := prepareSnapshot(t, dataDir, snapshotSource(t, "good", "selected bytes"))
			if err := SelectPackages(dataDir, "corp/eng", []PackageSnapshot{good}); err != nil {
				t.Fatal(err)
			}
			before := snapshotTree(t, dataDir)
			source := snapshotSource(t, "bad", "refuse these bytes")
			test.edit(t, source)
			if _, err := PreparePackage(dataDir, source); err == nil {
				t.Fatal("unsafe source accepted")
			}
			if after := snapshotTree(t, dataDir); !reflect.DeepEqual(before, after) {
				t.Fatalf("source validation changed storage: %v != %v", before, after)
			}
			if got := selectedSnapshots(t, dataDir, "corp/eng"); !reflect.DeepEqual(got, []PackageSnapshot{good}) {
				t.Fatalf("source failure changed selection: %+v", got)
			}
		})
	}
}

// Traversing identities, unsafe targets and missing artifacts cannot switch selection.
func TestPackageSelectionRefusal(t *testing.T) {
	dataDir := t.TempDir()
	lockSnapshotPackages(t, dataDir)
	good := prepareSnapshot(t, dataDir, snapshotSource(t, "good", "selected"))
	if err := SelectPackages(dataDir, "group", []PackageSnapshot{good}); err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, dataDir)
	for _, snapshot := range []PackageSnapshot{
		{Name: "../escape", Digest: good.Digest},
		{Name: "good", Digest: "../../outside"},
		{Name: "good", Digest: strings.Repeat("a", 64)},
		{Name: "different-name", Digest: good.Digest},
		{Name: "good", Digest: strings.ToUpper(good.Digest)},
	} {
		if err := SelectPackages(dataDir, "group", []PackageSnapshot{snapshot}); err == nil {
			t.Fatalf("accepted %+v", snapshot)
		}
		if _, err := ResolvePackage(dataDir, snapshot); err == nil {
			t.Fatalf("resolved %+v", snapshot)
		}
	}
	if err := SelectPackages(dataDir, "group", []PackageSnapshot{good, good}); err == nil {
		t.Fatal("duplicate accepted")
	}
	for _, target := range []string{"../escape", "/absolute", "group/../other", "group//child", ".", "group\\child", "group\nchild"} {
		if err := SelectPackages(dataDir, target, []PackageSnapshot{good}); err == nil {
			t.Fatalf("accepted target %q", target)
		}
		if _, err := ReadPackageSelection(dataDir, target); err == nil {
			t.Fatalf("read target %q", target)
		}
	}
	if after := snapshotTree(t, dataDir); !reflect.DeepEqual(before, after) {
		t.Fatalf("invalid selection changed storage: %v != %v", before, after)
	}
}

// Symlinked storage and source roots refuse instead of following host paths.
func TestPackageSnapshotRejectsSymlinkRoots(t *testing.T) {
	source := snapshotSource(t, "safe", "bytes")
	for _, part := range []string{"source", "data", "packages", ".packages.lock"} {
		t.Run(part, func(t *testing.T) {
			dataDir := t.TempDir()
			outside := t.TempDir()
			sourceDir := source
			switch part {
			case "source":
				sourceDir = filepath.Join(outside, "link")
				if err := os.Symlink(source, sourceDir); err != nil {
					t.Fatal(err)
				}
			case "data":
				dataDir = filepath.Join(dataDir, "link")
				if err := os.Symlink(outside, dataDir); err != nil {
					t.Fatal(err)
				}
			default:
				if err := os.Symlink(outside, filepath.Join(dataDir, part)); err != nil {
					t.Fatal(err)
				}
			}
			before := snapshotTree(t, outside)
			if part == ".packages.lock" || part == "data" {
				lock, err := LockPackages(dataDir)
				if err == nil {
					lock.Close()
					t.Fatal("unsafe lock accepted")
				}
			} else if _, err := PreparePackage(dataDir, sourceDir); err == nil {
				t.Fatal("unsafe storage accepted")
			}
			if after := snapshotTree(t, outside); !reflect.DeepEqual(before, after) {
				t.Fatal("wrote outside configured storage")
			}
		})
	}
}

// The writer keeps one lock from preflight through selection and receipt repair.
// A remove waits for the install receipt, then selects an empty completed set.
func TestPackageMutationLockSerializes(t *testing.T) {
	dataDir := t.TempDir()
	lock := lockSnapshotPackages(t, dataDir)
	snapshot := prepareSnapshot(t, dataDir, snapshotSource(t, "serial", "installed bytes"))
	probePackageLock(t, dataDir, true)
	if err := SelectPackages(dataDir, "group", []PackageSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	probePackageLock(t, dataDir, true)
	started := make(chan struct{})
	removed := make(chan error, 1)
	go func() {
		close(started)
		removeLock, err := LockPackages(dataDir)
		if err != nil {
			removed <- err
			return
		}
		defer removeLock.Close()
		if _, err := os.Stat(filepath.Join(dataDir, "receipt-repaired")); err != nil {
			removed <- err
			return
		}
		removed <- SelectPackages(dataDir, "group", nil)
	}()
	<-started
	select {
	case err := <-removed:
		t.Fatalf("remove entered before receipt repair: %v", err)
	default:
	}
	writeSnapshotFile(t, dataDir, "receipt-repaired", "install completed")
	probePackageLock(t, dataDir, true)
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-removed; err != nil {
		t.Fatal(err)
	}
	probePackageLock(t, dataDir, false)
	lockSnapshotPackages(t, dataDir)
	if got := selectedSnapshots(t, dataDir, "group"); len(got) != 0 {
		t.Fatalf("remove lost serialization: %+v", got)
	}
}

// Status can resolve root-owned-style read-only files under the same lock.
func TestPackageMutationLockReadOnly(t *testing.T) {
	dataDir := t.TempDir()
	lock := lockSnapshotPackages(t, dataDir)
	snapshot := prepareSnapshot(t, dataDir, snapshotSource(t, "readable", "status bytes"))
	if err := SelectPackages(dataDir, "", []PackageSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dataDir, ".packages.lock"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dataDir, 0o555); err != nil {
		t.Fatal(err)
	}
	reader := lockSnapshotPackages(t, dataDir)
	if got := selectedSnapshots(t, dataDir, ""); !reflect.DeepEqual(got, []PackageSnapshot{snapshot}) {
		t.Fatalf("read-only status selection = %+v", got)
	}
	artifact, err := ResolvePackage(dataDir, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	snapshotBytes(t, artifact, "status bytes")
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
}

// Malformed references and changed artifact bytes refuse rather than confer membership.
func TestPackageSelectionRejectsCorruptGraph(t *testing.T) {
	for _, corruption := range []string{"absolute active", "foreign target", "selection symlink", "incomplete selection", "changed artifact", "artifact symlink"} {
		t.Run(corruption, func(t *testing.T) {
			dataDir := t.TempDir()
			lockSnapshotPackages(t, dataDir)
			source := snapshotSource(t, "selected", "original bytes")
			snapshot := prepareSnapshot(t, dataDir, source)
			if err := SelectPackages(dataDir, "group", []PackageSnapshot{snapshot}); err != nil {
				t.Fatal(err)
			}
			artifact, err := ResolvePackage(dataDir, snapshot)
			if err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(filepath.Join(dataDir, "package-active"))
			if err != nil || len(entries) != 1 {
				t.Fatalf("active references: %v, %v", entries, err)
			}
			active := filepath.Join(dataDir, "package-active", entries[0].Name())
			selection, err := filepath.EvalSymlinks(active)
			if err != nil {
				t.Fatal(err)
			}
			switch corruption {
			case "absolute active":
				if err := os.Remove(active); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(selection, active); err != nil {
					t.Fatal(err)
				}
			case "foreign target":
				if err := SelectPackages(dataDir, "other", []PackageSnapshot{snapshot}); err != nil {
					t.Fatal(err)
				}
				otherKey, err := packageTarget("other")
				if err != nil {
					t.Fatal(err)
				}
				reference, err := os.Readlink(filepath.Join(dataDir, "package-active", otherKey))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(active); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(reference, active); err != nil {
					t.Fatal(err)
				}
			case "selection symlink":
				if err := os.Rename(selection, selection+"-saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(selection+"-saved", selection); err != nil {
					t.Fatal(err)
				}
			case "incomplete selection":
				if err := os.Chmod(selection, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(filepath.Join(selection, "selected")); err != nil {
					t.Fatal(err)
				}
			case "changed artifact":
				file := filepath.Join(artifact, "skills/triage/SKILL.md")
				if err := os.Chmod(file, 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, []byte("unselected bytes"), 0o644); err != nil {
					t.Fatal(err)
				}
			case "artifact symlink":
				if err := os.Rename(artifact, artifact+"-saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(source, artifact); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := ReadPackageSelection(dataDir, "group"); err == nil {
				t.Fatal("corrupt active graph accepted")
			}
		})
	}
}

func probePackageLock(t *testing.T, dataDir string, held bool) {
	t.Helper()
	probe, err := os.Open(filepath.Join(dataDir, ".packages.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	err = unix.Flock(int(probe.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if held {
		if !errors.Is(err, unix.EWOULDBLOCK) {
			t.Fatalf("writer released package lock: %v", err)
		}
	} else if err != nil {
		t.Fatalf("writer retained package lock: %v", err)
	}
}

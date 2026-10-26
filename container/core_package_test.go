package container

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

func corePackageFixture(t *testing.T) *core.Config {
	t.Helper()
	dataDir := t.TempDir()
	lockSnapshotPackages(t, dataDir)
	cfg := &core.Config{ProjectRoot: dataDir,
		GroupsDir: filepath.Join(dataDir, "groups")}
	seedCoreRelease(t, cfg)
	return cfg
}

func seedCoreRelease(t *testing.T, cfg *core.Config) {
	t.Helper()
	if cfg.ProjectRoot == "" {
		cfg.ProjectRoot = filepath.Dir(cfg.GroupsDir)
	}
	release := t.TempDir()
	for name, body := range map[string]string{
		ManifestFile:             "name = \"core\"\n",
		"CLAUDE.md":              "release manual",
		"skills/base/SKILL.md":   "release skill",
		"output-styles/brief.md": "release style",
	} {
		writeSnapshotFile(t, release, filepath.Join("ant", name), body)
	}
	cfg.AppSrcDir = release
	t.Cleanup(func() {
		for _, name := range []string{"packages", "package-selections"} {
			root := filepath.Join(cfg.ProjectRoot, name)
			_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
				if err != nil || !entry.IsDir() {
					return err
				}
				return os.Chmod(path, 0o755)
			})
		}
	})
}

func corePackageDatabase(t *testing.T) *store.Store {
	t.Helper()
	database, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func TestPackageInventoryCanonicalSelections(t *testing.T) {
	for _, conflict := range []string{"package", "skill", "group-core", "missing-core", "missing-root", "dangling-group", "corrupt-artifact"} {
		t.Run(conflict, func(t *testing.T) {
			cfg := corePackageFixture(t)
			if err := EnsureCorePackage(cfg, nil); err != nil {
				t.Fatal(err)
			}
			system, err := ReadPackageSelection(cfg.ProjectRoot, "")
			if err != nil {
				t.Fatal(err)
			}
			switch conflict {
			case "package":
				snapshot := prepareSnapshot(t, cfg.ProjectRoot, snapshotSource(t, "extra", "skill"))
				if err := SelectPackages(cfg.ProjectRoot, "", append(system, snapshot)); err != nil {
					t.Fatal(err)
				}
				if err := SelectPackages(cfg.ProjectRoot, "g", []PackageSnapshot{snapshot}); err != nil {
					t.Fatal(err)
				}
			case "skill":
				writeSnapshotFile(t, cfg.GroupsDir, "g/.claude/skills/base/.disabled", "disabled")
				source := snapshotSource(t, "extra", "skill")
				writeSnapshotFile(t, source, "skills/base/SKILL.md", "conflicting")
				if err := SelectPackages(cfg.ProjectRoot, "g", []PackageSnapshot{prepareSnapshot(t, cfg.ProjectRoot, source)}); err != nil {
					t.Fatal(err)
				}
			case "group-core":
				if err := SelectPackages(cfg.ProjectRoot, "g", system); err != nil {
					t.Fatal(err)
				}
			case "missing-core":
				if err := SelectPackages(cfg.ProjectRoot, "", nil); err != nil {
					t.Fatal(err)
				}
			case "missing-root":
				snapshot := prepareSnapshot(t, cfg.ProjectRoot, snapshotSource(t, "core", "skill"))
				if err := SelectPackages(cfg.ProjectRoot, "", []PackageSnapshot{snapshot}); err != nil {
					t.Fatal(err)
				}
			case "dangling-group":
				if err := SelectPackages(cfg.ProjectRoot, "g", nil); err != nil {
					t.Fatal(err)
				}
				entries, err := os.ReadDir(filepath.Join(cfg.ProjectRoot, "package-active"))
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					if entry.Name() == "system" {
						continue
					}
					active := filepath.Join(cfg.ProjectRoot, "package-active", entry.Name())
					if err := os.Remove(active); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink("../package-selections/"+entry.Name()+"/"+strings.Repeat("0", 64), active); err != nil {
						t.Fatal(err)
					}
				}
			case "corrupt-artifact":
				dir, err := ResolvePackage(cfg.ProjectRoot, system[0])
				if err != nil {
					t.Fatal(err)
				}
				file := filepath.Join(dir, "CLAUDE.md")
				if err := os.Chmod(file, 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, []byte("corrupt"), 0o444); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := ReadPackageInventory(cfg.ProjectRoot, "g"); err == nil {
				t.Fatalf("%s accepted", conflict)
			}
		})
	}
}

func TestPackageMountTargetsRefuseWritableState(t *testing.T) {
	for _, target := range []string{
		"skill", "disabled-payload", "top-level", "instance", "manual", "style",
		"skill-file", "manual-dir", "home-link", "ancestor-link", "target-link", "marker-link",
		"mask-link", "mask-writable", "mask-nonempty", "legacy-empty", "manual-link", "style-link", "marker-dir",
	} {
		t.Run(target, func(t *testing.T) {
			cfg := corePackageFixture(t)
			if err := EnsureCorePackage(cfg, nil); err != nil {
				t.Fatal(err)
			}
			inventory, err := ReadPackageInventory(cfg.ProjectRoot, "g")
			if err != nil {
				t.Fatal(err)
			}
			home := filepath.Join(cfg.GroupsDir, "g")
			var file string
			switch target {
			case "skill", "disabled-payload":
				file = filepath.Join(home, ".claude/skills/base/SKILL.md")
			case "top-level":
				file = filepath.Join(home, "skills/base/SKILL.md")
			case "instance":
				file = filepath.Join(cfg.ProjectRoot, "skills/base/SKILL.md")
			case "manual":
				file = filepath.Join(home, ".claude/CLAUDE.md")
			case "style":
				file = filepath.Join(home, ".claude/output-styles/brief.md")
			case "skill-file":
				file = filepath.Join(home, ".claude/skills/base")
			}
			if file != "" {
				writeSnapshotFile(t, filepath.Dir(file), filepath.Base(file), "preserve user bytes")
			}
			if target == "disabled-payload" {
				writeSnapshotFile(t, home, ".claude/skills/base/.disabled", "choice")
			}
			if target == "manual-dir" {
				if err := os.MkdirAll(filepath.Join(home, ".claude/CLAUDE.md"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if target == "legacy-empty" || target == "marker-dir" {
				dir := filepath.Join(home, "skills/base")
				if target == "marker-dir" {
					dir = filepath.Join(home, ".claude/skills/base/.disabled")
				}
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			links := map[string]string{
				"home-link": home, "ancestor-link": filepath.Join(home, ".claude"),
				"target-link": filepath.Join(home, ".claude/skills/base"),
				"marker-link": filepath.Join(home, ".claude/skills/base/.disabled"),
				"mask-link":   filepath.Join(cfg.ProjectRoot, "packages/.mount-empty"),
				"manual-link": filepath.Join(home, ".claude/CLAUDE.md"),
				"style-link":  filepath.Join(home, ".claude/output-styles/brief.md"),
			}
			if link := links[target]; link != "" {
				if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), link); err != nil {
					t.Fatal(err)
				}
			}
			mask := filepath.Join(cfg.ProjectRoot, "packages/.mount-empty")
			if target == "mask-writable" || target == "mask-nonempty" {
				if err := os.Mkdir(mask, 0o755); err != nil {
					t.Fatal(err)
				}
				if target == "mask-nonempty" {
					writeSnapshotFile(t, mask, "SKILL.md", "not empty")
					if err := os.Chmod(mask, 0o555); err != nil {
						t.Fatal(err)
					}
				}
			}
			_, _, refusal := PreparePackageMountTargets(cfg, "g", inventory)
			if refusal == nil {
				t.Fatalf("%s accepted", target)
			}
			if file != "" {
				exact := file
				if target == "top-level" || target == "instance" {
					exact = filepath.Dir(file)
				}
				if !strings.Contains(refusal.Error(), exact) || !strings.Contains(refusal.Error(), "packages install/upgrade") {
					t.Fatalf("refusal lacks exact path/action: %v", refusal)
				}
				body, err := os.ReadFile(file)
				if err != nil || string(body) != "preserve user bytes" {
					t.Fatalf("user bytes changed at %s: %q, %v", file, body, err)
				}
			}
			if target == "manual" || target == "style" || strings.HasPrefix(target, "mask-") {
				if _, err := os.Lstat(filepath.Join(home, ".claude/skills/base")); !os.IsNotExist(err) {
					t.Fatalf("refusal created target before complete preflight: %v", err)
				}
			}
		})
	}
}

func TestPackageMountTargetsCopiedSetupRefuses(t *testing.T) {
	cfg := &core.Config{ProjectRoot: t.TempDir()}
	cfg.GroupsDir = filepath.Join(cfg.ProjectRoot, "groups")
	seedCoreRelease(t, cfg)
	if err := SetupGroup(cfg, nil, "g", ""); err != nil {
		t.Fatal(err)
	}
	lockSnapshotPackages(t, cfg.ProjectRoot)
	inventory, err := ReadPackageInventory(cfg.ProjectRoot, "g")
	if err != nil {
		t.Fatal(err)
	}
	writeSnapshotFile(t, cfg.GroupsDir, "g/.claude/skills/base/.disabled", "disabled copy")
	if _, _, err := PreparePackageMountTargets(cfg, "g", inventory); err == nil ||
		!strings.Contains(err.Error(), "packages install/upgrade") {
		t.Fatalf("copied SetupGroup home accepted: %v", err)
	}
	file := filepath.Join(cfg.GroupsDir, "g/.claude/skills/base/SKILL.md")
	if body, err := os.ReadFile(file); err != nil || string(body) != "release skill" {
		t.Fatalf("setup copy changed: %q, %v", body, err)
	}
}

func TestPackageMountTargetsAnchorExactHome(t *testing.T) {
	cfg := corePackageFixture(t)
	home := filepath.Join(cfg.GroupsDir, "g")
	other := filepath.Join(cfg.GroupsDir, "other")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSnapshotFile(t, other, "secret", "other group")
	root, rel, err := openPackageHome(cfg.GroupsDir, "g")
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if rel != "." {
		t.Fatalf("home is not anchored: %q", rel)
	}
	if err := os.Rename(home, home+"-moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, home); err != nil {
		t.Fatal(err)
	}
	if err := root.WriteFile("owned", []byte("original home"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(other, "owned")); !os.IsNotExist(err) {
		t.Fatalf("anchored write reached other group: %v", err)
	}
	if err := root.Symlink("../other", "escape"); err != nil {
		t.Fatal(err)
	}
	if _, err := root.ReadFile("escape/secret"); err == nil {
		t.Fatal("anchored home read another group's file")
	}
	if _, _, err := openPackageHome(cfg.GroupsDir, "g"); err == nil {
		t.Fatal("replacement home symlink accepted")
	}
}

func TestSetupGroupCoreSelectionConflict(t *testing.T) {
	cfg := &core.Config{ProjectRoot: t.TempDir()}
	cfg.GroupsDir = filepath.Join(cfg.ProjectRoot, "groups")
	seedCoreRelease(t, cfg)
	database := corePackageDatabase(t)
	lock, err := LockPackages(cfg.ProjectRoot)
	if err != nil {
		t.Fatal(err)
	}
	source := snapshotSource(t, "optional", "optional skill")
	writeSnapshotFile(t, source, "skills/base/SKILL.md", "conflicting skill")
	snapshot := prepareSnapshot(t, cfg.ProjectRoot, source)
	if err := SelectPackages(cfg.ProjectRoot, "new", []PackageSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := SetupGroup(cfg, database, "new", ""); err == nil {
		t.Fatal("unregistered group's core skill collision was accepted")
	}
	if _, err := os.Stat(filepath.Join(cfg.GroupsDir, "new")); !os.IsNotExist(err) {
		t.Fatalf("conflicting group got a skeleton: %v", err)
	}
}

// Excluded developer files produce the baked payload.
// Included changes create new immutable paths.
func TestCorePackageProjection(t *testing.T) {
	cfg := corePackageFixture(t)
	first, err := prepareCorePackage(cfg)
	if err != nil {
		t.Fatal(err)
	}
	old, err := ResolvePackage(cfg.ProjectRoot, first)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(cfg.AppSrcDir, "ant")
	writeSnapshotFile(t, source, "src/index.ts", "developer runner")
	writeSnapshotFile(t, source, "package.json", "dependencies")
	if err := os.Symlink("/missing-dependency-tree", filepath.Join(source, "node_modules")); err != nil {
		t.Fatal(err)
	}
	developer, err := prepareCorePackage(cfg)
	if err != nil || developer != first {
		t.Fatalf("developer projection differs: %+v != %+v, %v", developer, first, err)
	}
	writeSnapshotFile(t, source, "src/index.ts", "changed runner")
	writeSnapshotFile(t, source, "package.json", "changed dependencies")
	unchanged, err := prepareCorePackage(cfg)
	if err != nil || unchanged != first {
		t.Fatalf("excluded edit changed digest: %+v, %v", unchanged, err)
	}
	entries, err := os.ReadDir(old)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if !reflect.DeepEqual(names, []string{"CLAUDE.md", "arizuko.toml", "output-styles", "skills"}) {
		t.Fatalf("extra artifact roots: %v", names)
	}
	previous := first
	for _, name := range []string{"CLAUDE.md", "skills/base/SKILL.md", "output-styles/brief.md"} {
		writeSnapshotFile(t, source, name, "included edit")
		changed, err := prepareCorePackage(cfg)
		if err != nil || changed.Digest == previous.Digest {
			t.Fatalf("included edit %s kept digest: %+v, %v", name, changed, err)
		}
		previous = changed
	}
	if err := EnsureCorePackage(cfg, nil); err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolvePackage(cfg.ProjectRoot, first)
	if err != nil || resolved != old {
		t.Fatalf("old immutable path lost: %s, %v", resolved, err)
	}
	data, err := os.ReadFile(filepath.Join(old, "skills/base/SKILL.md"))
	if err != nil || string(data) != "release skill" {
		t.Fatalf("old bytes changed: %q, %v", data, err)
	}
	ordinarySource := snapshotSource(t, "ordinary", "ordinary skill")
	writeSnapshotFile(t, ordinarySource, "runner/index.ts", "ordinary payload")
	ordinary := prepareSnapshot(t, cfg.ProjectRoot, ordinarySource)
	dir, err := ResolvePackage(cfg.ProjectRoot, ordinary)
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "runner/index.ts")); err != nil || string(data) != "ordinary payload" {
		t.Fatalf("ordinary source was projected: %q, %v", data, err)
	}
	if err := os.Chmod(old, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSnapshotFile(t, old, "runner.ts", "unexpected artifact byte")
	if _, err := ResolvePackage(cfg.ProjectRoot, first); err == nil {
		t.Fatal("artifact validation ignored an extra root")
	}
	if err := os.Remove(filepath.Join(old, "runner.ts")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(old, 0o555); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing", filepath.Join(ordinarySource, "node_modules")); err != nil {
		t.Fatal(err)
	}
	if _, err := PreparePackage(cfg.ProjectRoot, ordinarySource); err == nil {
		t.Fatal("ordinary package ignored a symlink")
	}
}

// Invalid required roots, identity and included entries leave the prior active reference intact.
func TestCorePackageInvalidSource(t *testing.T) {
	for _, invalid := range []string{
		ManifestFile, "CLAUDE.md", "skills", "output-styles", "wrong-name",
		"malformed", "included-symlink", "root-symlink", "invalid-skill", "wrong-type",
	} {
		t.Run(invalid, func(t *testing.T) {
			cfg := corePackageFixture(t)
			if err := EnsureCorePackage(cfg, nil); err != nil {
				t.Fatal(err)
			}
			active := filepath.Join(cfg.ProjectRoot, "package-active/system")
			before, err := os.Lstat(active)
			if err != nil {
				t.Fatal(err)
			}
			selected := selectedSnapshots(t, cfg.ProjectRoot, "")
			source := filepath.Join(cfg.AppSrcDir, "ant")
			switch invalid {
			case "wrong-name":
				writeSnapshotFile(t, source, ManifestFile, "name = \"foreign\"\n")
			case "malformed":
				writeSnapshotFile(t, source, ManifestFile, "name = [\n")
			case "included-symlink":
				if err := os.Symlink("missing", filepath.Join(source, "skills/base/extra")); err != nil {
					t.Fatal(err)
				}
			case "invalid-skill":
				writeSnapshotFile(t, source, "skills/Bad Name/SKILL.md", "invalid")
			case "root-symlink", "wrong-type":
				if err := os.Rename(filepath.Join(source, "skills"), filepath.Join(source, "original-skills")); err != nil {
					t.Fatal(err)
				}
				if invalid == "root-symlink" {
					if err := os.Symlink("original-skills", filepath.Join(source, "skills")); err != nil {
						t.Fatal(err)
					}
				} else {
					writeSnapshotFile(t, source, "skills", "not a directory")
				}
			default:
				if err := os.Rename(filepath.Join(source, invalid), filepath.Join(source, invalid+"-absent")); err != nil {
					t.Fatal(err)
				}
			}
			if err := EnsureCorePackage(cfg, nil); err == nil || !strings.Contains(err.Error(), "required core source") {
				t.Fatalf("invalid source hidden: %v", err)
			}
			after, err := os.Lstat(active)
			unchanged := reflect.DeepEqual(selectedSnapshots(t, cfg.ProjectRoot, ""), selected)
			if err != nil || !os.SameFile(before, after) || !unchanged {
				t.Fatalf("invalid source changed active selection: %v", err)
			}
		})
	}
}

// Pre-store selection, failed completion and retry preserve optional receipts and every registered group's bytes.
func TestCorePackageCompletionAndPreservation(t *testing.T) {
	cfg := corePackageFixture(t)
	database := corePackageDatabase(t)
	optionalSource := snapshotSource(t, "optional", "optional skill")
	optional := prepareSnapshot(t, cfg.ProjectRoot, optionalSource)
	if err := SelectPackages(cfg.ProjectRoot, "", []PackageSnapshot{optional}); err != nil {
		t.Fatal(err)
	}
	receipt := store.InstalledPackage{
		Name: "optional", Source: optionalSource, Revision: "optional-revision",
		InstalledAt: "2020-01-01T00:00:00Z",
		Manifest: map[string][]string{
			"skill": {"triage"}, "package_source": {optional.Digest},
		},
		AssetHashes: map[string]string{"package_source": optional.Digest},
	}
	if err := database.PutInstalledPackage(receipt); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"copied/.claude/skills/base/SKILL.md":   "copied stock",
		"copied/.claude/skills/base/.disabled":  "disabled",
		"copied/CLAUDE.md":                      "edited manual",
		"copied/.claude/skills/custom/SKILL.md": "custom skill",
		"declared/arizuko.toml":                 "# mine\nname = \"mine\"\n",
	}
	for name, body := range files {
		writeSnapshotFile(t, cfg.GroupsDir, name, body)
	}
	for _, folder := range []string{"copied", "declared", "missing"} {
		if err := database.PutGroup(core.Group{Folder: folder}); err != nil {
			t.Fatal(err)
		}
	}
	if err := EnsureCorePackage(cfg, nil); err != nil {
		t.Fatal(err)
	}
	if _, found, err := database.InstalledPackage("", "core"); err != nil || found {
		t.Fatalf("pre-store stage invented receipt: %v, %v", found, err)
	}
	active := filepath.Join(cfg.ProjectRoot, "package-active/system")
	before, err := os.Lstat(active)
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("core receipt failure")
	failure := &packageReceiptFailure{Store: database.Obj(), failure: injected}
	err = EnsureCorePackage(cfg, store.NewOver(failure))
	if !errors.Is(err, injected) || !strings.Contains(err.Error(), "incomplete package operation") {
		t.Fatalf("receipt failure hidden: %v", err)
	}
	failure.failure = nil
	if err := EnsureCorePackage(cfg, store.NewOver(failure)); err != nil {
		t.Fatal(err)
	}
	coreReceipt, found, err := database.InstalledPackage("", "core")
	if err != nil || !found || coreReceipt.Source != "ant" ||
		coreReceipt.Revision != coreReceipt.AssetHashes["package_source"] ||
		!reflect.DeepEqual(coreReceipt.Manifest["skill"], []string{"base"}) {
		t.Fatalf("completion lost core provenance: %+v, %v, %v", coreReceipt, found, err)
	}
	if err := EnsureCorePackage(cfg, database); err != nil {
		t.Fatal(err)
	}
	after, err := os.Lstat(active)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("retry replaced active reference: %v", err)
	}
	writeSnapshotFile(t, cfg.AppSrcDir, "ant/skills/base/SKILL.md", "next release")
	if err := EnsureCorePackage(cfg, database); err != nil {
		t.Fatal(err)
	}
	got, _, err := database.InstalledPackage("", "optional")
	if err != nil || !reflect.DeepEqual(got, receipt) {
		t.Fatalf("core update changed optional receipt: %+v, %v", got, err)
	}
	if selected := selectedSnapshots(t, cfg.ProjectRoot, ""); len(selected) != 2 || selected[1] != optional {
		t.Fatalf("core update lost optional snapshot: %+v", selected)
	}
	for name, body := range files {
		data, err := os.ReadFile(filepath.Join(cfg.GroupsDir, name))
		if err != nil || string(data) != body {
			t.Fatalf("core changed group file %s: %q, %v", name, data, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(cfg.GroupsDir, "copied", ManifestFile)); !os.IsNotExist(err) {
		t.Fatalf("core invented group declaration: %v", err)
	}
}

// Missing optional provenance and foreign core receipts refuse before the existing selection changes.
func TestCorePackageReceiptRefusal(t *testing.T) {
	for _, invalid := range []string{
		"missing", "digest", "source", "revision", "inventory", "timestamp", "foreign-core",
	} {
		t.Run(invalid, func(t *testing.T) {
			cfg := corePackageFixture(t)
			database := corePackageDatabase(t)
			optional := prepareSnapshot(t, cfg.ProjectRoot, snapshotSource(t, "optional", "optional"))
			if err := ActivatePackages(cfg.ProjectRoot, "", database, []PackageSnapshot{optional},
				[]store.InstalledPackage{{Source: "optional-source", Revision: "optional-revision"}}); err != nil {
				t.Fatal(err)
			}
			receipt, _, err := database.InstalledPackage("", "optional")
			if err != nil {
				t.Fatal(err)
			}
			switch invalid {
			case "missing":
				if _, err := database.DeleteInstalledPackage("", "optional"); err != nil {
					t.Fatal(err)
				}
			case "digest":
				receipt.Manifest["package_source"] = []string{"wrong"}
			case "source":
				receipt.Source = ""
			case "revision":
				receipt.Revision = ""
			case "inventory":
				receipt.Manifest["skill"] = []string{"invented"}
			case "timestamp":
				receipt.InstalledAt = ""
			case "foreign-core":
				receipt = store.InstalledPackage{Name: "core", Source: "foreign-source"}
			}
			if invalid != "missing" {
				if err := database.PutInstalledPackage(receipt); err != nil {
					t.Fatal(err)
				}
			}
			before := selectedSnapshots(t, cfg.ProjectRoot, "")
			if err := EnsureCorePackage(cfg, database); err == nil {
				t.Fatal("invalid receipt accepted")
			}
			if after := selectedSnapshots(t, cfg.ProjectRoot, ""); !reflect.DeepEqual(before, after) {
				t.Fatalf("receipt refusal changed selection: %+v", after)
			}
		})
	}
}

// Canonical group names and skills conflict with required core even when a group has no manifest.
func TestCorePackageCanonicalCollisions(t *testing.T) {
	for _, conflict := range []string{"group-skill", "group-core", "system-skill", "package-name"} {
		t.Run(conflict, func(t *testing.T) {
			cfg := corePackageFixture(t)
			database := corePackageDatabase(t)
			if err := database.PutGroup(core.Group{Folder: "team"}); err != nil {
				t.Fatal(err)
			}
			name, skill := "tools", "base"
			if conflict == "group-core" {
				name, skill = "core", "other"
			}
			if conflict == "package-name" {
				skill = "group-only"
				optional := prepareSnapshot(t, cfg.ProjectRoot, snapshotSource(t, "tools", "system tool"))
				if err := ActivatePackages(cfg.ProjectRoot, "", database, []PackageSnapshot{optional},
					[]store.InstalledPackage{{Source: "system-source", Revision: "system-revision"}}); err != nil {
					t.Fatal(err)
				}
			}
			source := t.TempDir()
			writeSnapshotFile(t, source, ManifestFile, "name = \""+name+"\"\n")
			writeSnapshotFile(t, source, "skills/"+skill+"/SKILL.md", "conflicting skill")
			snapshot := prepareSnapshot(t, cfg.ProjectRoot, source)
			target := "team"
			if conflict == "system-skill" {
				target = ""
			}
			if err := ActivatePackages(cfg.ProjectRoot, target, database, []PackageSnapshot{snapshot},
				[]store.InstalledPackage{{Source: source, Revision: snapshot.Digest}}); err != nil {
				t.Fatal(err)
			}
			before := selectedSnapshots(t, cfg.ProjectRoot, "")
			if err := EnsureCorePackage(cfg, database); err == nil {
				t.Fatal("canonical collision accepted")
			}
			if after := selectedSnapshots(t, cfg.ProjectRoot, ""); !reflect.DeepEqual(before, after) {
				t.Fatalf("collision changed system selection: %+v", after)
			}
		})
	}
}

package main

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

func adoptionGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func adoptionHistoryFixture(t *testing.T) (*core.Config,
	container.PackageAdoptionSelection, string) {
	t.Helper()
	repo := t.TempDir()
	seedCoreSource(t, repo)
	mustWrite(t, filepath.Join(repo, "ant/skills/retired/SKILL.md"), "old skill")
	mustWrite(t, filepath.Join(repo, "ant/skills/retired/bin/run"), "old script")
	if err := os.Chmod(filepath.Join(repo, "ant/skills/retired/bin/run"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitInit(t, repo)
	old := adoptionGit(t, repo, "rev-parse", "HEAD")
	runLifecycleGit(t, repo, "tag", "v1.2.3")
	runLifecycleGit(t, repo, "-c", "user.name=t", "-c", "user.email=t@t",
		"tag", "-a", "v1.2.4", "-m", "release")
	runLifecycleGit(t, repo, "rm", "-r", "ant/skills/retired")
	mustWrite(t, filepath.Join(repo, "ant/CLAUDE.md"), "new manual")
	runLifecycleGit(t, repo, "add", ".")
	runLifecycleGit(t, repo, "-c", "user.name=t", "-c", "user.email=t@t",
		"commit", "-qm", "current")
	dataDir := t.TempDir()
	unsealPackageFixtures(t, dataDir)
	snapshot, err := container.PreparePackage(dataDir, filepath.Join(repo, "ant"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := &core.Config{ProjectRoot: dataDir, GroupsDir: filepath.Join(dataDir, "groups"),
		HostAppDir: repo, AppSrcDir: repo}
	return cfg, container.PackageAdoptionSelection{
		System: []container.PackageSnapshot{snapshot},
	}, old
}

// Reachable release trees include retired units and ignore HEAD, unrelated tags and working files.
func TestCorePackageAdoptionHistoryReleaseTrees(t *testing.T) {
	cfg, _, old := adoptionHistoryFixture(t)
	repo := cfg.HostAppDir
	mustWrite(t, filepath.Join(repo, "ant/skills/unrelated/SKILL.md"), "unrelated")
	runLifecycleGit(t, repo, "add", ".")
	tree := adoptionGit(t, repo, "write-tree")
	unrelated := adoptionGit(t, repo, "-c", "user.name=t", "-c", "user.email=t@t",
		"commit-tree", tree, "-m", "unrelated")
	runLifecycleGit(t, repo, "tag", "v9.9.9", unrelated)
	runLifecycleGit(t, repo, "tag", "debug")
	before := adoptionGit(t, repo, "status", "--porcelain=v1")
	head := adoptionGit(t, repo, "rev-parse", "HEAD")
	history, err := readCorePackageAdoptionHistory(cfg)
	if err != nil || len(history) != 1 {
		t.Fatalf("release history: %+v, %v", history, err)
	}
	want := container.PackageAdoptionHistory{Package: "core", Source: "ant", Commit: old,
		Units: []container.PackageAdoptionUnit{
			{Path: "CLAUDE.md", Entries: []container.PackageAdoptionEntry{
				{Path: ".", Mode: 0o644, Data: []byte("release manual")},
			}},
			{Path: "output-styles/brief.md", Entries: []container.PackageAdoptionEntry{
				{Path: ".", Mode: 0o644, Data: []byte("release style")},
			}},
			{Path: "skills/base", Entries: []container.PackageAdoptionEntry{
				{Path: ".", Mode: fs.ModeDir | 0o755},
				{Path: "SKILL.md", Mode: 0o644, Data: []byte("release skill")},
			}},
			{Path: "skills/retired", Entries: []container.PackageAdoptionEntry{
				{Path: ".", Mode: fs.ModeDir | 0o755},
				{Path: "SKILL.md", Mode: 0o644, Data: []byte("old skill")},
				{Path: "bin", Mode: fs.ModeDir | 0o755},
				{Path: "bin/run", Mode: 0o755, Data: []byte("old script")},
			}},
		}}
	if !reflect.DeepEqual(history[0], want) {
		t.Fatalf("whole release units = %#v, want %#v", history[0], want)
	}
	if before != adoptionGit(t, repo, "status", "--porcelain=v1") ||
		head != adoptionGit(t, repo, "rev-parse", "HEAD") {
		t.Fatal("history read changed checkout or index")
	}
}

// Whole retired units authorize matching copies; extra entries and mixed releases refuse.
func TestCorePackageAdoptionHistoryComparator(t *testing.T) {
	cfg, selected, _ := adoptionHistoryFixture(t)
	history, err := readCorePackageAdoptionHistory(cfg)
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(cfg.GroupsDir, "team", ".claude")
	mustWrite(t, filepath.Join(home, "skills/retired/SKILL.md"), "old skill")
	mustWrite(t, filepath.Join(home, "skills/retired/bin/run"), "old script")
	mustWrite(t, filepath.Join(home, "CLAUDE.md"), "release manual")
	plan, err := container.PlanPackageAdoption(cfg, "team", selected, nil, history)
	if err != nil || len(plan.Copies) != 2 || len(plan.Conflicts) != 0 {
		t.Fatalf("retired whole unit: %+v, %v", plan, err)
	}
	mustWrite(t, filepath.Join(home, "skills/retired/extra"), "operator")
	plan, err = container.PlanPackageAdoption(cfg, "team", selected, nil, history)
	if err != nil || len(plan.Conflicts) != 1 {
		t.Fatalf("extra entry accepted: %+v, %v", plan, err)
	}
	if err := os.Remove(filepath.Join(home, "skills/retired/extra")); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(cfg.HostAppDir, "ant/skills/retired/SKILL.md"), "new skill")
	mustWrite(t, filepath.Join(cfg.HostAppDir, "ant/skills/retired/bin/run"), "new script")
	runLifecycleGit(t, cfg.HostAppDir, "add", ".")
	runLifecycleGit(t, cfg.HostAppDir, "-c", "user.name=t", "-c", "user.email=t@t",
		"commit", "-qm", "next release")
	runLifecycleGit(t, cfg.HostAppDir, "tag", "v2.0.0")
	history, err = readCorePackageAdoptionHistory(cfg)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(home, "skills/retired/bin/run"), "new script")
	plan, err = container.PlanPackageAdoption(cfg, "team", selected, nil, history)
	if err != nil || len(plan.Conflicts) != 1 {
		t.Fatalf("mixed release accepted: %+v, %v", plan, err)
	}
}

// Current sealed bytes and complete receipts need no Git history.
func TestPackageAdoptionHistoryUnnecessary(t *testing.T) {
	for _, proof := range []string{"current", "receipt"} {
		t.Run(proof, func(t *testing.T) {
			cfg, selected, _ := adoptionHistoryFixture(t)
			file := filepath.Join(cfg.GroupsDir, "team/.claude/skills/base/SKILL.md")
			body := "release skill"
			var receipts []store.InstalledPackage
			if proof == "receipt" {
				body = "receipt bytes"
				receipts = []store.InstalledPackage{{Name: "core", Source: "ant", Revision: "local",
					Manifest: map[string][]string{"skill": {"base"}},
					AssetHashes: map[string]string{
						"file:.claude/skills/base/SKILL.md": sha256hex([]byte(body)),
					}}}
			}
			mustWrite(t, file, body)
			t.Setenv("PATH", t.TempDir())
			cfg.HostAppDir = ""
			plan, err := container.PlanPackageAdoption(cfg, "team", selected, receipts, nil)
			if err != nil || len(plan.Copies) != 1 || len(plan.Conflicts) != 0 {
				t.Fatalf("history unnecessary: %+v, %v", plan, err)
			}
		})
	}
}

// Optional evidence uses the receipt commit and owned names, independent of checkout HEAD and basename.
func TestOptionalPackageAdoptionHistoryExactReceipt(t *testing.T) {
	repo := t.TempDir()
	mustWrite(t, filepath.Join(repo, "arizuko.toml"), "name = \"optional\"\n")
	mustWrite(t, filepath.Join(repo, "skills/tool/SKILL.md"), "old")
	mustWrite(t, filepath.Join(repo, "skills/tool/data/a b"), "old data")
	mustWrite(t, filepath.Join(repo, "skills/other/SKILL.md"), "other")
	gitInit(t, repo)
	old := adoptionGit(t, repo, "rev-parse", "HEAD")
	mustWrite(t, filepath.Join(repo, "skills/tool/SKILL.md"), "new")
	runLifecycleGit(t, repo, "add", ".")
	runLifecycleGit(t, repo, "-c", "user.name=t", "-c", "user.email=t@t",
		"commit", "-qm", "new")
	dataDir := t.TempDir()
	unsealPackageFixtures(t, dataDir)
	source, err := preparePackageCandidate(dataDir, repo, "git+file://"+repo,
		adoptionGit(t, repo, "rev-parse", "HEAD"))
	if err != nil {
		t.Fatal(err)
	}
	receipt := store.InstalledPackage{Folder: "team", Name: "optional", Source: source.source,
		Revision: old, Manifest: map[string][]string{"skill": {"tool"}}}
	history, err := readOptionalPackageAdoptionHistory("team", receipt, source, repo)
	if err != nil || history.Commit != old || history.Folder != "team" ||
		history.Package != "optional" || history.Source != source.source || len(history.Units) != 1 {
		t.Fatalf("receipt binding: %+v, %v", history, err)
	}
	want := []container.PackageAdoptionEntry{
		{Path: ".", Mode: fs.ModeDir | 0o755},
		{Path: "SKILL.md", Mode: 0o644, Data: []byte("old")},
		{Path: "data", Mode: fs.ModeDir | 0o755},
		{Path: "data/a b", Mode: 0o644, Data: []byte("old data")},
	}
	if history.Units[0].Path != "skills/tool" || !reflect.DeepEqual(history.Units[0].Entries, want) {
		t.Fatalf("exact complete unit: %+v", history.Units)
	}
	for _, scenario := range []string{"folder", "name", "source", "local", "short",
		"missing-commit", "missing-skill", "duplicate-skill", "traversal", "tag-object"} {
		t.Run(scenario, func(t *testing.T) {
			rejected := receipt
			switch scenario {
			case "folder":
				rejected.Folder = "elsewhere"
			case "name":
				rejected.Name = "other"
			case "source":
				rejected.Source += "-other"
			case "local":
				rejected.Revision = "local"
			case "short":
				rejected.Revision = old[:12]
			case "missing-commit":
				rejected.Revision = strings.Repeat("f", 40)
			case "missing-skill":
				rejected.Manifest = map[string][]string{"skill": {"absent"}}
			case "duplicate-skill":
				rejected.Manifest = map[string][]string{"skill": {"tool", "tool"}}
			case "traversal":
				rejected.Manifest = map[string][]string{"skill": {"../tool"}}
			case "tag-object":
				runLifecycleGit(t, repo, "-c", "user.name=t", "-c", "user.email=t@t",
					"tag", "-a", "receipt", "-m", "tag", old)
				rejected.Revision = adoptionGit(t, repo, "rev-parse", "refs/tags/receipt")
			}
			if _, err := readOptionalPackageAdoptionHistory("team", rejected, source, repo); err == nil {
				t.Fatal("unbound or incomplete receipt accepted")
			}
		})
	}
	receipt.Folder = ""
	if history, err := readOptionalPackageAdoptionHistory("team", receipt, source, repo); err != nil || history.Folder != "" {
		t.Fatalf("system receipt: %+v, %v", history, err)
	}
	if _, err := readOptionalPackageAdoptionHistory("team", receipt, source, source.dir); err == nil {
		t.Fatal("sealed artifact accepted as ordinary Git source")
	}
	for _, objectPath := range []string{"skills/tool", "skills/tool/data/a b"} {
		t.Run("missing-object/"+objectPath, func(t *testing.T) {
			object := adoptionGit(t, repo, "rev-parse", old+":"+objectPath)
			file := filepath.Join(repo, ".git/objects", object[:2], object[2:])
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := os.WriteFile(file, data, 0o444); err != nil {
					t.Error(err)
				}
			})
			if err := os.Remove(file); err != nil {
				t.Fatal(err)
			}
			if _, err := readOptionalPackageAdoptionHistory("team", receipt, source, repo); err == nil {
				t.Fatal("missing receipt object accepted")
			}
		})
	}
}

// Missing Git, identity, commits, trees and blobs return errors without selecting another release.
func TestCorePackageAdoptionHistoryMissingInputs(t *testing.T) {
	for _, scenario := range []string{"identity", "git", "commit", "tree", "blob", "malformed-tree", "subdirectory"} {
		t.Run(scenario, func(t *testing.T) {
			cfg, _, old := adoptionHistoryFixture(t)
			switch scenario {
			case "identity":
				cfg.HostAppDir = ""
			case "git":
				t.Setenv("PATH", t.TempDir())
			case "subdirectory":
				cfg.HostAppDir = filepath.Join(cfg.HostAppDir, "ant")
			default:
				object := old
				switch scenario {
				case "tree", "malformed-tree":
					object = adoptionGit(t, cfg.HostAppDir, "rev-parse", old+":ant/skills/retired")
				case "blob":
					object = adoptionGit(t, cfg.HostAppDir, "rev-parse", old+":ant/skills/retired/bin/run")
				}
				file := filepath.Join(cfg.HostAppDir, ".git/objects", object[:2], object[2:])
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
				if scenario == "malformed-tree" {
					if err := os.WriteFile(file, []byte("invalid Git object"), 0o444); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := readCorePackageAdoptionHistory(cfg); err == nil {
				t.Fatal("missing history input accepted")
			}
		})
	}
}

// Git symlinks, submodules and invalid unit paths never become regular proof entries.
func TestCorePackageAdoptionHistoryUnsafeEntries(t *testing.T) {
	for _, scenario := range []string{"symlink", "submodule", "backslash", "disabled", "missing-skill"} {
		t.Run(scenario, func(t *testing.T) {
			cfg, _, _ := adoptionHistoryFixture(t)
			repo := cfg.HostAppDir
			switch scenario {
			case "symlink":
				if err := os.Symlink("SKILL.md", filepath.Join(repo, "ant/skills/base/link")); err != nil {
					t.Fatal(err)
				}
			case "submodule":
				commit := adoptionGit(t, repo, "rev-parse", "HEAD")
				runLifecycleGit(t, repo, "update-index", "--add", "--cacheinfo",
					"160000,"+commit+",ant/skills/base/submodule")
			case "backslash":
				mustWrite(t, filepath.Join(repo, "ant/skills/base/bad\\name"), "bad")
			case "disabled":
				mustWrite(t, filepath.Join(repo, "ant/skills/base/.disabled"), "")
			case "missing-skill":
				mustWrite(t, filepath.Join(repo, "ant/skills/incomplete/data"), "data")
			}
			if scenario != "submodule" {
				runLifecycleGit(t, repo, "add", ".")
			}
			runLifecycleGit(t, repo, "-c", "user.name=t", "-c", "user.email=t@t",
				"commit", "-qm", "unsafe")
			runLifecycleGit(t, repo, "tag", "v2.0.0")
			if _, err := readCorePackageAdoptionHistory(cfg); err == nil {
				t.Fatal("unsafe history entry accepted")
			}
		})
	}
}

// Ambient Git state and replace refs cannot substitute another tree for the configured source.
func TestCorePackageAdoptionHistoryConfiguredCheckout(t *testing.T) {
	cfg, _, old := adoptionHistoryFixture(t)
	other, _, _ := adoptionHistoryFixture(t)
	current := adoptionGit(t, cfg.HostAppDir, "rev-parse", "HEAD")
	runLifecycleGit(t, cfg.HostAppDir, "replace", old, current)
	t.Setenv("GIT_DIR", filepath.Join(other.HostAppDir, ".git"))
	t.Setenv("GIT_WORK_TREE", other.HostAppDir)
	history, err := readCorePackageAdoptionHistory(cfg)
	if err != nil || len(history) != 1 || history[0].Commit != old ||
		!slices.ContainsFunc(history[0].Units, func(unit container.PackageAdoptionUnit) bool {
			return unit.Path == "skills/retired"
		}) {
		t.Fatalf("source substitution: %+v, %v", history, err)
	}
}

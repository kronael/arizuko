package container

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
	"golang.org/x/sys/unix"
)

func adoptionDisk(t *testing.T, root string) map[string]string {
	t.Helper()
	state := map[string]string{}
	err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		var data []byte
		if info.Mode().IsRegular() {
			data, err = os.ReadFile(name)
		} else if info.Mode()&fs.ModeSymlink != 0 {
			var target string
			target, err = os.Readlink(name)
			data = []byte(target)
		}
		state[name] = fmt.Sprintf("%v:%s", info.Mode(), data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func checkedAdoption(t *testing.T, cfg *core.Config, selected PackageAdoptionSelection,
	receipts []store.InstalledPackage, history []PackageAdoptionHistory) (PackageAdoptionPlan, error) {
	t.Helper()
	before := adoptionDisk(t, cfg.ProjectRoot)
	inputs := fmt.Sprintf("%#v %#v %#v", selected, receipts, history)
	plan, err := PlanPackageAdoption(cfg, "team/exact", selected, receipts, history)
	if !reflect.DeepEqual(before, adoptionDisk(t, cfg.ProjectRoot)) ||
		inputs != fmt.Sprintf("%#v %#v %#v", selected, receipts, history) {
		t.Fatal("planning changed filesystem, active selection or proof inputs")
	}
	return plan, err
}

func TestPackageAdoptionCurrentCopy(t *testing.T) {
	cfg := corePackageFixture(t)
	if err := EnsureCorePackage(cfg, nil); err != nil {
		t.Fatal(err)
	}
	selected, err := ReadPackageSelection(cfg.ProjectRoot, "")
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(cfg.GroupsDir, "team/exact")
	file := filepath.Join(home, ".claude/skills/base/SKILL.md")
	writeSnapshotFile(t, home, ".claude/skills/base/SKILL.md", "release skill")
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := checkedAdoption(t, cfg,
		PackageAdoptionSelection{System: selected}, nil, nil)
	if err != nil || len(plan.Conflicts) != 0 || len(plan.Copies) != 1 {
		t.Fatalf("exact current copy: %+v, %v", plan, err)
	}
	copy := plan.Copies[0]
	if copy.Path != filepath.Dir(file) || len(copy.Entries) != 2 ||
		copy.Entries[1].Path != "SKILL.md" || string(copy.Entries[1].Data) != "release skill" {
		t.Fatalf("missing exact preimage: %+v", copy)
	}
	after, err := os.ReadFile(file)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("planning changed copy: %q, %v", after, err)
	}
}

func TestPackageAdoptionCoreFiles(t *testing.T) {
	for _, scenario := range []string{"current", "historical", "edited"} {
		t.Run(scenario, func(t *testing.T) {
			cfg := corePackageFixture(t)
			if err := EnsureCorePackage(cfg, nil); err != nil {
				t.Fatal(err)
			}
			selected := PackageAdoptionSelection{System: selectedSnapshots(t, cfg.ProjectRoot, "")}
			manual, style, styleName := "release manual", "release style", "brief.md"
			var history []PackageAdoptionHistory
			switch scenario {
			case "historical":
				manual, style, styleName = "old manual", "old style", "retired.md"
				history = []PackageAdoptionHistory{{Package: "core", Source: "ant", Commit: strings.Repeat("e", 40),
					Units: []PackageAdoptionUnit{
						{Path: "CLAUDE.md", Entries: []PackageAdoptionEntry{{Path: ".", Mode: 0o644, Data: []byte(manual)}}},
						{Path: "output-styles/" + styleName,
							Entries: []PackageAdoptionEntry{{Path: ".", Mode: 0o644, Data: []byte(style)}}},
					}}}
			case "edited":
				manual, style = "private manual edit", "private style edit"
			}
			home := filepath.Join(cfg.GroupsDir, "team/exact")
			writeSnapshotFile(t, home, ".claude/CLAUDE.md", manual)
			writeSnapshotFile(t, home, ".claude/output-styles/"+styleName, style)
			plan, err := checkedAdoption(t, cfg, selected, nil, history)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "edited" {
				if len(plan.Copies) != 0 || len(plan.Conflicts) != 2 ||
					plan.Conflicts[0].Path != filepath.Join(home, ".claude/CLAUDE.md") ||
					plan.Conflicts[1].Path != filepath.Join(home, ".claude/output-styles/brief.md") {
					t.Fatalf("unsorted or incomplete conflicts: %+v", plan)
				}
			} else if len(plan.Copies) != 2 || len(plan.Conflicts) != 0 {
				t.Fatalf("core file proof refused: %+v", plan)
			}
		})
	}
}

func adoptionOldUnit(name, body, guide string) PackageAdoptionUnit {
	return PackageAdoptionUnit{Path: "skills/" + name, Entries: []PackageAdoptionEntry{
		{Path: ".", Mode: fs.ModeDir | 0o755},
		{Path: "SKILL.md", Mode: 0o644, Data: []byte(body)},
		{Path: "refs", Mode: fs.ModeDir | 0o755},
		{Path: "refs/guide.md", Mode: 0o755, Data: []byte(guide)},
	}}
}

func TestPackageAdoptionWholeTrees(t *testing.T) {
	for _, scenario := range []string{"current", "old", "old-partial", "old-extra", "retired", "mixed",
		"unknown", "changed", "extra", "empty-extra",
		"missing", "missing-skill-disabled", "marker", "marker-only", "empty", "absent", "umask", "mode", "dir-mode",
		"special-mode", "link", "ancestor", "fifo", "manual", "manual-tail", "style", "top-level", "instance", "wrong-kind"} {
		t.Run(scenario, func(t *testing.T) {
			cfg := corePackageFixture(t)
			writeSnapshotFile(t, cfg.AppSrcDir, "ant/skills/base/refs/guide.md", "current guide")
			if err := EnsureCorePackage(cfg, nil); err != nil {
				t.Fatal(err)
			}
			selected := PackageAdoptionSelection{System: selectedSnapshots(t, cfg.ProjectRoot, "")}
			home := filepath.Join(cfg.GroupsDir, "team/exact")
			dir := filepath.Join(home, ".claude/skills/base")
			body, guide, name := "release skill", "current guide", "base"
			var history []PackageAdoptionHistory
			if strings.HasPrefix(scenario, "old") || scenario == "retired" || scenario == "mixed" {
				body, guide = "old skill", "old guide"
				if scenario == "retired" {
					name = "retired"
					dir = filepath.Join(home, ".claude/skills/retired")
				}
				history = []PackageAdoptionHistory{{Package: "core", Source: "ant", Commit: strings.Repeat("a", 40),
					Units: []PackageAdoptionUnit{adoptionOldUnit(name, body, guide)}}}
				if scenario == "mixed" {
					history = append(history, PackageAdoptionHistory{Package: "core", Source: "ant", Commit: strings.Repeat("b", 64),
						Units: []PackageAdoptionUnit{adoptionOldUnit(name, "other skill", "other guide")}})
					guide = "other guide"
				}
				switch scenario {
				case "old-partial":
					history[0].Units[0].Entries = history[0].Units[0].Entries[:2]
				case "old-extra":
					history[0].Units[0].Entries = append(history[0].Units[0].Entries,
						PackageAdoptionEntry{Path: "extra", Mode: 0o644, Data: []byte("old extra")})
				}
			}
			switch scenario {
			case "top-level":
				dir = filepath.Join(home, "skills/base")
			case "instance":
				dir = filepath.Join(cfg.ProjectRoot, "skills/base")
			}
			writeSnapshotFile(t, dir, "SKILL.md", body)
			writeSnapshotFile(t, dir, "refs/guide.md", guide)
			writeSnapshotFile(t, home, ".claude/skills/custom/SKILL.md", "custom")
			writeSnapshotFile(t, cfg.GroupsDir, "team/.claude/skills/base/SKILL.md", "parent instructions")
			writeSnapshotFile(t, home, ".claude/output-styles/custom.md", "custom style")
			writeSnapshotFile(t, home, "PERSONA.md", "persona")
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			switch scenario {
			case "changed", "unknown":
				writeSnapshotFile(t, dir, "SKILL.md", "operator bytes")
			case "extra":
				writeSnapshotFile(t, dir, "extra", "operator extra")
			case "empty-extra":
				must(os.Mkdir(filepath.Join(dir, "extra-empty"), 0o755))
			case "missing":
				must(os.Remove(filepath.Join(dir, "refs/guide.md")))
			case "marker", "missing-skill-disabled", "marker-only":
				writeSnapshotFile(t, dir, ".disabled", "exact marker bytes")
				must(os.Chmod(filepath.Join(dir, ".disabled"), 0o600))
			}
			if scenario == "missing-skill-disabled" || scenario == "marker-only" || scenario == "empty" || scenario == "absent" {
				must(os.Remove(filepath.Join(dir, "SKILL.md")))
			}
			if scenario == "marker-only" || scenario == "empty" || scenario == "absent" {
				must(os.Remove(filepath.Join(dir, "refs/guide.md")))
				must(os.Remove(filepath.Join(dir, "refs")))
			}
			if scenario == "absent" {
				must(os.Remove(dir))
			}
			switch scenario {
			case "umask":
				must(os.Chmod(dir, 0o700))
				must(os.Chmod(filepath.Join(dir, "SKILL.md"), 0o400))
			case "mode":
				must(os.Chmod(filepath.Join(dir, "SKILL.md"), 0o744))
			case "dir-mode":
				must(os.Chmod(dir, 0o777))
			case "special-mode":
				must(os.Chmod(filepath.Join(dir, "SKILL.md"), fs.ModeSetuid|0o644))
			case "link":
				must(os.Remove(filepath.Join(dir, "SKILL.md")))
				must(os.Symlink(filepath.Join(home, "PERSONA.md"), filepath.Join(dir, "SKILL.md")))
			case "ancestor":
				must(os.Rename(filepath.Dir(dir), filepath.Dir(dir)+"-saved"))
				must(os.Symlink(filepath.Dir(dir)+"-saved", filepath.Dir(dir)))
			case "fifo":
				must(unix.Mkfifo(filepath.Join(dir, "pipe"), 0o644))
			case "manual", "manual-tail":
				manual := "edited manual"
				if scenario == "manual-tail" {
					manual = "release manual\ncustom tail"
				}
				writeSnapshotFile(t, home, ".claude/CLAUDE.md", manual)
			case "style":
				writeSnapshotFile(t, home, ".claude/output-styles/brief.md", "edited style")
			case "wrong-kind":
				must(os.Rename(dir, dir+"-saved"))
				writeSnapshotFile(t, filepath.Dir(dir), filepath.Base(dir), "")
			}
			plan, err := checkedAdoption(t, cfg, selected, nil, history)
			if err != nil {
				t.Fatal(err)
			}
			clean := slices.Contains([]string{"current", "old", "retired", "marker", "marker-only",
				"empty", "absent", "umask", "top-level", "instance"}, scenario)
			if clean != (len(plan.Conflicts) == 0) {
				t.Fatalf("unexpected plan: %+v", plan)
			}
			if clean && scenario != "marker-only" && scenario != "empty" && scenario != "absent" {
				if len(plan.Copies) != 1 || plan.Copies[0].Path != dir || len(plan.Copies[0].Entries) != 4 {
					t.Fatalf("incomplete copy: %+v", plan)
				}
				if scenario == "marker" && (plan.Copies[0].Disabled == nil ||
					string(plan.Copies[0].Disabled.Data) != "exact marker bytes" || plan.Copies[0].Disabled.Mode != 0o600) {
					t.Fatalf("marker preimage lost: %+v", plan.Copies[0])
				}
			}
			for _, conflict := range plan.Conflicts {
				if !filepath.IsAbs(conflict.Path) || conflict.Reason == "" {
					t.Fatalf("inexact conflict: %+v", conflict)
				}
			}
		})
	}
}

func TestPackageAdoptionReceiptAndBoundHistory(t *testing.T) {
	for _, scenario := range []string{"receipt", "history", "partial", "extra", "empty-extra", "names-only", "bad-hash",
		"bad-path", "unowned", "wrong-folder", "wrong-source", "wrong-revision", "no-receipt", "instance", "top-level"} {
		t.Run(scenario, func(t *testing.T) {
			cfg := corePackageFixture(t)
			if err := EnsureCorePackage(cfg, nil); err != nil {
				t.Fatal(err)
			}
			selected := PackageAdoptionSelection{System: selectedSnapshots(t, cfg.ProjectRoot, "")}
			source := snapshotSource(t, "optional", "current optional")
			selected.Group = []PackageSnapshot{prepareSnapshot(t, cfg.ProjectRoot, source)}
			home := filepath.Join(cfg.GroupsDir, "team/exact")
			dir := filepath.Join(home, ".claude/skills/triage")
			if scenario == "instance" {
				dir = filepath.Join(cfg.ProjectRoot, "skills/triage")
			}
			if scenario == "top-level" {
				dir = filepath.Join(home, "skills/triage")
			}
			writeSnapshotFile(t, dir, "SKILL.md", "old optional")
			writeSnapshotFile(t, dir, "refs/guide.md", "old guide")
			receipt := store.InstalledPackage{Folder: "team/exact", Name: "optional", Source: "configured-origin",
				Revision: strings.Repeat("c", 40), Manifest: map[string][]string{"skill": {"triage"}},
				AssetHashes: map[string]string{"file:.claude/skills/triage/SKILL.md": hashFile(t, filepath.Join(dir, "SKILL.md")),
					"file:.claude/skills/triage/refs/guide.md": hashFile(t, filepath.Join(dir, "refs/guide.md"))}}
			previous := PackageAdoptionHistory{Folder: receipt.Folder, Package: receipt.Name, Source: receipt.Source,
				Commit: receipt.Revision, Units: []PackageAdoptionUnit{adoptionOldUnit("triage", "old optional", "old guide")}}
			var history []PackageAdoptionHistory
			switch scenario {
			case "history", "wrong-folder", "wrong-source", "wrong-revision", "no-receipt":
				receipt.AssetHashes = nil
				history = []PackageAdoptionHistory{previous}
			case "partial":
				delete(receipt.AssetHashes, "file:.claude/skills/triage/refs/guide.md")
			case "extra":
				writeSnapshotFile(t, dir, "extra", "custom")
			case "empty-extra":
				if err := os.Mkdir(filepath.Join(dir, "extra-empty"), 0o755); err != nil {
					t.Fatal(err)
				}
			case "names-only":
				receipt.AssetHashes = nil
			case "bad-hash":
				receipt.AssetHashes["file:.claude/skills/triage/SKILL.md"] = "invalid"
			case "bad-path":
				receipt.AssetHashes["file:.claude/skills/triage/../escape"] = strings.Repeat("0", 64)
			case "unowned":
				receipt.AssetHashes["file:.claude/skills/custom/SKILL.md"] = strings.Repeat("0", 64)
			}
			if scenario == "wrong-folder" {
				history[0].Folder = "team"
			}
			if scenario == "wrong-source" {
				history[0].Source = "other-origin"
			}
			if scenario == "wrong-revision" {
				history[0].Commit = strings.Repeat("d", 40)
			}
			receipts := []store.InstalledPackage{receipt}
			if scenario == "no-receipt" {
				receipts = nil
			}
			plan, err := checkedAdoption(t, cfg, selected, receipts, history)
			invalid := slices.Contains([]string{"bad-hash", "bad-path", "unowned", "wrong-folder",
				"wrong-source", "wrong-revision", "no-receipt"}, scenario)
			if invalid {
				if err == nil {
					t.Fatalf("invalid binding accepted: %+v", plan)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			clean := scenario == "receipt" || scenario == "history"
			if clean != (len(plan.Conflicts) == 0) || clean && len(plan.Copies) != 1 {
				t.Fatalf("unexpected proof result: %+v", plan)
			}
			if clean && !strings.Contains(plan.Copies[0].Proof, receipt.Revision) {
				t.Fatalf("proof identity missing: %+v", plan.Copies[0])
			}
		})
	}
}

func TestPackageAdoptionRejectsInvalidInputs(t *testing.T) {
	for _, scenario := range []string{"manifest", "missing-core", "group-core", "duplicate", "abbreviated",
		"uppercase", "local", "duplicate-entry", "traversal", "absolute", "backslash", "parent", "link-mode",
		"dir-data", "marker", "missing-skill", "duplicate-unit", "overlap"} {
		t.Run(scenario, func(t *testing.T) {
			cfg := corePackageFixture(t)
			if err := EnsureCorePackage(cfg, nil); err != nil {
				t.Fatal(err)
			}
			selected := PackageAdoptionSelection{System: selectedSnapshots(t, cfg.ProjectRoot, "")}
			unit := adoptionOldUnit("base", "old skill", "old guide")
			previous := PackageAdoptionHistory{Package: "core", Source: "ant", Commit: strings.Repeat("a", 40)}
			switch scenario {
			case "manifest":
				dir, err := ResolvePackage(cfg.ProjectRoot, selected.System[0])
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(filepath.Join(dir, ManifestFile)); err != nil {
					t.Fatal(err)
				}
			case "missing-core":
				selected.System = nil
			case "group-core":
				selected.Group = selected.System
			case "duplicate":
				selected.System = append(selected.System, selected.System[0])
			case "abbreviated":
				previous.Commit = "aabbcc"
			case "uppercase":
				previous.Commit = strings.Repeat("A", 40)
			case "local":
				previous.Commit = "local"
			case "duplicate-entry":
				unit.Entries = append(unit.Entries, unit.Entries[1])
			case "traversal":
				unit.Path = "skills/../base"
			case "absolute":
				unit.Entries[1].Path = "/SKILL.md"
			case "backslash":
				unit.Entries[1].Path = "refs\\SKILL.md"
			case "parent":
				unit.Entries = append(unit.Entries[:2], unit.Entries[3])
			case "link-mode":
				unit.Entries[1].Mode = fs.ModeSymlink
			case "dir-data":
				unit.Entries[0].Data = []byte("not directory bytes")
			case "marker":
				unit.Entries = append(unit.Entries, PackageAdoptionEntry{Path: ".disabled", Mode: 0o644})
			case "missing-skill":
				unit.Entries = append(unit.Entries[:1], unit.Entries[2:]...)
			case "overlap":
				selected.Group = []PackageSnapshot{prepareSnapshot(t, cfg.ProjectRoot, snapshotSource(t, "other", "other"))}
				unit.Path = "skills/triage"
			}
			previous.Units = []PackageAdoptionUnit{unit}
			if scenario == "duplicate-unit" {
				previous.Units = append(previous.Units, unit)
			}
			plan, err := checkedAdoption(t, cfg, selected, nil, []PackageAdoptionHistory{previous})
			if err == nil {
				t.Fatalf("invalid input accepted: %+v", plan)
			}
		})
	}
}

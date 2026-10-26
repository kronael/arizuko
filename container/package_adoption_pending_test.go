package container

import (
	"fmt"
	"io/fs"
	"maps"
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

func TestPackageAdoptionPendingEncoding(t *testing.T) {
	cfg := corePackageFixture(t)
	if err := EnsureCorePackage(cfg, nil); err != nil {
		t.Fatal(err)
	}
	selected := PackageAdoptionSelection{System: selectedSnapshots(t, cfg.ProjectRoot, "")}
	writeSnapshotFile(t, filepath.Join(cfg.GroupsDir, "team/exact"),
		".claude/skills/base/SKILL.md", "release skill")
	plan, err := checkedAdoption(t, cfg, selected, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	units, err := readPackageAdoptionUnits(cfg, "team/exact", selected, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	binding := adoptionBinding{snapshot: selected.System[0], source: "ant", revision: selected.System[0].Digest}
	hashes, err := encodePackageAdoption(cfg, binding, packageAdoptionTargets("team/exact", units), plan)
	if err != nil || len(hashes) != 5 || !HasPendingAdoption(hashes) {
		t.Fatalf("complete copied skill requires five pending hashes: %v, %v", hashes, err)
	}
}

type pendingAdoptionFixture struct {
	cfg      *core.Config
	binding  adoptionBinding
	selected PackageAdoptionSelection
	receipts []store.InstalledPackage
	history  []PackageAdoptionHistory
	targets  []adoptionTarget
	hashes   map[string]string
	dir      string
	plan     PackageAdoptionPlan
}

func newPendingAdoptionFixture(t *testing.T, proof string) pendingAdoptionFixture {
	t.Helper()
	f := pendingAdoptionFixture{cfg: corePackageFixture(t)}
	writeSnapshotFile(t, f.cfg.AppSrcDir, "ant/skills/base/refs/guide.md", "current guide")
	if err := EnsureCorePackage(f.cfg, nil); err != nil {
		t.Fatal(err)
	}
	f.selected.System = selectedSnapshots(t, f.cfg.ProjectRoot, "")
	f.binding = adoptionBinding{snapshot: f.selected.System[0], source: "ant", revision: f.selected.System[0].Digest}
	name, body, guide := "base", "release skill", "current guide"
	if proof == "retired" {
		name, body, guide = "retired", "old skill", "old guide"
		f.history = []PackageAdoptionHistory{{Package: "core", Source: "ant", Commit: strings.Repeat("a", 40),
			Units: []PackageAdoptionUnit{adoptionOldUnit(name, body, guide)}}}
	}
	if proof == "receipt" || proof == "history" {
		f.selected.Group = []PackageSnapshot{prepareSnapshot(t, f.cfg.ProjectRoot,
			snapshotSource(t, "optional", "current optional"))}
		name, body, guide = "triage", "old optional", "old guide"
		receipt := store.InstalledPackage{Folder: "team/exact", Name: "optional", Source: "configured-origin",
			Revision: strings.Repeat("c", 40), Manifest: map[string][]string{"skill": {"triage"}}}
		if proof == "receipt" {
			receipt.AssetHashes = map[string]string{"file:.claude/skills/triage/SKILL.md": hashBytes([]byte(body)),
				"file:.claude/skills/triage/refs/guide.md": hashBytes([]byte(guide))}
		} else {
			f.history = []PackageAdoptionHistory{{Folder: receipt.Folder, Package: receipt.Name,
				Source: receipt.Source, Commit: receipt.Revision,
				Units: []PackageAdoptionUnit{adoptionOldUnit(name, body, guide)}}}
		}
		f.receipts = []store.InstalledPackage{receipt}
		f.binding = adoptionBinding{folder: receipt.Folder, snapshot: f.selected.Group[0],
			source: receipt.Source, revision: receipt.Revision}
	}
	f.dir = filepath.Join(f.cfg.GroupsDir, "team/exact/.claude/skills", name)
	writeSnapshotFile(t, f.dir, "SKILL.md", body)
	writeSnapshotFile(t, f.dir, "refs/guide.md", guide)
	writeSnapshotFile(t, f.dir, ".disabled", "operator marker")
	if err := os.Chmod(filepath.Join(f.dir, "SKILL.md"), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(f.dir, "refs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if proof == "current" {
		writeSnapshotFile(t, f.cfg.ProjectRoot, "skills/base/SKILL.md", body)
		writeSnapshotFile(t, f.cfg.ProjectRoot, "skills/base/refs/guide.md", guide)
	}
	var err error
	f.plan, err = checkedAdoption(t, f.cfg, f.selected, f.receipts, f.history)
	if err != nil || len(f.plan.Conflicts) != 0 || len(f.plan.Copies) == 0 {
		t.Fatalf("invalid fixture plan: %+v, %v", f.plan, err)
	}
	units, err := readPackageAdoptionUnits(f.cfg, "team/exact", f.selected, f.receipts, f.history)
	if err != nil {
		t.Fatal(err)
	}
	f.targets = packageAdoptionTargets("team/exact", units)
	f.hashes, err = encodePackageAdoption(f.cfg, f.binding, f.targets, f.plan)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func checkedPendingAdoption(t *testing.T, f pendingAdoptionFixture) ([]adoptionTree, error) {
	t.Helper()
	before := adoptionDisk(t, f.cfg.ProjectRoot)
	inputs := fmt.Sprintf("%#v %#v %#v %#v", f.selected, f.receipts, f.history, f.hashes)
	units, err := readPackageAdoptionUnits(f.cfg, "team/exact", f.selected, f.receipts, f.history)
	var trees []adoptionTree
	if err == nil {
		trees, err = readPackageAdoptionLedger(f.binding, f.hashes, packageAdoptionTargets("team/exact", units))
	}
	if err == nil {
		err = checkPackageAdoptionTrees(f.cfg, trees)
	}
	if !reflect.DeepEqual(before, adoptionDisk(t, f.cfg.ProjectRoot)) ||
		inputs != fmt.Sprintf("%#v %#v %#v %#v", f.selected, f.receipts, f.history, f.hashes) {
		t.Fatal("recovery changed instance bytes, modes, selections, receipts or proofs")
	}
	return trees, err
}

func TestPackageAdoptionPendingRecovery(t *testing.T) {
	for _, proof := range []string{"current", "retired", "receipt", "history"} {
		t.Run(proof, func(t *testing.T) {
			f := newPendingAdoptionFixture(t, proof)
			if err := os.Remove(filepath.Join(f.dir, "SKILL.md")); err != nil {
				t.Fatal(err)
			}
			trees, err := checkedPendingAdoption(t, f)
			if err != nil {
				t.Fatal(err)
			}
			for _, tree := range trees {
				if tree.target.root != "groups" {
					continue
				}
				for _, entry := range tree.entries {
					if entry.path == "SKILL.md" && entry.mode != 0o400 ||
						entry.path == "refs" && entry.mode != fs.ModeDir|0o700 {
						t.Fatalf("original exact mode lost: %+v", entry)
					}
				}
			}
			if err := os.Remove(filepath.Join(f.dir, "refs/guide.md")); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(f.dir, "refs")); err != nil {
				t.Fatal(err)
			}
			if _, err := checkedPendingAdoption(t, f); err != nil {
				t.Fatal(err)
			}
			if proof == "current" {
				for _, rel := range []string{"SKILL.md", "refs/guide.md", "refs", "."} {
					if err := os.Remove(filepath.Join(f.cfg.ProjectRoot, "skills/base", rel)); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := checkedPendingAdoption(t, f); err != nil {
					t.Fatalf("fully removed legacy unit refused: %v", err)
				}
			}
			if proof == "retired" {
				f.history = nil
				if _, err := checkedPendingAdoption(t, f); err == nil {
					t.Fatal("retired unit accepted without its original history")
				}
			}
		})
	}
}

func TestPackageAdoptionPendingRefusals(t *testing.T) {
	for _, scenario := range []string{"source", "revision", "candidate", "history-id", "missing-member",
		"whole-tree", "bindings-only", "unknown", "orphan", "overlap", "cross-folder", "owner", "bad-hash",
		"noncanonical", "missing-binding", "extra", "edit", "mode", "marker-edit", "marker-mode", "marker-missing",
		"root-missing", "new-marker", "ancestor", "link", "fifo"} {
		t.Run(scenario, func(t *testing.T) {
			proof := "current"
			if scenario == "history-id" {
				proof = "retired"
			}
			f := newPendingAdoptionFixture(t, proof)
			treeKey := adoptionPrefix + "tree:groups/team/exact/.claude/skills/base"
			fileKey := adoptionPrefix + "file:groups/team/exact/.claude/skills/base/SKILL.md"
			switch scenario {
			case "source":
				f.binding.source = "other"
			case "revision":
				f.binding.revision = "other"
			case "candidate":
				f.binding.snapshot.Digest = strings.Repeat("b", 64)
				f.binding.revision = f.binding.snapshot.Digest
			case "history-id":
				f.history[0].Commit = strings.Repeat("b", 40)
			case "missing-member":
				delete(f.hashes, fileKey)
			case "whole-tree":
				for key := range f.hashes {
					if strings.Contains(key, ":skills/base") {
						delete(f.hashes, key)
					}
				}
			case "bindings-only":
				for key := range f.hashes {
					if key != adoptionPrefix+"source" && key != adoptionPrefix+"package_source" {
						delete(f.hashes, key)
					}
				}
			case "unknown":
				f.hashes[adoptionPrefix+"unexpected"] = strings.Repeat("a", 64)
			case "orphan":
				f.hashes[adoptionPrefix+"file:groups/team/exact/orphan"] = strings.Repeat("a", 64)
			case "overlap":
				f.hashes[treeKey+"/refs"] = strings.Repeat("a", 64)
			case "cross-folder":
				f.hashes[strings.Replace(treeKey, "team/exact", "team/other", 1)] = f.hashes[treeKey]
			case "owner":
				f.binding.snapshot.Name = "other"
			case "bad-hash":
				f.hashes[fileKey] = strings.Repeat("A", 64)
			case "noncanonical":
				f.hashes[treeKey+"/../base"] = f.hashes[treeKey]
			case "missing-binding":
				delete(f.hashes, adoptionPrefix+"source")
			case "extra":
				writeSnapshotFile(t, f.dir, "extra", "private extra")
			case "edit":
				writeSnapshotFile(t, f.dir, "refs/guide.md", "private edit")
			case "mode":
				if err := os.Chmod(filepath.Join(f.dir, "refs/guide.md"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "marker-edit":
				writeSnapshotFile(t, f.dir, ".disabled", "changed marker")
			case "marker-mode":
				if err := os.Chmod(filepath.Join(f.dir, ".disabled"), 0o400); err != nil {
					t.Fatal(err)
				}
			case "marker-missing", "root-missing", "new-marker":
				if err := os.Remove(filepath.Join(f.dir, ".disabled")); err != nil {
					t.Fatal(err)
				}
				if scenario != "marker-missing" {
					f.plan.Copies[0].Disabled = nil
					var err error
					f.hashes, err = encodePackageAdoption(f.cfg, f.binding, f.targets, f.plan)
					if err != nil {
						t.Fatal(err)
					}
					if scenario == "root-missing" {
						if err := os.Rename(f.dir, f.dir+"-moved"); err != nil {
							t.Fatal(err)
						}
					} else {
						writeSnapshotFile(t, f.dir, ".disabled", "new marker")
					}
				}
			case "ancestor":
				parent := filepath.Dir(f.dir)
				if err := os.Rename(parent, parent+"-moved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(parent+"-moved", parent); err != nil {
					t.Fatal(err)
				}
			case "link", "fifo":
				file := filepath.Join(f.dir, "refs/guide.md")
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
				var err error
				if scenario == "link" {
					err = os.Symlink("../SKILL.md", file)
				} else {
					err = unix.Mkfifo(file, 0o600)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := checkedPendingAdoption(t, f); err == nil {
				t.Fatal("changed or malformed pending adoption accepted")
			}
		})
	}
}

func TestPackageAdoptionPendingCoreFileAbsence(t *testing.T) {
	f := newPendingAdoptionFixture(t, "current")
	home := filepath.Join(f.cfg.GroupsDir, "team/exact")
	writeSnapshotFile(t, home, ".claude/CLAUDE.md", "release manual")
	writeSnapshotFile(t, home, ".claude/output-styles/brief.md", "release style")
	var err error
	f.plan, err = checkedAdoption(t, f.cfg, f.selected, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.hashes, err = encodePackageAdoption(f.cfg, f.binding, f.targets, f.plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{".claude/CLAUDE.md", ".claude/output-styles/brief.md"} {
		if err := os.Remove(filepath.Join(home, rel)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := checkedPendingAdoption(t, f); err != nil {
		t.Fatalf("fully removed core files refused: %v", err)
	}
}

func TestPackageAdoptionPendingModePreimage(t *testing.T) {
	for _, kind := range []string{"file", "dir"} {
		t.Run(kind, func(t *testing.T) {
			f := newPendingAdoptionFixture(t, "current")
			trees, err := readPackageAdoptionLedger(f.binding, f.hashes, f.targets)
			if err != nil {
				t.Fatal(err)
			}
			entryPath, mode, content := "SKILL.md", fs.FileMode(0o200), hashBytes([]byte("release skill"))
			if kind == "dir" {
				entryPath, mode = "refs", fs.ModeDir|0o400
				if err := os.Remove(filepath.Join(f.dir, "refs/guide.md")); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Remove(filepath.Join(f.dir, entryPath)); err != nil {
				t.Fatal(err)
			}
			fingerprint, err := adoptionFingerprint(mode, content)
			if err != nil {
				t.Fatal(err)
			}
			for i := range trees {
				if trees[i].target.root != "groups" {
					continue
				}
				for j := range trees[i].entries {
					if trees[i].entries[j].path == entryPath {
						trees[i].entries[j].fingerprint = fingerprint
						f.hashes[adoptionEntryKey(trees[i].target, kind, entryPath)] = fingerprint
					}
				}
			}
			set, err := adoptionSet(f.hashes)
			if err != nil {
				t.Fatal(err)
			}
			for _, tree := range trees {
				display, err := adoptionDisplay(f.cfg, tree.target)
				if err != nil {
					t.Fatal(err)
				}
				for _, copy := range f.plan.Copies {
					if copy.Path == display {
						value, err := adoptionTreeHash(f.binding, set, copy.Proof, tree)
						if err != nil {
							t.Fatal(err)
						}
						f.hashes[adoptionEntryKey(tree.target, "tree", ".")] = value
					}
				}
			}
			if _, err := checkedPendingAdoption(t, f); err == nil {
				t.Fatal("absent entry accepted without an allowed original mode preimage")
			}
		})
	}
}

func TestPackageAdoptionPendingPlanValidation(t *testing.T) {
	for _, scenario := range []string{"conflict", "path", "duplicate", "proof", "entries", "marker", "target",
		"cross-folder", "empty", "other-owner"} {
		t.Run(scenario, func(t *testing.T) {
			f := newPendingAdoptionFixture(t, "current")
			before := adoptionDisk(t, f.cfg.ProjectRoot)
			switch scenario {
			case "conflict":
				f.plan.Conflicts = []PackageAdoptionConflict{{Path: "other-owner", Reason: "unproved"}}
			case "path":
				f.plan.Copies[0].Path += "/../base"
			case "duplicate":
				f.plan.Copies = append(f.plan.Copies, f.plan.Copies[0])
			case "proof":
				f.plan.Copies[0].Proof = "git:unbound"
			case "entries":
				f.plan.Copies[0].Entries = f.plan.Copies[0].Entries[:2]
			case "marker":
				f.plan.Copies[0].Disabled.Path = "other"
			case "target":
				f.targets[0].relative = "team/exact/unmanaged"
			case "cross-folder":
				f.binding = adoptionBinding{folder: "team/other", snapshot: f.binding.snapshot,
					source: f.binding.source, revision: f.binding.revision}
			case "empty":
				f.plan.Copies = nil
			case "other-owner":
				f.binding = adoptionBinding{snapshot: PackageSnapshot{Name: "other", Digest: strings.Repeat("a", 64)},
					source: "origin", revision: "revision"}
			}
			hashes, err := encodePackageAdoption(f.cfg, f.binding, f.targets, f.plan)
			if scenario == "empty" || scenario == "other-owner" {
				if err != nil || len(hashes) != 0 {
					t.Fatalf("no selected copies must produce no namespace: %v, %v", hashes, err)
				}
			} else if err == nil {
				t.Fatal("invalid plan encoded")
			}
			if !reflect.DeepEqual(before, adoptionDisk(t, f.cfg.ProjectRoot)) {
				t.Fatal("encoding changed filesystem")
			}
		})
	}
	for _, key := range []string{"pending:adopt:unknown", "pending:adopt:", "pending:adopt:source"} {
		if !HasPendingAdoption(map[string]string{key: "malformed"}) {
			t.Fatalf("pending namespace not detected: %s", key)
		}
	}
	f := newPendingAdoptionFixture(t, "receipt")
	f.binding.snapshot.Digest = strings.Repeat("a", 64)
	hashes, err := encodePackageAdoption(f.cfg, f.binding, f.targets, f.plan)
	if err != nil {
		t.Fatal(err)
	}
	rel := "groups/team/exact/.claude/skills/triage"
	want := map[string]string{
		adoptionPrefix + "dir:" + rel:                     "2226ec10b26b5b8fcbf6a02bf93f26643c5bd5d1bb440c461f36c72b19323f2a",
		adoptionPrefix + "dir:" + rel + "/refs":           "c93cf31ecbc327f3c1096be01673cd30c116e49ae8e08f7d2a8f95714a975770",
		adoptionPrefix + "file:" + rel + "/SKILL.md":      "71cbc809e55d7c2ffbd0456a97f1fc71b36470e5b6ec75e24621d270a6c466b5",
		adoptionPrefix + "file:" + rel + "/refs/guide.md": "7cd48cd7b289ca7181a0ef16660c25306157e66980c4ed4720239a900c25d409",
		adoptionPrefix + "marker:" + rel + "/.disabled":   "6515183e73123e53aac37226ca43edd568d6246fb6546cf8902ceaa0e8b21bdc",
		adoptionPrefix + "package_source":                 strings.Repeat("a", 64),
		adoptionPrefix + "source":                         "f81307ad051cba56a489327be4834198cf57d671f8630529defb042288896a77",
		adoptionPrefix + "tree:" + rel:                    "cd218f66a96706519f36572c3544d87738be248d8cfab0a1d480d05bcb040e72",
	}
	if !maps.Equal(hashes, want) {
		t.Fatalf("ledger differs from independently encoded hash vector: %v", hashes)
	}
	f.hashes = hashes
	f.hashes["pending:settings:unrelated"] = "unchanged"
	before := maps.Clone(f.hashes)
	if _, err := checkedPendingAdoption(t, f); err != nil || !maps.Equal(before, f.hashes) {
		t.Fatalf("unrelated namespace changed or refused: %v", err)
	}
	if !slices.ContainsFunc(f.plan.Copies, func(copy PackageAdoptionCopy) bool {
		return strings.HasPrefix(copy.Proof, "receipt:")
	}) {
		t.Fatal("receipt fixture did not use receipt proof")
	}
}

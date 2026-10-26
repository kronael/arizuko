package container

import (
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

type adoptionApplyFixture struct {
	pendingAdoptionFixture
	database *store.Store
	batch    adoptionBatch
}

func newAdoptionApplyFixture(t *testing.T) adoptionApplyFixture {
	t.Helper()
	f := adoptionApplyFixture{pendingAdoptionFixture: newPendingAdoptionFixture(t, "current"),
		database: corePackageDatabase(t)}
	if err := f.database.PutGroup(core.Group{Folder: "team/exact"}); err != nil {
		t.Fatal(err)
	}
	if err := ActivatePackages(f.cfg.ProjectRoot, "", f.database, f.selected.System,
		[]store.InstalledPackage{{Source: "ant", Revision: f.binding.revision}}); err != nil {
		t.Fatal(err)
	}
	row, _, err := f.database.InstalledPackage("", "core")
	if err != nil {
		t.Fatal(err)
	}
	row.AssetHashes[PendingSettingsArtifact] = f.binding.snapshot.Digest
	row.AssetHashes[PendingSettingsSource] = hashBytes([]byte("ant"))
	row.AssetHashes[PendingSettingPrefix+"KEEP"] = hashBytes([]byte("operator"))
	row.AssetHashes["file:PERSONA.md"] = hashBytes([]byte("operator persona"))
	row.AssetHashes["file:.claude/skills/base/SKILL.md"] = hashBytes([]byte("release skill"))
	row.AssetHashes["file:.claude/skills/base/refs/guide.md"] = hashBytes([]byte("current guide"))
	if err := f.database.PutInstalledPackage(row); err != nil {
		t.Fatal(err)
	}
	f.batch = adoptionBatch{cfg: *f.cfg, bindings: []adoptionBinding{f.binding},
		scopes: []adoptionScope{{folder: "team/exact", active: f.selected,
			selected: f.selected, receipts: []store.InstalledPackage{row}, plan: f.plan}}}
	return f
}

func adoptionFinalReceipts(t *testing.T, f adoptionApplyFixture) []store.InstalledPackage {
	t.Helper()
	var receipts []store.InstalledPackage
	for _, snapshot := range f.batch.scopes[0].selected.System {
		row, found, err := f.database.InstalledPackage("", snapshot.Name)
		if err != nil || !found {
			t.Fatalf("missing prepared receipt: %+v, %v", snapshot, err)
		}
		row = cloneAdoptionReceipt(row)
		maps.DeleteFunc(row.AssetHashes, func(key, _ string) bool {
			return strings.HasPrefix(key, "pending:")
		})
		receipts = append(receipts, row)
	}
	return receipts
}

func addAdoptionApplyOptional(t *testing.T, f *adoptionApplyFixture, name string) {
	t.Helper()
	snapshot := prepareSnapshot(t, f.cfg.ProjectRoot, snapshotSource(t, name, "optional"))
	writeSnapshotFile(t, filepath.Join(f.cfg.GroupsDir, "team/exact"),
		".claude/skills/triage/SKILL.md", "optional")
	f.batch.bindings = append(f.batch.bindings, adoptionBinding{snapshot: snapshot,
		source: name + "-origin", revision: snapshot.Digest})
	f.batch.scopes[0].selected.System = append(f.batch.scopes[0].selected.System, snapshot)
	plan, err := PlanPackageAdoption(f.cfg, "team/exact", f.batch.scopes[0].selected,
		f.batch.scopes[0].receipts, nil)
	if err != nil || len(plan.Conflicts) != 0 {
		t.Fatalf("optional fixture: %+v, %v", plan, err)
	}
	f.batch.scopes[0].plan = plan
}

func TestPackageAdoptionApplyRetryAndCompletion(t *testing.T) {
	f := newAdoptionApplyFixture(t)
	if err := prepareAdoptionReceipts(f.cfg, f.database, f.batch); err != nil {
		t.Fatal(err)
	}
	row, _, err := f.database.InstalledPackage("", "core")
	if err != nil || !HasPendingAdoption(row.AssetHashes) {
		t.Fatalf("preimage not persisted: %+v, %v", row, err)
	}
	rows, err := readAdoptionBatch(f.cfg, f.database, f.batch, adoptionApply)
	if err != nil {
		t.Fatal(err)
	}
	interrupted := false
	for _, tree := range rows[0].trees {
		for _, entry := range tree.entries {
			if !interrupted && entry.mode.IsRegular() {
				if err := unlinkAdoptionEntry(f.cfg, tree, entry); err != nil {
					t.Fatal(err)
				}
				interrupted = true
			}
		}
	}
	if !interrupted {
		t.Fatal("fixture did not interrupt a named unlink")
	}
	if err := applyPackageAdoption(f.cfg, f.database, f.batch); err != nil {
		t.Fatal(err)
	}
	if err := applyPackageAdoption(f.cfg, f.database, f.batch); err != nil {
		t.Fatalf("retry after complete legacy-root removal: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.dir, "SKILL.md")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("payload remains: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(f.dir, ".disabled")); err != nil || string(data) != "operator marker" {
		t.Fatalf("marker changed: %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(f.cfg.ProjectRoot, "skills/base")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("legacy root remains: %v", err)
	}
	if err := activatePackages(f.cfg.ProjectRoot, "", f.database, f.selected.System,
		adoptionFinalReceipts(t, f), &f.batch); err != nil {
		t.Fatal(err)
	}
	got, _, err := f.database.InstalledPackage("", "core")
	if err != nil || HasPendingAdoption(got.AssetHashes) || !HasPendingSettings(got.AssetHashes) ||
		got.AssetHashes["file:PERSONA.md"] != hashBytes([]byte("operator persona")) ||
		got.AssetHashes["file:.claude/skills/base/SKILL.md"] != "" {
		t.Fatalf("completion lost pending settings or seed, or retained copied ownership: %+v, %v", got, err)
	}
}

func TestPackageAdoptionApplyPreimages(t *testing.T) {
	for _, scenario := range []string{"preview-edit", "edit", "mode", "marker", "extra",
		"new-target", "registry", "receipt", "source", "unprepared", "unbound-pending", "ledger-tree"} {
		t.Run(scenario, func(t *testing.T) {
			f := newAdoptionApplyFixture(t)
			if scenario != "preview-edit" && scenario != "unprepared" {
				if err := prepareAdoptionReceipts(f.cfg, f.database, f.batch); err != nil {
					t.Fatal(err)
				}
			}
			switch scenario {
			case "preview-edit", "edit":
				writeSnapshotFile(t, f.dir, "refs/guide.md", "operator edit")
			case "mode":
				if err := os.Chmod(filepath.Join(f.dir, "refs/guide.md"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "marker":
				writeSnapshotFile(t, f.dir, ".disabled", "changed marker")
			case "extra":
				writeSnapshotFile(t, f.dir, "extra", "operator extra")
			case "new-target":
				writeSnapshotFile(t, filepath.Join(f.cfg.GroupsDir, "team/exact"),
					".claude/CLAUDE.md", "operator manual")
			case "registry":
				if err := f.database.PutGroup(core.Group{Folder: "team/added"}); err != nil {
					t.Fatal(err)
				}
			case "receipt", "unbound-pending", "ledger-tree":
				row, _, err := f.database.InstalledPackage("", "core")
				if err != nil {
					t.Fatal(err)
				}
				switch scenario {
				case "receipt":
					row.AssetHashes["file:PERSONA.md"] = "changed"
				case "ledger-tree":
					maps.DeleteFunc(row.AssetHashes, func(key, _ string) bool {
						return strings.Contains(key, ":skills/base")
					})
				default:
					row = store.InstalledPackage{Name: "other", Source: "origin", Revision: "revision",
						AssetHashes: map[string]string{adoptionPrefix + "unknown": "malformed"}}
				}
				if err := f.database.PutInstalledPackage(row); err != nil {
					t.Fatal(err)
				}
				if scenario == "unbound-pending" {
					f.batch.scopes[0].receipts = append(f.batch.scopes[0].receipts, row)
				}
			case "source":
				f.batch.bindings[0].revision = "changed"
			}
			before := adoptionDisk(t, f.cfg.ProjectRoot)
			rows, err := f.database.InstalledPackages()
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "preview-edit" {
				err = prepareAdoptionReceipts(f.cfg, f.database, f.batch)
			} else {
				err = applyPackageAdoption(f.cfg, f.database, f.batch)
			}
			if err == nil {
				t.Fatal("changed or unprepared operation accepted")
			}
			after, readErr := f.database.InstalledPackages()
			if readErr != nil || !reflect.DeepEqual(rows, after) ||
				!reflect.DeepEqual(before, adoptionDisk(t, f.cfg.ProjectRoot)) {
				t.Fatalf("refusal changed surviving files or receipts: %v", readErr)
			}
		})
	}
}

func TestPackageAdoptionApplyReceiptFailures(t *testing.T) {
	for _, scenario := range []string{"before-preparation", "partial-preparation", "after-selection"} {
		t.Run(scenario, func(t *testing.T) {
			f := newAdoptionApplyFixture(t)
			injected := errors.New("injected adoption receipt failure")
			failure := &packageReceiptFailure{Store: f.database.Obj(), failure: injected}
			borrowed := store.NewOver(failure)
			if scenario == "partial-preparation" {
				addAdoptionApplyOptional(t, &f, "tools")
				failure.allow = 1
			}
			before := adoptionDisk(t, f.cfg.ProjectRoot)
			if scenario != "after-selection" {
				if err := prepareAdoptionReceipts(f.cfg, borrowed, f.batch); !errors.Is(err, injected) {
					t.Fatalf("preparation failure hidden: %v", err)
				}
				if !reflect.DeepEqual(before, adoptionDisk(t, f.cfg.ProjectRoot)) {
					t.Fatal("failed preparation changed payloads")
				}
				failure.failure = nil
			}
			if err := prepareAdoptionReceipts(f.cfg, f.database, f.batch); err != nil {
				t.Fatal(err)
			}
			if err := applyPackageAdoption(f.cfg, f.database, f.batch); err != nil {
				t.Fatal(err)
			}
			if scenario == "after-selection" {
				if err := activatePackages(f.cfg.ProjectRoot, "", borrowed, f.selected.System,
					adoptionFinalReceipts(t, f), &f.batch); !errors.Is(err, injected) {
					t.Fatalf("completion failure hidden: %v", err)
				}
				row, _, err := f.database.InstalledPackage("", "core")
				if err != nil || !HasPendingAdoption(row.AssetHashes) {
					t.Fatalf("failure erased recovery proof: %+v, %v", row, err)
				}
				failure.failure = nil
			}
			if err := activatePackages(f.cfg.ProjectRoot, "", f.database, f.batch.scopes[0].selected.System,
				adoptionFinalReceipts(t, f), &f.batch); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPackageAdoptionApplySelectionFailureAndReplay(t *testing.T) {
	f := newAdoptionApplyFixture(t)
	addAdoptionApplyOptional(t, &f, "tools")
	if err := prepareAdoptionReceipts(f.cfg, f.database, f.batch); err != nil {
		t.Fatal(err)
	}
	if err := applyPackageAdoption(f.cfg, f.database, f.batch); err != nil {
		t.Fatal(err)
	}
	selected := f.batch.scopes[0].selected.System
	obstruction := filepath.Join(f.cfg.ProjectRoot, "package-selections/system", packageSelectionDigest(selected))
	if err := os.WriteFile(obstruction, []byte("selection obstruction"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := activatePackages(f.cfg.ProjectRoot, "", f.database, selected,
		adoptionFinalReceipts(t, f), &f.batch); err == nil {
		t.Fatal("obstructed selection accepted")
	}
	if got := selectedSnapshots(t, f.cfg.ProjectRoot, ""); !reflect.DeepEqual(got, f.selected.System) {
		t.Fatalf("failed selection changed active packages: %+v", got)
	}
	row, _, err := f.database.InstalledPackage("", "core")
	if err != nil || !HasPendingAdoption(row.AssetHashes) {
		t.Fatalf("selection failure erased proof: %+v, %v", row, err)
	}
	if err := os.Remove(obstruction); err != nil {
		t.Fatal(err)
	}
	if err := activatePackages(f.cfg.ProjectRoot, "", f.database, selected,
		adoptionFinalReceipts(t, f), &f.batch); err != nil {
		t.Fatal(err)
	}
	before := adoptionDisk(t, f.cfg.ProjectRoot)
	rows, err := f.database.InstalledPackages()
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareAdoptionReceipts(f.cfg, f.database, f.batch); err != nil {
		t.Fatalf("completed preparation replay: %v", err)
	}
	if err := applyPackageAdoption(f.cfg, f.database, f.batch); err != nil {
		t.Fatalf("completed apply replay: %v", err)
	}
	if err := activatePackages(f.cfg.ProjectRoot, "", f.database, selected,
		adoptionFinalReceipts(t, f), &f.batch); err != nil {
		t.Fatalf("completed activation replay: %v", err)
	}
	after, err := f.database.InstalledPackages()
	if err != nil || !reflect.DeepEqual(rows, after) || !reflect.DeepEqual(before, adoptionDisk(t, f.cfg.ProjectRoot)) {
		t.Fatalf("completed replay changed state: %v", err)
	}
}

func TestPackageAdoptionApplyAllRegisteredGroups(t *testing.T) {
	f := newAdoptionApplyFixture(t)
	if err := f.database.PutGroup(core.Group{Folder: "team/second"}); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(f.cfg.GroupsDir, "team/second")
	writeSnapshotFile(t, home, ".claude/skills/base/SKILL.md", "release skill")
	writeSnapshotFile(t, home, ".claude/skills/base/refs/guide.md", "current guide")
	writeSnapshotFile(t, home, "PERSONA.md", "second operator persona")
	plan, err := PlanPackageAdoption(f.cfg, "team/second", f.selected, f.batch.scopes[0].receipts, nil)
	if err != nil || len(plan.Conflicts) != 0 {
		t.Fatalf("second group plan: %+v, %v", plan, err)
	}
	f.batch.scopes = append(f.batch.scopes, adoptionScope{folder: "team/second",
		active: f.selected, selected: f.selected, receipts: f.batch.scopes[0].receipts, plan: plan})
	if err := prepareAdoptionReceipts(f.cfg, f.database, f.batch); err != nil {
		t.Fatal(err)
	}
	row, _, err := f.database.InstalledPackage("", "core")
	if err != nil || row.AssetHashes[adoptionPrefix+"tree:groups/team/second/.claude/skills/base"] == "" {
		t.Fatalf("system receipt omitted second group: %+v, %v", row, err)
	}
	if err := applyPackageAdoption(f.cfg, f.database, f.batch); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude/skills/base/SKILL.md")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("second group payload remains: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(home, "PERSONA.md")); err != nil || string(data) != "second operator persona" {
		t.Fatalf("second group persona changed: %q, %v", data, err)
	}
}

func TestPackageAdoptionApplyExactGroup(t *testing.T) {
	f := newPendingAdoptionFixture(t, "history")
	database := corePackageDatabase(t)
	for _, folder := range []string{"team/exact", "team/other"} {
		if err := database.PutGroup(core.Group{Folder: folder}); err != nil {
			t.Fatal(err)
		}
	}
	if err := ActivatePackages(f.cfg.ProjectRoot, "", database, f.selected.System,
		[]store.InstalledPackage{{Source: "ant", Revision: f.selected.System[0].Digest}}); err != nil {
		t.Fatal(err)
	}
	if err := ActivatePackages(f.cfg.ProjectRoot, "team/exact", database, f.selected.Group, f.receipts); err != nil {
		t.Fatal(err)
	}
	receipts, err := database.InstalledPackages()
	if err != nil {
		t.Fatal(err)
	}
	coreBefore, _, err := database.InstalledPackage("", "core")
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(f.cfg.GroupsDir, "team/other/.claude/skills/triage/SKILL.md")
	writeSnapshotFile(t, filepath.Dir(other), "SKILL.md", "other group custom")
	batch := adoptionBatch{cfg: *f.cfg, bindings: []adoptionBinding{f.binding},
		scopes: []adoptionScope{{folder: "team/exact", active: f.selected, selected: f.selected,
			receipts: receipts, history: f.history, plan: f.plan}}}
	if err := prepareAdoptionReceipts(f.cfg, database, batch); err != nil {
		t.Fatal(err)
	}
	if err := applyPackageAdoption(f.cfg, database, batch); err != nil {
		t.Fatal(err)
	}
	row, _, err := database.InstalledPackage("team/exact", "optional")
	if err != nil {
		t.Fatal(err)
	}
	if err := activatePackages(f.cfg.ProjectRoot, "team/exact", database, f.selected.Group,
		[]store.InstalledPackage{row}, &batch); err != nil {
		t.Fatal(err)
	}
	coreAfter, _, err := database.InstalledPackage("", "core")
	if err != nil || !reflect.DeepEqual(coreBefore, coreAfter) {
		t.Fatalf("group operation changed core receipt: %+v, %v", coreAfter, err)
	}
	if data, err := os.ReadFile(other); err != nil || string(data) != "other group custom" {
		t.Fatalf("group operation changed sibling: %q, %v", data, err)
	}
	inventory, err := ReadPackageInventory(f.cfg.ProjectRoot, "team/exact")
	if err != nil {
		t.Fatal(err)
	}
	disabled, err := CheckPackageMountTargets(f.cfg, "team/exact", inventory)
	if err != nil || !disabled["triage"] {
		t.Fatalf("adopted group is not mount-ready with its marker: %v, %v", disabled, err)
	}
}

func TestPackageAdoptionApplyCanonicalSelections(t *testing.T) {
	f := newAdoptionApplyFixture(t)
	addAdoptionApplyOptional(t, &f, "alpha")
	if err := prepareAdoptionReceipts(f.cfg, f.database, f.batch); err != nil {
		t.Fatal(err)
	}
	if err := applyPackageAdoption(f.cfg, f.database, f.batch); err != nil {
		t.Fatal(err)
	}
	if err := activatePackages(f.cfg.ProjectRoot, "", f.database, f.batch.scopes[0].selected.System,
		adoptionFinalReceipts(t, f), &f.batch); err != nil {
		t.Fatal(err)
	}
	if got := selectedSnapshots(t, f.cfg.ProjectRoot, ""); len(got) != 2 || got[0].Name != "alpha" {
		t.Fatalf("fixture did not change canonical order: %+v", got)
	}
	if err := prepareAdoptionReceipts(f.cfg, f.database, f.batch); err != nil {
		t.Fatalf("equivalent reordered selection refused: %v", err)
	}
}

func TestPackageAdoptionApplyChangedReceiptBeforePreparation(t *testing.T) {
	for _, scenario := range []string{"preview", "completed"} {
		t.Run(scenario, func(t *testing.T) {
			f := newAdoptionApplyFixture(t)
			if scenario == "completed" {
				if err := prepareAdoptionReceipts(f.cfg, f.database, f.batch); err != nil {
					t.Fatal(err)
				}
				if err := applyPackageAdoption(f.cfg, f.database, f.batch); err != nil {
					t.Fatal(err)
				}
				if err := activatePackages(f.cfg.ProjectRoot, "", f.database, f.selected.System,
					adoptionFinalReceipts(t, f), &f.batch); err != nil {
					t.Fatal(err)
				}
			}
			home := filepath.Join(f.cfg.GroupsDir, "team/exact")
			writeSnapshotFile(t, home, "PERSONA.md", "new operator persona")
			row, _, err := f.database.InstalledPackage("", "core")
			if err != nil {
				t.Fatal(err)
			}
			row.AssetHashes["file:PERSONA.md"] = hashBytes([]byte("new operator persona"))
			if err := f.database.PutInstalledPackage(row); err != nil {
				t.Fatal(err)
			}
			before := adoptionDisk(t, f.cfg.ProjectRoot)
			if err := prepareAdoptionReceipts(f.cfg, f.database, f.batch); err == nil {
				got, _, readErr := f.database.InstalledPackage("", "core")
				t.Fatalf("changed receipt reached preparation: pending=%v, read=%v", HasPendingAdoption(got.AssetHashes), readErr)
			}
			got, _, err := f.database.InstalledPackage("", "core")
			if err != nil || !reflect.DeepEqual(row, got) || !reflect.DeepEqual(before, adoptionDisk(t, f.cfg.ProjectRoot)) {
				t.Fatalf("changed preimage refusal mutated files or receipt: %+v, %v", got, err)
			}
		})
	}
}

func newUncopiedAdoptionFixture(t *testing.T) adoptionApplyFixture {
	t.Helper()
	f := newAdoptionApplyFixture(t)
	addAdoptionApplyOptional(t, &f, "alpha")
	if err := os.Remove(filepath.Join(f.cfg.GroupsDir, "team/exact/.claude/skills/triage/SKILL.md")); err != nil {
		t.Fatal(err)
	}
	f.batch.bindings[1].revision = strings.Repeat("d", 40)
	var planErr error
	f.batch.scopes[0].plan, planErr = PlanPackageAdoption(f.cfg, "team/exact", f.batch.scopes[0].selected,
		f.batch.scopes[0].receipts, nil)
	if planErr != nil || len(f.batch.scopes[0].plan.Conflicts) != 0 {
		t.Fatalf("uncopied candidate fixture failed: %+v, %v", f.batch.scopes[0].plan, planErr)
	}
	return f
}

func TestPackageAdoptionApplyPartialActivationWithUncopiedPackage(t *testing.T) {
	f := newUncopiedAdoptionFixture(t)
	f.batch.bindings = f.batch.bindings[:1]
	before := adoptionDisk(t, f.cfg.ProjectRoot)
	rows, err := f.database.InstalledPackages()
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []adoptionPhase{adoptionPrepare, adoptionApply, adoptionActivate} {
		switch phase {
		case adoptionPrepare:
			err = prepareAdoptionReceipts(f.cfg, f.database, f.batch)
		case adoptionApply:
			err = applyPackageAdoption(f.cfg, f.database, f.batch)
		case adoptionActivate:
			err = activatePackages(f.cfg.ProjectRoot, "", f.database, f.batch.scopes[0].selected.System,
				[]store.InstalledPackage{rows[0], {Source: "alpha-origin", Revision: strings.Repeat("d", 40)}}, &f.batch)
		}
		if err == nil {
			t.Fatalf("phase %d accepted the unbound activation candidate", phase)
		}
		after, readErr := f.database.InstalledPackages()
		if readErr != nil || !reflect.DeepEqual(rows, after) || !reflect.DeepEqual(before, adoptionDisk(t, f.cfg.ProjectRoot)) {
			t.Fatalf("phase %d changed state before refusing the incomplete witness: %v", phase, readErr)
		}
	}
}

func TestPackageAdoptionApplyUncopiedReceiptReplay(t *testing.T) {
	for _, scenario := range []string{"replay", "source", "revision", "digest", "extra-row", "binding-source", "binding-revision", "binding-digest", "extra-binding"} {
		t.Run(scenario, func(t *testing.T) {
			f := newUncopiedAdoptionFixture(t)
			if err := prepareAdoptionReceipts(f.cfg, f.database, f.batch); err != nil {
				t.Fatal(err)
			}
			if _, found, err := f.database.InstalledPackage("", "alpha"); err != nil || found {
				t.Fatalf("zero-copy candidate acquired a preparatory row: %v, %v", found, err)
			}
			if err := applyPackageAdoption(f.cfg, f.database, f.batch); err != nil {
				t.Fatal(err)
			}
			alpha := f.batch.scopes[0].selected.System[1]
			coreRow, _, err := f.database.InstalledPackage("", "core")
			if err != nil {
				t.Fatal(err)
			}
			selected := []PackageSnapshot{alpha, f.binding.snapshot}
			receipts := []store.InstalledPackage{{Source: "alpha-origin", Revision: strings.Repeat("d", 40)}, coreRow}
			injected := errors.New("injected second final receipt failure")
			failure := &packageReceiptFailure{Store: f.database.Obj(), failure: injected, allow: 1}
			if err := activatePackages(f.cfg.ProjectRoot, "", store.NewOver(failure), selected, receipts, &f.batch); !errors.Is(err, injected) {
				t.Fatalf("did not reach partial final receipt failure: %v", err)
			}
			row, found, err := f.database.InstalledPackage("", "alpha")
			if err != nil || !found || HasPendingAdoption(row.AssetHashes) || row.Source != "alpha-origin" ||
				row.Revision != strings.Repeat("d", 40) || row.AssetHashes[PackageSourceArtifact] != alpha.Digest {
				t.Fatalf("first final receipt did not retain its binding: %+v, %v, %v", row, found, err)
			}
			switch scenario {
			case "source":
				row.Source = "changed-origin"
			case "revision":
				row.Revision = strings.Repeat("e", 40)
			case "digest":
				row.AssetHashes[PackageSourceArtifact] = f.binding.snapshot.Digest
				row.Manifest[PackageSourceArtifact] = []string{f.binding.snapshot.Digest}
			case "extra-row":
				row = store.InstalledPackage{Name: "unrelated", Source: "other-origin", Revision: strings.Repeat("e", 40)}
			case "binding-source":
				f.batch.bindings[1].source = "changed-origin"
			case "binding-revision":
				f.batch.bindings[1].revision = strings.Repeat("e", 40)
			case "binding-digest":
				f.batch.bindings[1].snapshot.Digest = f.binding.snapshot.Digest
			case "extra-binding":
				snapshot := prepareSnapshot(t, f.cfg.ProjectRoot, snapshotSource(t, "unrelated", "unrelated"))
				f.batch.bindings = append(f.batch.bindings, adoptionBinding{snapshot: snapshot,
					source: "other-origin", revision: strings.Repeat("e", 40)})
			}
			if scenario == "source" || scenario == "revision" || scenario == "digest" || scenario == "extra-row" {
				if err := f.database.PutInstalledPackage(row); err != nil {
					t.Fatal(err)
				}
			}
			before := adoptionDisk(t, f.cfg.ProjectRoot)
			previous, err := f.database.InstalledPackages()
			if err != nil {
				t.Fatal(err)
			}
			for _, phase := range []adoptionPhase{adoptionPrepare, adoptionApply, adoptionActivate,
				adoptionPrepare, adoptionApply, adoptionActivate} {
				switch phase {
				case adoptionPrepare:
					err = prepareAdoptionReceipts(f.cfg, f.database, f.batch)
				case adoptionApply:
					err = applyPackageAdoption(f.cfg, f.database, f.batch)
				case adoptionActivate:
					err = activatePackages(f.cfg.ProjectRoot, "", f.database, selected, receipts, &f.batch)
				}
				if scenario == "replay" {
					if err != nil {
						t.Fatalf("phase %d cannot resume its complete selection: %v", phase, err)
					}
				} else {
					after, readErr := f.database.InstalledPackages()
					if err == nil || readErr != nil || !reflect.DeepEqual(previous, after) ||
						!reflect.DeepEqual(before, adoptionDisk(t, f.cfg.ProjectRoot)) {
						t.Fatalf("phase %d accepted or changed the mismatched receipt: %v, %v", phase, err, readErr)
					}
				}
			}
			if scenario == "replay" {
				got, _, err := f.database.InstalledPackage("", "alpha")
				core, _, coreErr := f.database.InstalledPackage("", "core")
				if err != nil || coreErr != nil || !reflect.DeepEqual(row, got) || HasPendingAdoption(core.AssetHashes) ||
					!HasPendingSettings(core.AssetHashes) || core.AssetHashes["file:PERSONA.md"] != hashBytes([]byte("operator persona")) ||
					!reflect.DeepEqual(before, adoptionDisk(t, f.cfg.ProjectRoot)) {
					t.Fatalf("replay changed completed receipt, seed, settings or payloads: %+v, %v, %v", got, err, coreErr)
				}
			}
		})
	}
}

func TestPackageAdoptionApplyUncopiedReceiptLateFailure(t *testing.T) {
	for _, scenario := range []struct {
		name  string
		allow int
	}{{"before-first-receipt", 0}, {"after-core-receipt", 1}, {"unprepared-selection", 0}} {
		t.Run(scenario.name, func(t *testing.T) {
			f := newUncopiedAdoptionFixture(t)
			if scenario.name == "unprepared-selection" {
				if err := SelectPackages(f.cfg.ProjectRoot, "", f.batch.scopes[0].selected.System); err != nil {
					t.Fatal(err)
				}
				before := adoptionDisk(t, f.cfg.ProjectRoot)
				previous, err := f.database.InstalledPackages()
				if err != nil {
					t.Fatal(err)
				}
				if err := prepareAdoptionReceipts(f.cfg, f.database, f.batch); err == nil {
					t.Fatal("selection change without adoption preparation accepted")
				}
				after, err := f.database.InstalledPackages()
				if err != nil || !reflect.DeepEqual(previous, after) || !reflect.DeepEqual(before, adoptionDisk(t, f.cfg.ProjectRoot)) {
					t.Fatalf("unprepared selection refusal changed state: %v", err)
				}
				return
			}
			if err := prepareAdoptionReceipts(f.cfg, f.database, f.batch); err != nil {
				t.Fatal(err)
			}
			if err := applyPackageAdoption(f.cfg, f.database, f.batch); err != nil {
				t.Fatal(err)
			}
			coreRow, _, err := f.database.InstalledPackage("", "core")
			if err != nil {
				t.Fatal(err)
			}
			selected := f.batch.scopes[0].selected.System
			receipts := []store.InstalledPackage{coreRow, {Source: "alpha-origin", Revision: strings.Repeat("d", 40)}}
			injected := errors.New("injected final receipt failure before uncopied candidate")
			failure := &packageReceiptFailure{Store: f.database.Obj(), failure: injected, allow: scenario.allow}
			if err := activatePackages(f.cfg.ProjectRoot, "", store.NewOver(failure), selected, receipts, &f.batch); !errors.Is(err, injected) {
				t.Fatalf("did not reach final receipt failure: %v", err)
			}
			before := adoptionDisk(t, f.cfg.ProjectRoot)
			if err := prepareAdoptionReceipts(f.cfg, f.database, f.batch); err != nil {
				t.Fatalf("preparation cannot resume before uncopied receipt: %v", err)
			}
			if err := applyPackageAdoption(f.cfg, f.database, f.batch); err != nil {
				t.Fatalf("apply cannot resume before uncopied receipt: %v", err)
			}
			if err := activatePackages(f.cfg.ProjectRoot, "", f.database, selected, receipts, &f.batch); err != nil {
				t.Fatalf("activation cannot resume before uncopied receipt: %v", err)
			}
			alpha, found, err := f.database.InstalledPackage("", "alpha")
			core, _, coreErr := f.database.InstalledPackage("", "core")
			if err != nil || !found || coreErr != nil || alpha.Source != "alpha-origin" ||
				alpha.Revision != strings.Repeat("d", 40) || HasPendingAdoption(alpha.AssetHashes) ||
				HasPendingAdoption(core.AssetHashes) || !HasPendingSettings(core.AssetHashes) ||
				!reflect.DeepEqual(before, adoptionDisk(t, f.cfg.ProjectRoot)) {
				t.Fatalf("late receipt replay lost state: alpha=%+v core=%+v errors=%v,%v", alpha, core, err, coreErr)
			}
		})
	}
}

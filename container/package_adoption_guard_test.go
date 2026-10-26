package container

import (
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestPendingPackageAdoptionScopes(t *testing.T) {
	for _, scenario := range []string{"all", "exact", "parent", "child", "other", "empty", "system", "instance", "settings"} {
		t.Run(scenario, func(t *testing.T) {
			f := newPendingAdoptionFixture(t, "receipt")
			row := f.receipts[0]
			row.AssetHashes = maps.Clone(f.hashes)
			folders := []string{"team/exact"}
			want := true
			switch scenario {
			case "all":
				folders = nil
			case "parent":
				folders, want = []string{"team"}, false
			case "child":
				folders, want = []string{"team/exact/child"}, false
			case "other":
				folders, want = []string{"other"}, false
			case "empty":
				folders, want = []string{}, false
			case "system":
				row.Folder = ""
				folders = []string{"other"}
			case "instance":
				row.AssetHashes["pending:adopt:tree:skills/triage"] = strings.Repeat("a", 64)
				folders = []string{"other"}
			case "settings":
				row.AssetHashes = map[string]string{PendingSettingPrefix + "KEEP": strings.Repeat("b", 64)}
				want = false
			}
			database := corePackageDatabase(t)
			if err := database.PutInstalledPackage(row); err != nil {
				t.Fatal(err)
			}
			before, err := database.InstalledPackages()
			if err != nil {
				t.Fatal(err)
			}
			if err := CheckPendingPackageAdoption(database, folders); (err != nil) != want {
				t.Fatalf("refusal = %v, want %v", err, want)
			}
			after, err := database.InstalledPackages()
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("guard changed receipts: %v", err)
			}
		})
	}
}

func TestPendingPackageAdoptionMalformed(t *testing.T) {
	for _, scenario := range []string{"unknown", "hash", "source", "artifact", "tree", "root", "path", "kind"} {
		t.Run(scenario, func(t *testing.T) {
			f := newPendingAdoptionFixture(t, "receipt")
			row := f.receipts[0]
			row.AssetHashes = maps.Clone(f.hashes)
			switch scenario {
			case "unknown":
				row.AssetHashes["pending:adopt:unknown"] = strings.Repeat("a", 64)
			case "hash":
				row.AssetHashes["pending:adopt:source"] = "wrong"
			case "source", "artifact":
				key := "source"
				if scenario == "artifact" {
					key = "package_source"
				}
				delete(row.AssetHashes, "pending:adopt:"+key)
			case "tree":
				maps.DeleteFunc(row.AssetHashes, func(key, _ string) bool { return strings.HasPrefix(key, "pending:adopt:tree:") })
			case "root":
				row.AssetHashes["pending:adopt:tree:outside/triage"] = strings.Repeat("a", 64)
			case "path":
				row.AssetHashes["pending:adopt:file:groups/team/../outside"] = strings.Repeat("a", 64)
			case "kind":
				row.AssetHashes["pending:adopt:link:groups/team/exact/skills/triage"] = strings.Repeat("a", 64)
			}
			database := corePackageDatabase(t)
			if err := database.PutInstalledPackage(row); err != nil {
				t.Fatal(err)
			}
			if err := CheckPendingPackageAdoption(database, []string{"other"}); err == nil {
				t.Fatal("malformed ledger allowed an unrelated mutation")
			}
		})
	}
}

func TestCorePackagePendingAdoptionStartup(t *testing.T) {
	for _, scenario := range []string{"core", "optional", "malformed"} {
		t.Run(scenario, func(t *testing.T) {
			f := newAdoptionApplyFixture(t)
			if err := prepareAdoptionReceipts(f.cfg, f.database, f.batch); err != nil {
				t.Fatal(err)
			}
			if scenario != "core" {
				row, _, err := f.database.InstalledPackage("", "core")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.database.DeleteInstalledPackage("", "core"); err != nil {
					t.Fatal(err)
				}
				row.Name, row.Folder = "optional", "other/nested"
				if scenario == "malformed" {
					row.AssetHashes = map[string]string{"pending:adopt:invalid": "invalid"}
				}
				if err := f.database.PutInstalledPackage(row); err != nil {
					t.Fatal(err)
				}
			}
			writeSnapshotFile(t, f.cfg.AppSrcDir, "ant/CLAUDE.md", "next release")
			before := adoptionDisk(t, f.cfg.ProjectRoot)
			rows, err := f.database.InstalledPackages()
			if err != nil {
				t.Fatal(err)
			}
			if err := EnsureCorePackage(f.cfg, f.database); err == nil || !strings.Contains(err.Error(), "pending package adoption") {
				t.Fatalf("startup bypassed adoption: %v", err)
			}
			after, err := f.database.InstalledPackages()
			if err != nil || !reflect.DeepEqual(rows, after) || !reflect.DeepEqual(before, adoptionDisk(t, f.cfg.ProjectRoot)) {
				t.Fatalf("refused startup changed files, selections or receipts: %v", err)
			}
		})
	}
}

func TestPrepareCorePackagesSeparatesActivation(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "upgrade"}[existing], func(t *testing.T) {
			cfg := corePackageFixture(t)
			database := corePackageDatabase(t)
			if existing {
				if err := EnsureCorePackage(cfg, database); err != nil {
					t.Fatal(err)
				}
				writeSnapshotFile(t, cfg.AppSrcDir, "ant/CLAUDE.md", "next release")
			}
			selected := selectedSnapshots(t, cfg.ProjectRoot, "")
			rows, err := database.InstalledPackages()
			if err != nil {
				t.Fatal(err)
			}
			snapshots, receipts, err := PrepareCorePackages(cfg, database)
			if err != nil || len(snapshots) != 1 || snapshots[0].Name != "core" || len(receipts) != 1 || receipts[0].Revision != snapshots[0].Digest {
				t.Fatalf("core candidate = %+v, %+v, %v", snapshots, receipts, err)
			}
			if slices.Equal(selected, snapshots) {
				t.Fatal("fixture did not prepare a changed selection")
			}
			if _, err := os.Stat(filepath.Join(cfg.ProjectRoot, "packages", "core", snapshots[0].Digest, "CLAUDE.md")); err != nil {
				t.Fatal(err)
			}
			after, err := database.InstalledPackages()
			if err != nil || !reflect.DeepEqual(rows, after) || !slices.Equal(selected, selectedSnapshots(t, cfg.ProjectRoot, "")) {
				t.Fatalf("preparation selected or repaired receipts: %v", err)
			}
			if err := ActivatePackages(cfg.ProjectRoot, "", database, snapshots, receipts); err != nil {
				t.Fatal(err)
			}
			row, found, err := database.InstalledPackage("", "core")
			if err != nil || !found || row.Revision != snapshots[0].Digest || !slices.Equal(snapshots, selectedSnapshots(t, cfg.ProjectRoot, "")) {
				t.Fatalf("explicit activation failed: %+v, %v", row, err)
			}
		})
	}
}

func TestGroupWithdrawalPendingAdoption(t *testing.T) {
	f := newAdoptionApplyFixture(t)
	if err := prepareAdoptionReceipts(f.cfg, f.database, f.batch); err != nil {
		t.Fatal(err)
	}
	snapshot := prepareSnapshot(t, f.cfg.ProjectRoot, snapshotSource(t, "optional", "optional"))
	if err := SelectPackages(f.cfg.ProjectRoot, "team/exact", []PackageSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	before := adoptionDisk(t, f.cfg.ProjectRoot)
	if err := WithdrawGroupPackages(f.cfg.ProjectRoot, f.database, []string{"team/exact"}); err == nil {
		t.Fatal("withdrawal bypassed pending system core adoption")
	}
	if !reflect.DeepEqual(before, adoptionDisk(t, f.cfg.ProjectRoot)) {
		t.Fatal("refused withdrawal changed the selection")
	}
}

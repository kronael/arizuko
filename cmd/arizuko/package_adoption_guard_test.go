package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/store"
)

func pendingAdoptionReceipt(folder string) store.InstalledPackage {
	return store.InstalledPackage{Folder: folder, Name: "waiting", Source: "origin", Revision: "revision",
		Manifest: map[string][]string{}, AssetHashes: map[string]string{
			"pending:adopt:source": strings.Repeat("a", 64), "pending:adopt:package_source": strings.Repeat("b", 64),
			"pending:adopt:tree:groups/main/child/.claude/skills/probe": strings.Repeat("c", 64),
			container.PendingSettingPrefix + "KEEP":                     strings.Repeat("d", 64),
		}}
}

func TestPendingAdoptionPackageConsumers(t *testing.T) {
	for _, command := range []string{"install", "legacy-install", "upgrade", "legacy-upgrade", "remove", "legacy-remove", "sync", "group-install", "group-upgrade", "group-remove", "group-sync", "products"} {
		t.Run(command, func(t *testing.T) {
			dataDir, base := mixInstance(t, map[string]map[string]string{"one": {
				"arizuko.toml": "name='one'\n", "PERSONA.md": "seed",
				".claude/settings.json": `{"env":{"MODE":"old"}}`,
			}}, []string{"one"})
			cmdProducts([]string{"t", "apply", "main"})
			source := filepath.Join(base, "tools")
			mustWrite(t, filepath.Join(source, "arizuko.toml"), "name='tools'\n")
			mustWrite(t, filepath.Join(source, "tools.yml"), "services:\n  tools: {image: old}\n")
			cmdPackages([]string{"t", "install", source})
			legacy := filepath.Join(base, "legacy")
			mustWrite(t, filepath.Join(legacy, "legacy.yml"), "services:\n  legacy: {image: old}\n")
			cmdPackages([]string{"t", "install", legacy})
			mustWrite(t, filepath.Join(source, "tools.yml"), "services:\n  tools: {image: next}\n")
			mustWrite(t, filepath.Join(legacy, "legacy.yml"), "services:\n  legacy: {image: next}\n")
			mustWrite(t, filepath.Join(base, "src/one/.claude/settings.json"), `{"env":{"MODE":"next"}}`)
			db := instanceStore(t)
			row := pendingAdoptionReceipt("")
			row.Name = "core"
			if err := db.PutInstalledPackage(row); err != nil {
				t.Fatal(err)
			}
			before, mtimes := treeHashes(t, dataDir), treeMtimes(t, dataDir)
			receipts, err := db.InstalledPackages()
			if err != nil {
				t.Fatal(err)
			}
			system, err := container.ReadPackageSelection(dataDir, "")
			if err != nil {
				t.Fatal(err)
			}
			group, err := container.ReadPackageSelection(dataDir, "main")
			if err != nil {
				t.Fatal(err)
			}
			args := []string{"packages", "t", command}
			switch command {
			case "install":
				args = []string{"packages", "t", "install", source}
			case "legacy-install":
				args = []string{"packages", "t", "install", legacy}
			case "upgrade", "remove":
				args = append(args, "tools")
			case "legacy-upgrade", "legacy-remove":
				args = []string{"packages", "t", strings.TrimPrefix(command, "legacy-"), "legacy"}
			case "group-install":
				args = []string{"packages", "t", "install", source, "--group", "main"}
			case "group-upgrade", "group-remove":
				args = []string{"packages", "t", strings.TrimPrefix(command, "group-"), "one", "--group", "main"}
			case "group-sync":
				args = []string{"packages", "t", "sync", "--group", "main"}
			case "products":
				args = []string{"products", "t", "apply", "main"}
			}
			if out, err := runManifestCLI(t, args); err == nil || !strings.Contains(out, "pending package adoption") {
				t.Fatalf("consumer bypassed adoption: %v %s", err, out)
			}
			after, err := db.InstalledPackages()
			if err != nil || !reflect.DeepEqual(receipts, after) || !reflect.DeepEqual(before, treeHashes(t, dataDir)) || !reflect.DeepEqual(mtimes, treeMtimes(t, dataDir)) {
				t.Fatalf("refusal changed declarations, services, settings or receipts: %v", err)
			}
			assertGroupPackages(t, dataDir, "", system)
			assertGroupPackages(t, dataDir, "main", group)
		})
	}
}

func TestPendingAdoptionManagedConsumers(t *testing.T) {
	for _, operation := range []string{"install", "remove"} {
		t.Run(operation, func(t *testing.T) {
			dataDir, base := mixInstance(t, nil, nil)
			source := filepath.Join(base, "tools")
			mustWrite(t, filepath.Join(source, "arizuko.toml"), "name='tools'\n")
			mustWrite(t, filepath.Join(source, "tools.yml"), "services:\n  tools: {image: old}\n")
			cmdPackages([]string{"t", "install", source})
			mustWrite(t, filepath.Join(source, "tools.yml"), "services:\n  tools: {image: next}\n")
			lock := lockPackageOperation(dataDir)
			defer lock.Close()
			rdb := mustOpenRoutd()
			defer rdb.Close()
			candidate, err := preparePackageCandidate(dataDir, source, source, "local")
			if err != nil {
				t.Fatal(err)
			}
			if err := rdb.PutInstalledPackage(pendingAdoptionReceipt("other/child")); err != nil {
				t.Fatal(err)
			}
			before := treeHashes(t, dataDir)
			selected, err := container.ReadPackageSelection(dataDir, "")
			if err != nil {
				t.Fatal(err)
			}
			if operation == "install" {
				err = installManagedPackage(dataDir, rdb, candidate, nil)
			} else {
				err = removeManagedPackage(dataDir, "", "tools", rdb)
			}
			if err == nil || !strings.Contains(err.Error(), "pending package adoption") || !reflect.DeepEqual(before, treeHashes(t, dataDir)) {
				t.Fatalf("managed consumer bypassed adoption: %v", err)
			}
			assertGroupPackages(t, dataDir, "", selected)
		})
	}
}

func TestGroupAddPendingProductDeclaration(t *testing.T) {
	for _, pending := range []string{"core", "group", "malformed"} {
		t.Run(pending, func(t *testing.T) {
			dataDir, base := mixInstance(t, nil, nil)
			t.Setenv("HOST_APP_DIR", base)
			mustWrite(t, filepath.Join(base, "ant/examples/sample/arizuko.toml"), "name='sample'\n")
			mustWrite(t, filepath.Join(base, "ant/examples/sample/PERSONA.md"), "source persona")
			row := pendingAdoptionReceipt("")
			row.Name = "core"
			switch pending {
			case "group":
				row.Folder, row.Name = "other/child", "waiting"
			case "malformed":
				row.AssetHashes = map[string]string{"pending:adopt:unknown": "invalid"}
			}
			database := instanceStore(t)
			if err := database.PutInstalledPackage(row); err != nil {
				t.Fatal(err)
			}
			lock := lockPackageOperation(dataDir)
			if err := lock.Close(); err != nil {
				t.Fatal(err)
			}
			before, mtimes := treeHashes(t, dataDir), treeMtimes(t, dataDir)
			if out, err := runManifestCLI(t, []string{"group", "t", "add", "web:sample", "new", "--product", "sample"}); err == nil || !strings.Contains(out, "pending package adoption") {
				t.Errorf("group add bypassed adoption: %v, %s", err, out)
			}
			if !reflect.DeepEqual(before, treeHashes(t, dataDir)) || !reflect.DeepEqual(mtimes, treeMtimes(t, dataDir)) {
				t.Error("refused group add wrote the product declaration")
			}
			if _, found := database.GroupByFolder("new"); found {
				t.Error("refused group add registered the group")
			}
		})
	}
}

func TestPendingAdoptionRenameConsumers(t *testing.T) {
	for _, phase := range []string{"preflight", "initial", "resume", "rollback"} {
		for _, pending := range []string{"source", "destination", "system", "malformed", "instance"} {
			t.Run(phase+"/"+pending, func(t *testing.T) {
				root := t.TempDir()
				inventory, drivers := renameFixture(t)
				for _, folder := range []string{"main", "main/child", "keeper"} {
					renameSeed(t, &inventory, drivers, "routd", store.Groups,
						objstore.Key{Partition: "routd", Sort: folder}, map[string]string{})
				}
				seedGroupPackages(t, root, []string{"main", "main/child", "keeper"})
				mapping := renameTestMap(t)
				plan, err := renamePreflight(context.Background(), "fixture", root, mapping, inventory, drivers)
				if err != nil {
					t.Fatal(err)
				}
				journal := filepath.Join(t.TempDir(), "journal")
				var journalBefore map[string]string
				if phase == "resume" || phase == "rollback" {
					original := drivers["routd"]
					drivers["routd"] = &renameFaultStore{Store: original, Fail: 1}
					if err := renameExecute(context.Background(), journal, plan, drivers, false, false); err == nil {
						t.Fatal("fixture did not interrupt rename before its first row write")
					}
					drivers["routd"] = original
					journalBefore = treeHashes(t, journal)
				}
				selections := map[string][]container.PackageSnapshot{}
				for _, folder := range []string{"main", "main/child", "keeper", "sloth", "sloth/child"} {
					selected, err := container.ReadPackageSelection(root, folder)
					if err != nil {
						t.Fatal(err)
					}
					selections[folder] = selected
				}
				row := pendingAdoptionReceipt("main/child")
				switch pending {
				case "destination":
					row.Folder = "sloth/child"
				case "system":
					row.Folder, row.Name = "", "core"
				case "malformed":
					row.Folder, row.AssetHashes = "keeper", map[string]string{"pending:adopt:unknown": "invalid"}
				case "instance":
					row.Folder = "keeper"
					row.AssetHashes["pending:adopt:tree:skills/probe"] = strings.Repeat("e", 64)
				}
				database := store.NewOver(drivers["routd"])
				if err := database.PutInstalledPackage(row); err != nil {
					t.Fatal(err)
				}
				before, err := database.InstalledPackages()
				if err != nil {
					t.Fatal(err)
				}
				if phase == "preflight" {
					_, err = renamePreflight(context.Background(), "fixture", root, mapping, inventory, drivers)
				} else {
					err = renameExecute(context.Background(), journal, plan, drivers, phase == "resume", phase == "rollback")
				}
				if err == nil || !strings.Contains(err.Error(), "pending package adoption") {
					t.Fatalf("rename bypassed adoption: %v", err)
				}
				if journalBefore == nil {
					if _, err := os.Stat(journal); !os.IsNotExist(err) {
						t.Fatalf("refusal wrote the journal: %v", err)
					}
				} else if !reflect.DeepEqual(journalBefore, treeHashes(t, journal)) {
					t.Fatal("refusal changed the interrupted journal")
				}
				after, err := database.InstalledPackages()
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatalf("refusal changed receipts: %v", err)
				}
				for _, folder := range []string{"main", "main/child", "keeper"} {
					if _, found := database.GroupByFolder(folder); !found {
						t.Fatalf("refusal moved %s", folder)
					}
				}
				for folder, selected := range selections {
					assertGroupPackages(t, root, folder, selected)
				}
			})
		}
	}
}

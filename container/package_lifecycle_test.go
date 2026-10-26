package container

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/objstore/dynstore/dyntest"
	"github.com/kronael/arizuko/store"
)

func TestMain(m *testing.M) { dyntest.Main(m) }

// Re-selecting an unchanged package preserves its complete existing receipt.
func TestPackageActivationPreservesMatchingReceipt(t *testing.T) {
	t.Run("skills", func(t *testing.T) {
		dataDir := t.TempDir()
		lockSnapshotPackages(t, dataDir)
		snapshot := prepareSnapshot(t, dataDir, snapshotSource(t, "tools", "v1"))
		database, err := store.OpenMem()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = database.Close() })
		receipt := store.InstalledPackage{
			Name: "tools", Source: "optional-source", Revision: "optional-revision",
			InstalledAt: "2020-01-01T00:00:00Z",
			Manifest: map[string][]string{
				"skill": {"triage"}, "package_source": {snapshot.Digest},
				"file": {"tools.yml"},
			},
			AssetHashes: map[string]string{
				"package_source": snapshot.Digest, "file:tools.yml": "owned-hash",
			},
		}
		if err := database.PutInstalledPackage(receipt); err != nil {
			t.Fatal(err)
		}
		if err := ActivatePackages(dataDir, "", database, []PackageSnapshot{snapshot},
			[]store.InstalledPackage{receipt}); err != nil {
			t.Fatal(err)
		}
		got, found, err := database.InstalledPackage("", "tools")
		if err != nil || !found || !reflect.DeepEqual(got, receipt) {
			t.Fatalf("matching optional receipt changed: %+v, %v, %v", got, found, err)
		}
	})
	t.Run("empty-inventory", func(t *testing.T) {
		dataDir := t.TempDir()
		lockSnapshotPackages(t, dataDir)
		source := t.TempDir()
		writeSnapshotFile(t, source, ManifestFile, "name = \"empty\"\n")
		snapshot := prepareSnapshot(t, dataDir, source)
		database := corePackageDatabase(t)
		receipt := store.InstalledPackage{
			Name: "empty", Source: "optional-source", Revision: "optional-revision",
			InstalledAt: "2020-01-01T00:00:00Z",
			Manifest: map[string][]string{
				"skill": {}, "package_source": {snapshot.Digest},
			},
			AssetHashes: map[string]string{"package_source": snapshot.Digest},
		}
		if err := database.PutInstalledPackage(receipt); err != nil {
			t.Fatal(err)
		}
		if err := ActivatePackages(dataDir, "", database, []PackageSnapshot{snapshot},
			[]store.InstalledPackage{receipt}); err != nil {
			t.Fatal(err)
		}
		got, found, err := database.InstalledPackage("", "empty")
		if err != nil || !found || !reflect.DeepEqual(got, receipt) {
			t.Fatalf("matching empty inventory receipt changed: %+v, %v, %v", got, found, err)
		}
	})
}

type packageReceiptFailure struct {
	objstore.Store
	failure error
	allow   int
}

func (database *packageReceiptFailure) GuardedBatch(ctx context.Context,
	checks []objstore.Check, writes []objstore.Write) (bool, error) {
	for _, write := range writes {
		if write.Entity == store.InstalledPackages && database.failure != nil {
			if database.allow > 0 {
				database.allow--
				continue
			}
			return false, database.failure
		}
	}
	return database.Store.GuardedBatch(ctx, checks, writes)
}

// A failed receipt leaves active bytes visible; retry repairs the receipt without replacing the active link.
func TestPackageActivationReceiptRepair(t *testing.T) {
	for _, target := range []string{"", "main"} {
		t.Run("target="+target, func(t *testing.T) {
			dataDir := t.TempDir()
			lockSnapshotPackages(t, dataDir)
			snapshot := prepareSnapshot(t, dataDir, snapshotSource(t, "tools", "v1"))
			database, err := store.OpenMem()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := database.Close(); err != nil {
					t.Error(err)
				}
			})
			injected := errors.New("injected receipt failure")
			failure := &packageReceiptFailure{Store: database.Obj(), failure: injected}
			borrowed := store.NewOver(failure)
			receipts := []store.InstalledPackage{{
				Source: "configured-source", Revision: "configured-revision",
				Manifest:    map[string][]string{"file": {"PERSONA.md"}},
				AssetHashes: map[string]string{"file:PERSONA.md": "operator-seed-hash"},
			}}
			err = ActivatePackages(dataDir, target, borrowed, []PackageSnapshot{snapshot}, receipts)
			if !errors.Is(err, injected) || !strings.Contains(err.Error(),
				"incomplete package operation: tools is active but its receipt failed; retry the same operation") {
				t.Fatalf("receipt failure hidden: %v", err)
			}
			if got := selectedSnapshots(t, dataDir, target); !reflect.DeepEqual(got, []PackageSnapshot{snapshot}) {
				t.Fatalf("failed receipt lost selection: %+v", got)
			}
			if _, found, err := database.InstalledPackage(target, "tools"); err != nil || found {
				t.Fatalf("failed write produced a receipt: %v, %v", found, err)
			}
			key, err := packageTarget(target)
			if err != nil {
				t.Fatal(err)
			}
			active := filepath.Join(dataDir, "package-active", key)
			before, err := os.Lstat(active)
			if err != nil {
				t.Fatal(err)
			}
			failure.failure = nil
			if err := ActivatePackages(dataDir, target, borrowed, []PackageSnapshot{snapshot}, receipts); err != nil {
				t.Fatal(err)
			}
			after, err := os.Lstat(active)
			if err != nil || !os.SameFile(before, after) {
				t.Fatalf("repair replaced active link: %v", err)
			}
			got, found, err := database.InstalledPackage(target, "tools")
			if err != nil || !found || got.Source != "configured-source" || got.Revision != "configured-revision" ||
				!reflect.DeepEqual(got.Manifest, map[string][]string{
					"file": {"PERSONA.md"}, "skill": {"triage"}, "package_source": {snapshot.Digest},
				}) || !reflect.DeepEqual(got.AssetHashes, map[string]string{
				"file:PERSONA.md": "operator-seed-hash", "package_source": snapshot.Digest,
			}) {
				t.Fatalf("repair lost receipt provenance: %+v, %v, %v", got, found, err)
			}
			if _, err := time.Parse(time.RFC3339, got.InstalledAt); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Pre-store activation selects filesystem state; completion writes the receipt and preserves the active link.
func TestPackageActivationBeforeStore(t *testing.T) {
	dataDir := t.TempDir()
	lockSnapshotPackages(t, dataDir)
	snapshot := prepareSnapshot(t, dataDir, snapshotSource(t, "tools", "v1"))
	if err := ActivatePackages(dataDir, "", nil, []PackageSnapshot{snapshot}, nil); err != nil {
		t.Fatal(err)
	}
	if got := selectedSnapshots(t, dataDir, ""); !reflect.DeepEqual(got, []PackageSnapshot{snapshot}) {
		t.Fatalf("pre-store activation missing: %+v", got)
	}
	active := filepath.Join(dataDir, "package-active", "system")
	before, err := os.Lstat(active)
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	})
	if records, err := database.InstalledPackages(); err != nil || len(records) != 0 {
		t.Fatalf("pre-store activation invented receipts: %+v, %v", records, err)
	}
	if err := ActivatePackages(dataDir, "", database, []PackageSnapshot{snapshot},
		[]store.InstalledPackage{{Source: "configured-source", Revision: snapshot.Digest}}); err != nil {
		t.Fatal(err)
	}
	if got, found, err := database.InstalledPackage("", "tools"); err != nil || !found ||
		got.Source != "configured-source" || got.AssetHashes["package_source"] != snapshot.Digest ||
		!reflect.DeepEqual(got.Manifest["skill"], []string{"triage"}) {
		t.Fatalf("completion lost receipt: %+v, %v, %v", got, found, err)
	}
	after, err := os.Lstat(active)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("completion replaced active link: %v", err)
	}
	if err := ActivatePackages(dataDir, "", database, nil, []store.InstalledPackage{{Name: "extra"}}); err == nil {
		t.Fatal("mismatched receipts accepted")
	}
	missing := []PackageSnapshot{{Name: "missing", Digest: snapshot.Digest}}
	if err := ActivatePackages(dataDir, "", nil, missing, nil); err == nil {
		t.Fatal("missing snapshot accepted")
	}
	if got := selectedSnapshots(t, dataDir, ""); !reflect.DeepEqual(got, []PackageSnapshot{snapshot}) {
		t.Fatalf("failed activation changed selection: %+v", got)
	}
}

func TestPackageActivationPendingAdoptionRefuses(t *testing.T) {
	dataDir := t.TempDir()
	lockSnapshotPackages(t, dataDir)
	snapshot := prepareSnapshot(t, dataDir, snapshotSource(t, "tools", "v1"))
	database := corePackageDatabase(t)
	receipt := store.InstalledPackage{Name: "other", Source: "origin", Revision: "revision",
		Manifest:    map[string][]string{},
		AssetHashes: map[string]string{"pending:adopt:unknown": "malformed", PendingSettingsArtifact: "settings"}}
	if err := database.PutInstalledPackage(receipt); err != nil {
		t.Fatal(err)
	}
	before := adoptionDisk(t, dataDir)
	if err := ActivatePackages(dataDir, "", database, []PackageSnapshot{snapshot},
		[]store.InstalledPackage{{Source: "origin", Revision: "revision"}}); err == nil {
		t.Fatal("activation without a witness accepted pending adoption")
	}
	got, _, err := database.InstalledPackage("", "other")
	if err != nil || !reflect.DeepEqual(got, receipt) || !reflect.DeepEqual(before, adoptionDisk(t, dataDir)) {
		t.Fatalf("refused activation changed selection or pending receipt: %+v, %v", got, err)
	}
}

package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/kronael/arizuko/objstore"
)

// Nil documents store as objects, empty optional fields stay absent, and CRUD preserves folder coordinates.
func TestInstalledPackageStoreDocuments(t *testing.T) {
	database := openEntities(t)
	for _, folder := range []string{"", "main"} {
		if err := database.PutInstalledPackage(InstalledPackage{Folder: folder, Name: "tools"}); err != nil {
			t.Fatal(err)
		}
	}
	items, err := database.Obj().Query(context.Background(), objstore.Query{
		Entity: InstalledPackages, Partition: OwnerRoutd,
	})
	if err != nil || len(items) != 2 {
		t.Fatalf("raw records: %+v, %v", items, err)
	}
	for _, item := range items {
		if !reflect.DeepEqual(item.Attrs, map[string]string{"manifest": "{}", "asset_hashes": "{}"}) {
			t.Fatalf("nil document or absent field encoding changed: %+v", item)
		}
	}
	receipt := InstalledPackage{
		Folder: "main", Name: "tools", Source: "configured-source", Revision: "revision",
		InstalledAt: "2026-10-26T00:00:00Z", Manifest: map[string][]string{"skill": {"triage"}},
		AssetHashes: map[string]string{"skill:triage": "hash"},
	}
	if err := database.PutInstalledPackage(receipt); err != nil {
		t.Fatal(err)
	}
	got, found, err := database.InstalledPackage("main", "tools")
	if err != nil || !found || !reflect.DeepEqual(got, receipt) {
		t.Fatalf("receipt roundtrip: %+v, %v, %v", got, found, err)
	}
	if rows, err := database.InstalledPackages(); err != nil || len(rows) != 2 ||
		rows[0].Folder != "" || !reflect.DeepEqual(rows[1], receipt) {
		t.Fatalf("folder ordered listing: %+v, %v", rows, err)
	}
	if deleted, err := database.DeleteInstalledPackage("main", "tools"); err != nil || !deleted {
		t.Fatalf("delete: %v, %v", deleted, err)
	}
	if deleted, err := database.DeleteInstalledPackage("main", "tools"); err != nil || deleted {
		t.Fatalf("absent delete: %v, %v", deleted, err)
	}
	if _, found, err := database.InstalledPackage("main", "tools"); err != nil || found {
		t.Fatalf("deleted receipt remains: %v, %v", found, err)
	}
	if _, found, err := database.InstalledPackage("", "tools"); err != nil || !found {
		t.Fatalf("folder delete removed system receipt: %v, %v", found, err)
	}
}

// Malformed stored documents fail through both readers; missing attributes retain the empty-document read behavior.
func TestInstalledPackageStoreReadErrors(t *testing.T) {
	for _, attribute := range []string{AttrPkgManifest, AttrPkgAssetHashes} {
		t.Run(attribute, func(t *testing.T) {
			database := openEntities(t)
			key, err := InstalledPackageKey("main", "tools")
			if err != nil {
				t.Fatal(err)
			}
			if err := database.WriteEntities(context.Background(), nil, objstore.Write{
				Op: objstore.Put, Entity: InstalledPackages, Key: key,
				Attrs: map[string]string{attribute: "{"},
			}); err != nil {
				t.Fatal(err)
			}
			_, _, getErr := database.InstalledPackage("main", "tools")
			_, listErr := database.InstalledPackages()
			label := "manifest"
			if attribute == AttrPkgAssetHashes {
				label = "asset hashes"
			}
			for _, err := range []error{getErr, listErr} {
				if err == nil || err.Error() != "installed package main/tools "+label+": unexpected end of JSON input" {
					t.Fatalf("malformed document error changed: %v", err)
				}
			}
			if err := database.WriteEntities(context.Background(), nil, objstore.Write{
				Op: objstore.Put, Entity: InstalledPackages, Key: key,
			}); err != nil {
				t.Fatal(err)
			}
			got, found, err := database.InstalledPackage("main", "tools")
			if err != nil || !found || got.Manifest != nil || got.AssetHashes != nil {
				t.Fatalf("absent attributes changed: %+v, %v, %v", got, found, err)
			}
		})
	}
}

type installedPackageFailure struct {
	objstore.Store
	failure error
}

func (database installedPackageFailure) Query(context.Context, objstore.Query) ([]objstore.Item, error) {
	return nil, database.failure
}

func (database installedPackageFailure) GuardedBatch(context.Context,
	[]objstore.Check, []objstore.Write) (bool, error) {
	return false, database.failure
}

// Invalid coordinates fail before storage, and every storage error reaches the caller unchanged.
func TestInstalledPackageStoreFailures(t *testing.T) {
	injected := errors.New("injected storage failure")
	base := openEntities(t)
	database := NewOver(installedPackageFailure{Store: base.Obj(), failure: injected})
	for _, err := range []error{
		database.PutInstalledPackage(InstalledPackage{Name: "tools"}),
		func() error { _, _, err := database.InstalledPackage("", "tools"); return err }(),
		func() error { _, err := database.InstalledPackages(); return err }(),
		func() error { _, err := database.DeleteInstalledPackage("", "tools"); return err }(),
	} {
		if !errors.Is(err, injected) {
			t.Fatalf("storage error lost: %v", err)
		}
	}
	for _, name := range []string{"", "bad" + KeySep + "name"} {
		for _, err := range []error{
			database.PutInstalledPackage(InstalledPackage{Name: name}),
			func() error { _, _, err := database.InstalledPackage("", name); return err }(),
			func() error { _, err := database.DeleteInstalledPackage("", name); return err }(),
		} {
			if err == nil || errors.Is(err, injected) || !strings.Contains(err.Error(), "installed package") {
				t.Fatalf("invalid coordinate reached storage: %v", err)
			}
		}
	}
}

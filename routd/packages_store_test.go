package routd

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/store"
)

// TestInstalledPackageCRUD exercises the installed-package record (see compose/README.md):
// absent → put → get roundtrip (incl. JSON manifest + hashes) → upsert/upgrade
// → list → delete. This is the lock P1+ install/upgrade/remove build on.
func TestInstalledPackageCRUD(t *testing.T) {
	d, err := OpenMem()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	if _, ok, err := d.InstalledPackage(InstanceWide, "slakd"); err != nil || ok {
		t.Fatalf("absent lookup: ok=%v err=%v", ok, err)
	}

	p := InstalledPackage{
		Name:        "slakd",
		Source:      "github.com/kronael/slakd",
		Revision:    "abc123",
		Manifest:    map[string][]string{"proxyd_route": {"/slack/"}, "skills": {"slakd"}},
		AssetHashes: map[string]string{"proxyd_route:/slack/": "h1", "skills:slakd": "h2"},
		InstalledAt: "2026-07-29T00:00:00Z",
	}
	if err := d.PutInstalledPackage(p); err != nil {
		t.Fatalf("put: %v", err)
	}

	got, ok, err := d.InstalledPackage(InstanceWide, "slakd")
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if got.Revision != "abc123" || got.Source != p.Source ||
		len(got.Manifest["proxyd_route"]) != 1 || got.Manifest["proxyd_route"][0] != "/slack/" ||
		got.AssetHashes["skills:slakd"] != "h2" {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}

	// upsert = upgrade: new revision authoritative, manifest replaced wholesale.
	p.Revision = "def456"
	p.Manifest = map[string][]string{"proxyd_route": {"/slack/"}}
	if err := d.PutInstalledPackage(p); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, _, _ = d.InstalledPackage(InstanceWide, "slakd")
	if got.Revision != "def456" || len(got.Manifest) != 1 {
		t.Fatalf("upgrade mismatch: %+v", got)
	}

	if ps, err := d.InstalledPackages(); err != nil || len(ps) != 1 || ps[0].Name != "slakd" {
		t.Fatalf("list: n=%d err=%v", len(ps), err)
	}

	if ok, err := d.DeleteInstalledPackage(InstanceWide, "slakd"); err != nil || !ok {
		t.Fatalf("delete: ok=%v err=%v", ok, err)
	}
	if _, ok, _ := d.InstalledPackage(InstanceWide, "slakd"); ok {
		t.Fatal("still present after delete")
	}
	if ok, _ := d.DeleteInstalledPackage(InstanceWide, "slakd"); ok {
		t.Fatal("second delete reported ok")
	}
}

// TestInstalledPackageFolderIsPartOfKey is why the folder LEADS the coordinate
// (BUGS F30): the package composition blends a product mix per GROUP, so two
// groups must be able to hold the SAME package name independently. A coordinate
// of the name alone is one lock for two subjects — the second Put addresses the
// first record. Falsified by dropping the folder from the key: the second Put
// overwrites, len(list) is 1 not 3, and the per-folder gets return each other's
// revision.
func TestInstalledPackageFolderIsPartOfKey(t *testing.T) {
	d, err := OpenMem()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	put := func(folder, rev string) {
		t.Helper()
		if err := d.PutInstalledPackage(InstalledPackage{
			Folder: folder, Name: "ttsd", Source: "github.com/kronael/ttsd",
			Revision: rev, InstalledAt: "2026-08-06T00:00:00Z",
		}); err != nil {
			t.Fatalf("put %q: %v", folder, err)
		}
	}
	put(InstanceWide, "rev-instance")
	put("acme", "rev-acme")
	put("acme/eng", "rev-acme-eng")

	ps, err := d.InstalledPackages()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(ps) != 3 {
		t.Fatalf("three folders each holding ttsd should be three rows, got %d: %+v", len(ps), ps)
	}
	// Instance-wide sorts first, then by folder — the order InstalledPackages promises.
	if ps[0].Folder != InstanceWide || ps[1].Folder != "acme" || ps[2].Folder != "acme/eng" {
		t.Fatalf("list order = %q/%q/%q, want ''/acme/acme%ceng", ps[0].Folder, ps[1].Folder, ps[2].Folder, '/')
	}

	for folder, want := range map[string]string{
		InstanceWide: "rev-instance", "acme": "rev-acme", "acme/eng": "rev-acme-eng",
	} {
		got, ok, err := d.InstalledPackage(folder, "ttsd")
		if err != nil || !ok {
			t.Fatalf("get %q: ok=%v err=%v", folder, ok, err)
		}
		if got.Revision != want {
			t.Errorf("get %q revision = %q, want %q — the folders share one row", folder, got.Revision, want)
		}
	}

	// Delete is keyed too: dropping one folder's row leaves the others intact.
	if ok, err := d.DeleteInstalledPackage("acme", "ttsd"); err != nil || !ok {
		t.Fatalf("delete acme: ok=%v err=%v", ok, err)
	}
	if _, ok, _ := d.InstalledPackage("acme", "ttsd"); ok {
		t.Error("acme row survived its own delete")
	}
	if _, ok, _ := d.InstalledPackage(InstanceWide, "ttsd"); !ok {
		t.Error("deleting acme's row also removed the instance-wide one")
	}
	if _, ok, _ := d.InstalledPackage("acme/eng", "ttsd"); !ok {
		t.Error("deleting acme's row also removed acme/eng's")
	}
}

// Nil documents store as objects, empty optional fields stay absent, and CRUD preserves folder coordinates.
func TestInstalledPackageStoreDocuments(t *testing.T) {
	database, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	for _, folder := range []string{"", "main"} {
		if err := database.PutInstalledPackage(InstalledPackage{Folder: folder, Name: "tools"}); err != nil {
			t.Fatal(err)
		}
	}
	items, err := database.Obj().Query(context.Background(), objstore.Query{
		Entity: store.InstalledPackages, Partition: store.OwnerRoutd,
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
		InstalledAt: "2026-10-26T00:00:00Z", Manifest: map[string][]string{"skill": {"triage"}, "empty": nil},
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
	for _, attribute := range []string{store.AttrPkgManifest, store.AttrPkgAssetHashes} {
		t.Run(attribute, func(t *testing.T) {
			database, err := OpenMem()
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			key, err := store.InstalledPackageKey("main", "tools")
			if err != nil {
				t.Fatal(err)
			}
			if err := database.store().WriteEntities(context.Background(), nil, objstore.Write{
				Op: objstore.Put, Entity: store.InstalledPackages, Key: key,
				Attrs: map[string]string{attribute: "{"},
			}); err != nil {
				t.Fatal(err)
			}
			_, _, getErr := database.InstalledPackage("main", "tools")
			_, listErr := database.InstalledPackages()
			label := "manifest"
			if attribute == store.AttrPkgAssetHashes {
				label = "asset hashes"
			}
			for _, err := range []error{getErr, listErr} {
				if err == nil || err.Error() != "installed package main/tools "+label+": unexpected end of JSON input" {
					t.Fatalf("malformed document error changed: %v", err)
				}
			}
			if err := database.store().WriteEntities(context.Background(), nil, objstore.Write{
				Op: objstore.Put, Entity: store.InstalledPackages, Key: key,
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
	base, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	database := OpenOver(installedPackageFailure{Store: base.Obj(), failure: injected})
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
	for _, name := range []string{"", "bad" + store.KeySep + "name"} {
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

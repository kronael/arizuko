package routd

import "testing"

// TestInstalledPackageCRUD exercises the installed-package record (spec 5/28):
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
// (BUGS F30): spec 5/28's composition blends a product mix per GROUP, so two
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

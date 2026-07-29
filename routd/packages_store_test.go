package routd

import "testing"

// TestInstalledPackageCRUD exercises the installed-package record (spec 5/28):
// absent → put → get roundtrip (incl. JSON manifest + hashes) → upsert/upgrade
// → list → delete. This is the lock P1+ install/upgrade/remove build on.
func TestInstalledPackageCRUD(t *testing.T) {
	d, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	if _, ok, err := d.InstalledPackage("slakd"); err != nil || ok {
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

	got, ok, err := d.InstalledPackage("slakd")
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
	got, _, _ = d.InstalledPackage("slakd")
	if got.Revision != "def456" || len(got.Manifest) != 1 {
		t.Fatalf("upgrade mismatch: %+v", got)
	}

	if ps, err := d.InstalledPackages(); err != nil || len(ps) != 1 || ps[0].Name != "slakd" {
		t.Fatalf("list: n=%d err=%v", len(ps), err)
	}

	if ok, err := d.DeleteInstalledPackage("slakd"); err != nil || !ok {
		t.Fatalf("delete: ok=%v err=%v", ok, err)
	}
	if _, ok, _ := d.InstalledPackage("slakd"); ok {
		t.Fatal("still present after delete")
	}
	if ok, _ := d.DeleteInstalledPackage("slakd"); ok {
		t.Fatal("second delete reported ok")
	}
}

package routd

import (
	"database/sql"
	"testing"
)

// TestInstalledPackageCRUD exercises the installed-package record (spec 5/28):
// absent → put → get roundtrip (incl. JSON manifest + hashes) → upsert/upgrade
// → list → delete. This is the lock P1+ install/upgrade/remove build on.
func TestInstalledPackageCRUD(t *testing.T) {
	d, err := Create(t.TempDir())
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

// TestInstalledPackageFolderIsPartOfKey is the whole point of migration 0031
// (BUGS F30): spec 5/28's composition blends a product mix per GROUP, so two
// groups must be able to hold the SAME package name independently. Under the old
// `name TEXT PRIMARY KEY` the second Put silently UPDATED the first — one lock
// for two subjects. Falsified by reverting the PK to name alone: the second Put
// overwrites, len(list) is 1 not 3, and the per-folder gets return each other's
// revision.
func TestInstalledPackageFolderIsPartOfKey(t *testing.T) {
	d, err := Create(t.TempDir())
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

// TestInstalledPackagesMigration0031PreservesRows is the migration rehearsal as
// a test. It writes the PRE-0031 table, fills it, then runs 0031's body and
// asserts every row survives with byte-identical content on the instance-wide
// sentinel. The fleet lost its tools to a migration whose rehearsal "matched 0
// rows and looked fine", so this asserts content BEFORE and AFTER, not just that
// the DDL ran. Falsified by changing 0031's INSERT ... SELECT to add a WHERE
// that matches nothing: count goes 3 -> 0 and this fails alone.
func TestInstalledPackagesMigration0031PreservesRows(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/m.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	// The shape 0020 shipped, verbatim.
	if _, err := db.Exec(`CREATE TABLE installed_packages (
		name TEXT PRIMARY KEY, source TEXT NOT NULL, revision TEXT NOT NULL,
		manifest TEXT NOT NULL DEFAULT '{}', asset_hashes TEXT NOT NULL DEFAULT '{}',
		installed_at TEXT NOT NULL)`); err != nil {
		t.Fatalf("create 0020 table: %v", err)
	}
	seed := [][]string{
		{"ttsd", "https://github.com/kronael/ttsd", "a1b2c3",
			`{"compose_fragment":["ttsd.yml"],"proxyd_route":["/tts/"]}`, `{"file:ttsd.yml":"deadbeef"}`, "2026-08-01T10:00:00Z"},
		{"slakd", "/srv/pkgs/slakd", "v2", `{"compose_fragment":["slakd.yml"]}`, `{"file:slakd.yml":"cafe"}`, "2026-08-02T11:00:00Z"},
		{"zzz", "https://example.com/zzz", "rev9", "{}", "{}", "2026-08-03T12:00:00Z"},
	}
	for _, r := range seed {
		if _, err := db.Exec(`INSERT INTO installed_packages
			(name, source, revision, manifest, asset_hashes, installed_at) VALUES (?,?,?,?,?,?)`,
			r[0], r[1], r[2], r[3], r[4], r[5]); err != nil {
			t.Fatalf("seed %s: %v", r[0], err)
		}
	}
	const dump = `SELECT name||'|'||source||'|'||revision||'|'||manifest||'|'||asset_hashes||'|'||installed_at
		FROM installed_packages ORDER BY name`
	before := scanStrings(t, db, dump)
	if len(before) != len(seed) {
		t.Fatalf("seed did not land: %d rows", len(before))
	}

	raw, err := migrationFS.ReadFile("migrations/0031-installed-packages-folder.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if _, err := db.Exec(string(raw)); err != nil {
		t.Fatalf("apply 0031: %v", err)
	}

	after := scanStrings(t, db, dump)
	if len(after) != len(before) {
		t.Fatalf("row count %d -> %d: the migration dropped rows", len(before), len(after))
	}
	for i := range before {
		if before[i] != after[i] {
			t.Errorf("row %d changed:\n before %s\n after  %s", i, before[i], after[i])
		}
	}
	for _, f := range scanStrings(t, db, `SELECT folder FROM installed_packages`) {
		if f != InstanceWide {
			t.Errorf("pre-0031 row landed on folder %q, want the instance-wide sentinel", f)
		}
	}
	if pk := scanStrings(t, db,
		`SELECT name FROM pragma_table_info('installed_packages') WHERE pk > 0 ORDER BY pk`); len(pk) != 2 ||
		pk[0] != "folder" || pk[1] != "name" {
		t.Errorf("primary key = %v, want [folder name]", pk)
	}
}

func scanStrings(t *testing.T, db *sql.DB, query string) []string {
	t.Helper()
	rows, err := db.Query(query)
	if err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

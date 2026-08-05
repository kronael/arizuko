package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kronael/arizuko/routd"
)

// A package apply mutates many acl rows at once, so it needs a verdict of its
// own: ONE audit row for the bundle, or N for the grants? N (BUGS.md Q5). A
// bundle row would say "installed grantpkg" and hide that the install handed
// @ops admin on a folder — the question an audit trail answers is per-grant, and
// installed_packages already records the bundle. Each row carries the package in
// granted_by, so N rows still read as one act. `packages remove` was already
// per-grant (audited RemoveACLRow); install now matches it.
//
// Falsifiable: revert applyPackageGrants to st.PutACLRow and the acl rows still
// land, but aclAuditRows returns none. Drop the granted_by ParamsSummary entry
// and the provenance case fails while the count case passes — which is the whole
// difference between a per-row trail that is useful and one that is merely
// truthful. Drop AsCLI and the actor falls back to the granted_by string.

// aclAuditRows returns (actor, surface, params_summary) of every audit_log row
// for action in dir/routd.db, oldest first.
func aclAuditRows(t *testing.T, dir, action string) [][3]string {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, "routd.db"))
	if err != nil {
		t.Fatalf("open routd.db: %v", err)
	}
	defer db.Close()
	rows, err := db.Query(
		`SELECT COALESCE(actor, ''), COALESCE(surface, ''), COALESCE(params_summary, '')
		 FROM audit_log WHERE action = ? ORDER BY id`, action)
	if err != nil {
		t.Fatalf("query audit_log: %v", err)
	}
	defer rows.Close()
	var out [][3]string
	for rows.Next() {
		var r [3]string
		if err := rows.Scan(&r[0], &r[1], &r[2]); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate: %v", err)
	}
	return out
}

// installGrantPkg installs a two-grant package as osUser and returns the store dir.
func installGrantPkg(t *testing.T, osUser string) string {
	t.Helper()
	base := t.TempDir()
	t.Setenv("ARIZUKO_DATA_DIR", base)
	t.Setenv("USER", osUser)
	dataDir := filepath.Join(base, "arizuko_gaudit")
	storeDir := filepath.Join(dataDir, "store")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rdb, err := routd.Open(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	has := hasTable(rdb, "acl")
	rdb.Close()
	if !has {
		t.Skip("routd.Open db has no acl table")
	}
	src := filepath.Join(base, "grantpkg")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "grantpkg.yml"),
		[]byte("services:\n  grantpkg:\n    image: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "grantpkg-grants.json"),
		[]byte(`[{"principal":"@ops","action":"reply","scope":"eng/*"},`+
			`{"principal":"@ops","action":"admin","scope":"eng/tts"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	cmdPackages([]string{"gaudit", "install", src})
	return storeDir
}

// One row per grant, not one for the bundle: an operator auditing the install
// must see WHICH authority it handed out, on which scope.
func TestPackageGrantsAuditedPerRow(t *testing.T) {
	dir := installGrantPkg(t, "alice")

	rows := aclAuditRows(t, dir, "acl.add")
	if len(rows) != 2 {
		t.Fatalf("acl.add rows = %d, want 2 (one per grant, not one per bundle)", len(rows))
	}
	for i, r := range rows {
		if r[0] != "cli:alice" || r[1] != "cli" {
			t.Errorf("row %d actor/surface = (%q, %q), want (cli:alice, cli)", i, r[0], r[1])
		}
	}
	if !strings.Contains(rows[0][2], `"scope":"eng/*"`) ||
		!strings.Contains(rows[1][2], `"scope":"eng/tts"`) {
		t.Errorf("rows do not name the individual scopes: %q, %q", rows[0][2], rows[1][2])
	}
}

// The rows must also say the grants came from a package rather than from an
// operator typing `arizuko grant` — the actor is the same person either way.
func TestPackageGrantsAuditNamesPackage(t *testing.T) {
	dir := installGrantPkg(t, "alice")

	for i, r := range aclAuditRows(t, dir, "acl.add") {
		if !strings.Contains(r[2], `"granted_by":"package:grantpkg"`) {
			t.Errorf("row %d params_summary = %q, want granted_by package:grantpkg", i, r[2])
		}
	}
}

// Remove already used the audited RemoveACLRow; it must now name the operator
// too, so install and remove are one symmetric trail.
func TestPackageGrantsRemovalAudited(t *testing.T) {
	dir := installGrantPkg(t, "alice")
	cmdPackages([]string{"gaudit", "remove", "grantpkg"})

	rows := aclAuditRows(t, dir, "acl.remove")
	if len(rows) != 2 {
		t.Fatalf("acl.remove rows = %d, want 2", len(rows))
	}
	for i, r := range rows {
		if r[0] != "cli:alice" || r[1] != "cli" {
			t.Errorf("row %d actor/surface = (%q, %q), want (cli:alice, cli)", i, r[0], r[1])
		}
	}
}

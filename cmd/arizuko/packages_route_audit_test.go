package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kronael/arizuko/routd"
)

// A package install opens public URLs onto backends with no restart, and until
// BUGS.md F2 it left nothing behind — while applyPackageGrants, twenty lines up
// the SAME install path, audited per grant. The route is the larger blast radius
// of the two, and proxyd's own /v1/proxyd_routes resource already records the
// identical mutation (resreg emits in the handler's tx), so the CLI was not
// "matching proxyd's discipline", it was the only silent writer left.
//
// Falsifiable per writer: drop the audit.Event return from PutProxydRoute (or
// revert it to the bare tx) and the route still lands in proxyd_routes — the
// hot-apply test TestPackagesInstallRoutesHotApply stays green — but
// TestPackageRoutesAudited fails with 0 rows. Same for DeleteProxydRoute and
// TestPackageRouteRemovalAudited. Drop AsCLI in packageStore and only the
// actor/surface assertions fail: the rows are there, attributed to the daemon.

// installRoutePkg installs a two-route package as osUser and returns the store dir.
func installRoutePkg(t *testing.T, osUser string) string {
	t.Helper()
	base := t.TempDir()
	t.Setenv("ARIZUKO_DATA_DIR", base)
	t.Setenv("USER", osUser)
	dataDir := filepath.Join(base, "arizuko_raudit")
	storeDir := filepath.Join(dataDir, "store")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rdb, err := routd.Create(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	has := hasTable(rdb, "proxyd_routes")
	rdb.Close()
	if !has {
		t.Skip("routd.Open db has no proxyd_routes table")
	}
	src := filepath.Join(base, "routepkg")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "routepkg.yml"),
		[]byte("services:\n  routepkg:\n    image: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "routepkg-routes.json"),
		[]byte(`[{"path":"/rp/","backend":"http://routepkg:8080","auth":"public"},`+
			`{"path":"/rp-admin/","backend":"http://routepkg:8080","auth":"auth"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	cmdPackages([]string{"raudit", "install", src})
	return storeDir
}

// One row per route, naming the operator who installed the package — same
// verdict as the grant half (BUGS.md Q5): the question an audit trail answers is
// per-route ("which URL opened onto what"), and installed_packages already
// records the bundle.
func TestPackageRoutesAudited(t *testing.T) {
	dir := installRoutePkg(t, "alice")

	rows := aclAuditRows(t, dir, "proxyd_route.set")
	if len(rows) != 2 {
		t.Fatalf("proxyd_route.set rows = %d, want 2 (one per route)", len(rows))
	}
	for i, r := range rows {
		if r[0] != "cli:alice" || r[1] != "cli" {
			t.Errorf("row %d actor/surface = (%q, %q), want (cli:alice, cli)", i, r[0], r[1])
		}
	}
}

// The path alone does not say what the route exposes. A reader auditing "what
// did this install open" needs the backend it fronts and the auth in front of
// it — the two fields that ARE the blast radius.
func TestPackageRouteAuditNamesBackendAndAuth(t *testing.T) {
	dir := installRoutePkg(t, "alice")

	rows := aclAuditRows(t, dir, "proxyd_route.set")
	if len(rows) != 2 {
		t.Fatalf("proxyd_route.set rows = %d, want 2", len(rows))
	}
	for i, want := range []string{`"auth":"public"`, `"auth":"auth"`} {
		if !strings.Contains(rows[i][2], `"backend":"http://routepkg:8080"`) {
			t.Errorf("row %d params_summary = %q, want the backend", i, rows[i][2])
		}
		if !strings.Contains(rows[i][2], want) {
			t.Errorf("row %d params_summary = %q, want %s", i, rows[i][2], want)
		}
	}
}

// Withdrawal must record too. A trail that shows routes opening and never
// closing reads as "still live" for a route that is gone — worse than silence,
// because it looks complete.
func TestPackageRouteRemovalAudited(t *testing.T) {
	dir := installRoutePkg(t, "alice")
	cmdPackages([]string{"raudit", "remove", "routepkg"})

	rows := aclAuditRows(t, dir, "proxyd_route.delete")
	if len(rows) != 2 {
		t.Fatalf("proxyd_route.delete rows = %d, want 2", len(rows))
	}
	for i, r := range rows {
		if r[0] != "cli:alice" || r[1] != "cli" {
			t.Errorf("row %d actor/surface = (%q, %q), want (cli:alice, cli)", i, r[0], r[1])
		}
		if !strings.Contains(r[2], `"deleted":true`) {
			t.Errorf("row %d params_summary = %q, want deleted:true", i, r[2])
		}
	}
}

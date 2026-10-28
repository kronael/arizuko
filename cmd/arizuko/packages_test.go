package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/routd"
)

// C1: package names that could traverse outside services/ must be rejected.
func TestPkgNameRE(t *testing.T) {
	good := []string{"teled", "teled-rhias", "ttsd", "a_b.c", "x1"}
	bad := []string{"../docker-compose", "..", ".hidden", "a/b", "a b", "", "-lead", "a\nb"}
	for _, n := range good {
		if !pkgNameRE.MatchString(n) {
			t.Errorf("pkgNameRE rejected valid name %q", n)
		}
	}
	for _, n := range bad {
		if pkgNameRE.MatchString(n) {
			t.Errorf("pkgNameRE accepted unsafe name %q", n)
		}
	}
}

// C7: writeFileAtomic replaces content whole; a reader never sees a partial file.
func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "frag.yml")
	if err := writeFileAtomic(p, []byte("services:\n  x: {}\n")); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(p, []byte("v2\n")); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "v2\n" {
		t.Fatalf("content = %q, want v2", b)
	}
}

// C5: depends_on on a non-base, non-present service is detected (ttsd → kokoro).
func TestDependsOnParse(t *testing.T) {
	frag := []byte("services:\n  ttsd:\n    depends_on: [kokoro]\n    restart: on-failure\n")
	m := dependsOnRE.FindSubmatch(frag)
	if m == nil {
		t.Fatal("depends_on line not matched")
	}
	if got := string(m[1]); got != "kokoro" {
		t.Fatalf("dep = %q, want kokoro", got)
	}
	// a base-daemon dep is not flagged
	if baseDaemons["kokoro"] {
		t.Fatal("kokoro must not be a base daemon (it is a package)")
	}
	if !baseDaemons["routd"] {
		t.Fatal("routd must be a base daemon")
	}
}

// TestPackagesInstallRemove is the P1 install→record→remove flow (see compose/README.md):
// install a source dir's fragment assets, write the installed-package record,
// then remove exactly what the record says was installed.

// TestDirtyAssets is P3's safety core (see compose/README.md): a recorded asset whose
// on-disk content still matches its install hash is clean; an operator edit
// makes it dirty, so upgrade will refuse to overwrite it.
func TestDirtyAssets(t *testing.T) {
	svc := t.TempDir()
	if err := os.WriteFile(filepath.Join(svc, "a.yml"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := routd.InstalledPackage{AssetHashes: map[string]string{"file:a.yml": sha256hex([]byte("v1"))}}
	if d := dirtyAssets(svc, rec); len(d) != 0 {
		t.Fatalf("clean asset flagged dirty: %v", d)
	}
	if err := os.WriteFile(filepath.Join(svc, "a.yml"), []byte("edited"), 0o644); err != nil {
		t.Fatal(err)
	}
	if d := dirtyAssets(svc, rec); len(d) != 1 || d[0] != "a.yml" {
		t.Fatalf("edited asset not flagged dirty: %v", d)
	}
	catalog := filepath.Join(t.TempDir(), "b.yml")
	if err := os.WriteFile(catalog, []byte("next release"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(catalog, filepath.Join(svc, "b.yml")); err != nil {
		t.Fatal(err)
	}
	rec.AssetHashes["file:b.yml"] = sha256hex([]byte("previous release"))
	if d := dirtyAssets(svc, rec); len(d) != 1 || d[0] != "a.yml" {
		t.Fatalf("catalog-linked fragment flagged dirty (BUGS L635): %v", d)
	}
}

// TestPackagesUpgradeClean: an unedited install upgrades to the source's new
// revision and the record hash updates.

// seedPkg writes a one-fragment package source dir and installs it, returning
// the source dir so a test can move it to a new revision.

// TestPackagesUpgradePreservesNonFileManifest: upgrade re-applies only the
// compose_fragment assets, so the skill / route / grant identities install
// recorded SURVIVE it. Without this, `remove` after an `upgrade` strands
// exactly the rows and dirs it is supposed to delete.

// TestPackagesSyncReapplies: sync re-applies every installed package from its
// recorded source in one pass — the blend table's "on upstream update" verb.

// TestPackagesSyncSkipsDirty: a locally edited fragment is reported and skipped,
// never clobbered — and one dirty package does not stop the rest of the
// instance from syncing.

// TestPackagesInstallGit is P1b (see compose/README.md): install from a git source resolves
// an immutable revision (not "local") and records it, cloning via file:// URL.

// TestPackagesInstallRoutesHotApply is P2 (fixes 5/27-compose-native-packaging C2): install
// writes a package manifest's route straight into the live proxyd_routes table
// and records its PK; remove deletes it. proxyd reads that table per request, so
// the route is live without a restart.

// TestPackagesInstallGrants: the acl asset kind (see compose/README.md) — install applies a
// package manifest's acl block and records the rows; remove reverses exactly.

// Installing package skills records immutable artifacts; removal withdraws their selection.

// TestPackagesUpgradeReportsCatalogManifestDrift: a catalog package's files are
// links into the catalog, so upgrade has nothing to re-fetch — but the ROWS its
// manifest declares were applied once, by `add`, and neither verb re-applies
// them. When a release moves the manifest, the operator was told
// "already at catalog — nothing to do" while its new rows reached no instance
// (BUGS L315). Both sinks are asserted: there is one updater, two reports.

// The line has to separate two rows that differ only in their predicate,
// because an operator reads it to decide whether to accept the install and then
// to type the `ungrant` that removes one of them. store.aclKey puts the
// predicate in the key, so two such rows ARE two rows (BUGS L344).
func TestDescribeGrantNamesThePredicate(t *testing.T) {
	base := core.ACLRow{Principal: "github:bob", Action: "mcp:send", Scope: "main/**"}
	narrowed := base
	narrowed.Predicate = "platform=telegram"

	plain, narrow := describeGrant(base), describeGrant(narrowed)
	if plain == narrow {
		t.Fatalf("two rows that differ by predicate render as one line: %q", plain)
	}
	if !strings.Contains(narrow, "platform=telegram") {
		t.Errorf("the line does not name the predicate: %q", narrow)
	}
}

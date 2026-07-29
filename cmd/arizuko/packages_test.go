package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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

// TestPackagesInstallRemove is the P1 install→record→remove flow (spec 5/28):
// install a source dir's fragment assets, write the installed-package record,
// then remove exactly what the record says was installed.
func TestPackagesInstallRemove(t *testing.T) {
	base := t.TempDir()
	t.Setenv("ARIZUKO_DATA_DIR", base)
	dataDir := filepath.Join(base, "arizuko_t")
	if err := os.MkdirAll(filepath.Join(dataDir, "store"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(base, "teledpkg")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "teledpkg.yml"),
		[]byte("services:\n  teledpkg:\n    image: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "teledpkg-routes.json"), []byte("[]"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmdPackages([]string{"t", "install", src})

	if _, err := os.Stat(filepath.Join(dataDir, "services", "teledpkg.yml")); err != nil {
		t.Fatalf("fragment not installed: %v", err)
	}
	rdb, err := routd.Open(filepath.Join(dataDir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	rec, ok, err := rdb.InstalledPackage("teledpkg")
	if err != nil || !ok {
		t.Fatalf("record missing: ok=%v err=%v", ok, err)
	}
	if len(rec.Manifest["compose_fragment"]) != 2 || rec.AssetHashes["file:teledpkg.yml"] == "" {
		t.Fatalf("record contents: %+v", rec)
	}
	rdb.Close()

	cmdPackages([]string{"t", "remove", "teledpkg"})

	if _, err := os.Stat(filepath.Join(dataDir, "services", "teledpkg.yml")); !os.IsNotExist(err) {
		t.Fatalf("fragment not removed: %v", err)
	}
	rdb2, err := routd.Open(filepath.Join(dataDir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	defer rdb2.Close()
	if _, ok, _ := rdb2.InstalledPackage("teledpkg"); ok {
		t.Fatal("record not removed")
	}
}

// TestDirtyAssets is P3's safety core (spec 5/28): a recorded asset whose
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
}

// TestPackagesUpgradeClean: an unedited install upgrades to the source's new
// revision and the record hash updates.
func TestPackagesUpgradeClean(t *testing.T) {
	base := t.TempDir()
	t.Setenv("ARIZUKO_DATA_DIR", base)
	dataDir := filepath.Join(base, "arizuko_u")
	if err := os.MkdirAll(filepath.Join(dataDir, "store"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(base, "up")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "up.yml"), []byte("services:\n  up:\n    image: v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmdPackages([]string{"u", "install", src})

	if err := os.WriteFile(filepath.Join(src, "up.yml"), []byte("services:\n  up:\n    image: v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmdPackages([]string{"u", "upgrade", "up"})

	b, err := os.ReadFile(filepath.Join(dataDir, "services", "up.yml"))
	if err != nil || !strings.Contains(string(b), "v2") {
		t.Fatalf("upgrade did not apply new version: %s (err %v)", b, err)
	}
	rdb, err := routd.Open(filepath.Join(dataDir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	defer rdb.Close()
	rec, ok, _ := rdb.InstalledPackage("up")
	if !ok || rec.AssetHashes["file:up.yml"] != sha256hex(b) {
		t.Fatalf("record hash not updated on upgrade: %+v", rec)
	}
}

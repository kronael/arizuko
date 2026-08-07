package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kronael/arizuko/db_utils"
	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/store"
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
	if err := db_utils.CreateDBFile(store.OwnerDBPath(filepath.Join(dataDir, "store"), store.OwnerRoutd)); err != nil {
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
	rdb, err := routd.Create(filepath.Join(dataDir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	rec, ok, err := rdb.InstalledPackage(routd.InstanceWide, "teledpkg")
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
	rdb2, err := routd.Create(filepath.Join(dataDir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	defer rdb2.Close()
	if _, ok, _ := rdb2.InstalledPackage(routd.InstanceWide, "teledpkg"); ok {
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
	if err := db_utils.CreateDBFile(store.OwnerDBPath(filepath.Join(dataDir, "store"), store.OwnerRoutd)); err != nil {
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
	rdb, err := routd.Create(filepath.Join(dataDir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	defer rdb.Close()
	rec, ok, _ := rdb.InstalledPackage(routd.InstanceWide, "up")
	if !ok || rec.AssetHashes["file:up.yml"] != sha256hex(b) {
		t.Fatalf("record hash not updated on upgrade: %+v", rec)
	}
}

// seedPkg writes a one-fragment package source dir and installs it, returning
// the source dir so a test can move it to a new revision.
func seedPkg(t *testing.T, base, dataDir, inst, name, body string) string {
	t.Helper()
	src := filepath.Join(base, name)
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, name+".yml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cmdPackages([]string{inst, "install", src})
	return src
}

func mustRec(t *testing.T, dataDir, name string) routd.InstalledPackage {
	t.Helper()
	rdb, err := routd.Create(filepath.Join(dataDir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	defer rdb.Close()
	rec, ok, err := rdb.InstalledPackage(routd.InstanceWide, name)
	if err != nil || !ok {
		t.Fatalf("record %s missing: ok=%v err=%v", name, ok, err)
	}
	return rec
}

// TestPackagesUpgradePreservesNonFileManifest: upgrade re-applies only the
// compose_fragment assets, so the skill / route / grant identities install
// recorded SURVIVE it. Without this, `remove` after an `upgrade` strands
// exactly the rows and dirs it is supposed to delete.
func TestPackagesUpgradePreservesNonFileManifest(t *testing.T) {
	base := t.TempDir()
	t.Setenv("ARIZUKO_DATA_DIR", base)
	dataDir := filepath.Join(base, "arizuko_mf")
	if err := db_utils.CreateDBFile(store.OwnerDBPath(filepath.Join(dataDir, "store"), store.OwnerRoutd)); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(base, "mf")
	if err := os.MkdirAll(filepath.Join(src, "skills", "mfskill"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "mf.yml"), []byte("services:\n  mf:\n    image: v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "skills", "mfskill", "SKILL.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmdPackages([]string{"mf", "install", src})
	if got := mustRec(t, dataDir, "mf").Manifest["skill"]; len(got) != 1 {
		t.Fatalf("install did not record the skill: %v", got)
	}

	if err := os.WriteFile(filepath.Join(src, "mf.yml"), []byte("services:\n  mf:\n    image: v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmdPackages([]string{"mf", "upgrade", "mf"})

	rec := mustRec(t, dataDir, "mf")
	if got := rec.Manifest["skill"]; len(got) != 1 || got[0] != "mfskill" {
		t.Fatalf("upgrade dropped the skill identity: %+v", rec.Manifest)
	}
	if got := rec.Manifest["compose_fragment"]; len(got) != 1 || got[0] != "mf.yml" {
		t.Fatalf("upgrade lost the fragment identity: %+v", rec.Manifest)
	}
}

// TestPackagesSyncReapplies: sync re-applies every installed package from its
// recorded source in one pass — the blend table's "on upstream update" verb.
func TestPackagesSyncReapplies(t *testing.T) {
	base := t.TempDir()
	t.Setenv("ARIZUKO_DATA_DIR", base)
	dataDir := filepath.Join(base, "arizuko_sy")
	if err := db_utils.CreateDBFile(store.OwnerDBPath(filepath.Join(dataDir, "store"), store.OwnerRoutd)); err != nil {
		t.Fatal(err)
	}
	srcA := seedPkg(t, base, dataDir, "sy", "alpha", "services:\n  alpha:\n    image: v1\n")
	srcB := seedPkg(t, base, dataDir, "sy", "beta", "services:\n  beta:\n    image: v1\n")

	if err := os.WriteFile(filepath.Join(srcA, "alpha.yml"), []byte("services:\n  alpha:\n    image: v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcB, "beta.yml"), []byte("services:\n  beta:\n    image: v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmdPackages([]string{"sy", "sync"})

	for _, n := range []string{"alpha", "beta"} {
		b, err := os.ReadFile(filepath.Join(dataDir, "services", n+".yml"))
		if err != nil || !strings.Contains(string(b), "v2") {
			t.Fatalf("sync did not re-apply %s: %s (err %v)", n, b, err)
		}
		if got := mustRec(t, dataDir, n).AssetHashes["file:"+n+".yml"]; got != sha256hex(b) {
			t.Fatalf("sync left %s record hash stale", n)
		}
	}
}

// TestPackagesSyncIdempotent: re-applying an unchanged source reports
// Changed=false and leaves the record untouched. Asserted against a sentinel
// InstalledAt rather than two wall-clock reads — RFC3339 is second-granular, so
// comparing two same-second syncs would pass even if the record WERE rewritten.
func TestPackagesSyncIdempotent(t *testing.T) {
	base := t.TempDir()
	t.Setenv("ARIZUKO_DATA_DIR", base)
	dataDir := filepath.Join(base, "arizuko_si")
	if err := db_utils.CreateDBFile(store.OwnerDBPath(filepath.Join(dataDir, "store"), store.OwnerRoutd)); err != nil {
		t.Fatal(err)
	}
	seedPkg(t, base, dataDir, "si", "gamma", "services:\n  gamma:\n    image: v1\n")

	rdb, err := routd.Create(filepath.Join(dataDir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	defer rdb.Close()
	svcDir := filepath.Join(dataDir, "services")

	rec, _, err := rdb.InstalledPackage(routd.InstanceWide, "gamma")
	if err != nil {
		t.Fatal(err)
	}
	const sentinel = "1999-01-01T00:00:00Z"
	rec.InstalledAt = sentinel
	if err := rdb.PutInstalledPackage(rec); err != nil {
		t.Fatal(err)
	}

	res := reapplyPackage(svcDir, rdb, rec)
	if res.Changed {
		t.Fatal("re-applying an unchanged source reported Changed")
	}
	after, _, err := rdb.InstalledPackage(routd.InstanceWide, "gamma")
	if err != nil {
		t.Fatal(err)
	}
	if after.InstalledAt != sentinel {
		t.Fatalf("sync rewrote an unchanged record: InstalledAt %q -> %q", sentinel, after.InstalledAt)
	}
}

// TestPackagesSyncSkipsDirty: a locally edited fragment is reported and skipped,
// never clobbered — and one dirty package does not stop the rest of the
// instance from syncing.
func TestPackagesSyncSkipsDirty(t *testing.T) {
	base := t.TempDir()
	t.Setenv("ARIZUKO_DATA_DIR", base)
	dataDir := filepath.Join(base, "arizuko_sd")
	if err := db_utils.CreateDBFile(store.OwnerDBPath(filepath.Join(dataDir, "store"), store.OwnerRoutd)); err != nil {
		t.Fatal(err)
	}
	srcD := seedPkg(t, base, dataDir, "sd", "delta", "services:\n  delta:\n    image: v1\n")
	srcE := seedPkg(t, base, dataDir, "sd", "eps", "services:\n  eps:\n    image: v1\n")

	// Operator hand-edits delta's installed fragment; both sources move on.
	edited := "services:\n  delta:\n    image: MINE\n"
	if err := os.WriteFile(filepath.Join(dataDir, "services", "delta.yml"), []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcD, "delta.yml"), []byte("services:\n  delta:\n    image: v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcE, "eps.yml"), []byte("services:\n  eps:\n    image: v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmdPackages([]string{"sd", "sync"})

	b, err := os.ReadFile(filepath.Join(dataDir, "services", "delta.yml"))
	if err != nil || string(b) != edited {
		t.Fatalf("sync clobbered a locally edited fragment: %s (err %v)", b, err)
	}
	e, err := os.ReadFile(filepath.Join(dataDir, "services", "eps.yml"))
	if err != nil || !strings.Contains(string(e), "v2") {
		t.Fatalf("a dirty package blocked the rest of the sync: %s (err %v)", e, err)
	}
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "add", "."},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "init"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// TestPackagesInstallGit is P1b (spec 5/28): install from a git source resolves
// an immutable revision (not "local") and records it, cloning via file:// URL.
func TestPackagesInstallGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	base := t.TempDir()
	t.Setenv("ARIZUKO_DATA_DIR", base)
	dataDir := filepath.Join(base, "arizuko_g")
	if err := db_utils.CreateDBFile(store.OwnerDBPath(filepath.Join(dataDir, "store"), store.OwnerRoutd)); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(base, "gitpkg")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "gitpkg.yml"),
		[]byte("services:\n  gitpkg:\n    image: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitInit(t, repo)

	cmdPackages([]string{"g", "install", "file://" + repo})

	if _, err := os.Stat(filepath.Join(dataDir, "services", "gitpkg.yml")); err != nil {
		t.Fatalf("git package fragment not installed: %v", err)
	}
	rdb, err := routd.Create(filepath.Join(dataDir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	defer rdb.Close()
	rec, ok, _ := rdb.InstalledPackage(routd.InstanceWide, "gitpkg")
	if !ok || rec.Revision == "local" || len(rec.Revision) < 7 || rec.Source != "file://"+repo {
		t.Fatalf("git revision/source not recorded: %+v", rec)
	}
}

// TestPackagesInstallRoutesHotApply is P2 (spec 5/28, fixes 5/27 C2): install
// writes a package's route straight into the live proxyd_routes table and
// records the path; remove deletes it. Skips if a fresh routd.Open db lacks the
// (daemon-migrated) proxyd_routes table — the mechanism is covered by
// store.TestPutDeleteProxydRoute.
func TestPackagesInstallRoutesHotApply(t *testing.T) {
	base := t.TempDir()
	t.Setenv("ARIZUKO_DATA_DIR", base)
	dataDir := filepath.Join(base, "arizuko_r")
	if err := db_utils.CreateDBFile(store.OwnerDBPath(filepath.Join(dataDir, "store"), store.OwnerRoutd)); err != nil {
		t.Fatal(err)
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
		[]byte(`[{"path":"/rp/","backend":"http://routepkg:8080","auth":"public"}]`), 0o644); err != nil {
		t.Fatal(err)
	}

	rdb0, err := routd.Create(filepath.Join(dataDir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	has := hasTable(rdb0, "proxyd_routes")
	rdb0.Close()
	if !has {
		t.Skip("routd.Open db has no proxyd_routes; P2 mechanism covered by store.TestPutDeleteProxydRoute")
	}

	cmdPackages([]string{"r", "install", src})

	rdb, err := routd.Create(filepath.Join(dataDir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	if !routePresent(t, rdb, "/rp/") {
		rdb.Close()
		t.Fatal("route not hot-applied to proxyd_routes")
	}
	rec, _, _ := rdb.InstalledPackage(routd.InstanceWide, "routepkg")
	if len(rec.Manifest["proxyd_route"]) != 1 || rec.Manifest["proxyd_route"][0] != "/rp/" {
		rdb.Close()
		t.Fatalf("route path not recorded: %+v", rec)
	}
	rdb.Close()

	cmdPackages([]string{"r", "remove", "routepkg"})

	rdb2, err := routd.Create(filepath.Join(dataDir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	defer rdb2.Close()
	if routePresent(t, rdb2, "/rp/") {
		t.Fatal("route not removed from proxyd_routes")
	}
}

func routePresent(t *testing.T, rdb *routd.DB, path string) bool {
	t.Helper()
	all, err := store.New(rdb.SQL()).AllProxydRoutes()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range all {
		if r.Path == path {
			return true
		}
	}
	return false
}

// TestPackagesInstallGrants: the acl asset kind (spec 5/28) — install applies a
// package's *-grants.json into acl and records them; remove reverses exactly.
func TestPackagesInstallGrants(t *testing.T) {
	base := t.TempDir()
	t.Setenv("ARIZUKO_DATA_DIR", base)
	dataDir := filepath.Join(base, "arizuko_gr")
	if err := db_utils.CreateDBFile(store.OwnerDBPath(filepath.Join(dataDir, "store"), store.OwnerRoutd)); err != nil {
		t.Fatal(err)
	}
	rdb0, err := routd.Create(filepath.Join(dataDir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	has := hasTable(rdb0, "acl")
	rdb0.Close()
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
		[]byte(`[{"principal":"@ops","action":"reply","scope":"eng/*"}]`), 0o644); err != nil {
		t.Fatal(err)
	}

	cmdPackages([]string{"gr", "install", src})

	rdb, err := routd.Create(filepath.Join(dataDir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	rows := store.New(rdb.SQL()).ListACL("@ops")
	found := false
	for _, r := range rows {
		if r.Action == "reply" && r.Scope == "eng/*" {
			found = true
		}
	}
	rec, _, _ := rdb.InstalledPackage(routd.InstanceWide, "grantpkg")
	rdb.Close()
	if !found {
		t.Fatal("grant not applied to acl")
	}
	if len(rec.Manifest["grant"]) != 1 {
		t.Fatalf("grant not recorded: %+v", rec)
	}

	cmdPackages([]string{"gr", "remove", "grantpkg"})

	rdb2, err := routd.Create(filepath.Join(dataDir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	defer rdb2.Close()
	for _, r := range store.New(rdb2.SQL()).ListACL("@ops") {
		if r.Action == "reply" && r.Scope == "eng/*" {
			t.Fatal("grant not removed from acl")
		}
	}
}

// TestPackagesInstallSkills: the skills asset kind (spec 5/28) — install copies
// a package's skills/<name>/ into <datadir>/skills/<name>/ (seedSkills layers it
// into every group), records the names; remove deletes them.
func TestPackagesInstallSkills(t *testing.T) {
	base := t.TempDir()
	t.Setenv("ARIZUKO_DATA_DIR", base)
	dataDir := filepath.Join(base, "arizuko_sk")
	if err := db_utils.CreateDBFile(store.OwnerDBPath(filepath.Join(dataDir, "store"), store.OwnerRoutd)); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(base, "skillpkg")
	if err := os.MkdirAll(filepath.Join(src, "skills", "mytool"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "skillpkg.yml"),
		[]byte("services:\n  skillpkg:\n    image: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "skills", "mytool", "SKILL.md"), []byte("# mytool"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmdPackages([]string{"sk", "install", src})

	if _, err := os.Stat(filepath.Join(dataDir, "skills", "mytool", "SKILL.md")); err != nil {
		t.Fatalf("skill not installed: %v", err)
	}
	rdb, err := routd.Create(filepath.Join(dataDir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	rec, _, _ := rdb.InstalledPackage(routd.InstanceWide, "skillpkg")
	rdb.Close()
	if len(rec.Manifest["skill"]) != 1 || rec.Manifest["skill"][0] != "mytool" {
		t.Fatalf("skill not recorded: %+v", rec)
	}

	cmdPackages([]string{"sk", "remove", "skillpkg"})
	if _, err := os.Stat(filepath.Join(dataDir, "skills", "mytool")); !os.IsNotExist(err) {
		t.Fatal("skill dir not removed")
	}
}

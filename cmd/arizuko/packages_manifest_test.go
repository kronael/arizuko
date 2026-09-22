package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kronael/arizuko/compose"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/store"
)

// BUGS L44: packages declared grants and proxyd routes in two bespoke JSON
// formats hand-parsed beside the 5/8 manifest engine that already understood
// both — and four other row kinds it could not express at all. These tests
// cover the cutover: what a package can now declare, and that the apply engine's
// transaction came with it.

// pkgChildRun is the re-entry point of runPkgExpectingDeath. cmdPackages
// refuses by calling die(), which exits the process, so a refusal can only be
// observed from a second one. Called first in a test that uses it, before any
// setup: in the child the setup has already been done by the parent, whose temp
// dirs and ARIZUKO_DATA_DIR the child inherits.
func pkgChildRun() {
	packed := os.Getenv("ARIZUKO_PKG_ARGS")
	if packed == "" {
		return
	}
	cmdPackages(strings.Split(packed, "\n"))
	os.Exit(0)
}

// runPkgExpectingDeath runs cmdPackages(args) in a child copy of the test binary
// and returns its exit code.
func runPkgExpectingDeath(t *testing.T, args []string) int {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
	cmd.Env = append(os.Environ(), "ARIZUKO_PKG_ARGS="+strings.Join(args, "\n"))
	out, err := cmd.CombinedOutput()
	t.Logf("child: %s", out)
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	if err != nil {
		t.Fatalf("run child: %v", err)
	}
	return 0
}

// seedPkgInstance creates an instance dir with a migrated routd.db and returns it.
func seedPkgInstance(t *testing.T, base, name string) string {
	t.Helper()
	dataDir := filepath.Join(base, "arizuko_"+name)
	if err := os.MkdirAll(filepath.Join(dataDir, "store"), 0o755); err != nil {
		t.Fatal(err)
	}
	rdb, err := routd.Open()
	if err != nil {
		t.Fatal(err)
	}
	rdb.Close()
	t.Cleanup(func() { dropInstance(t) })
	return dataDir
}

// writePkg writes a package source dir whose fragment is the plainest one that
// installs, for a body that is testing something other than the fragment.
func writePkg(t *testing.T, base, name, manifest string) string {
	t.Helper()
	return writeSourced(t, base, name, "services:\n  "+name+":\n    image: x\n", manifest)
}

// The headline of the cutover: a scheduled task and a network rule are things
// the manifest has always carried and `apply` has always installed, and a
// package could not declare either — there was no `<name>-tasks.json`, and
// adding one was the wrong answer. Both now install from the one manifest.
func TestPackageInstallsTaskAndNetworkRule(t *testing.T) {
	t.Cleanup(func() { dropInstance(t) })
	base := t.TempDir()
	t.Setenv("ARIZUKO_DATA_DIR", base)
	src := writePkg(t, base, "tnpkg",
		"network_rules:\n"+
			"  - folder: ''\n    target: api.example.com\n"+
			"scheduled_tasks:\n"+
			"  - id: tnpkg-nightly\n    owner: tnpkg\n    chat_jid: web:ops\n"+
			"    prompt: run the nightly report\n    cron: '0 3 * * *'\n")

	cmdPackages([]string{"tn", "install", src})

	rdb, err := routd.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer rdb.Close()
	rst := store.NewOver(rdb.Obj())
	if n := ruleCount(t, store.NewOver(rdb.Obj()), "", "api.example.com"); n != 1 {
		t.Fatalf("package network rule not installed: %d rules name api.example.com", n)
	}
	task, ok, err := rst.TaskByID("tnpkg-nightly")
	if err != nil || !ok {
		t.Fatalf("package scheduled task not installed: %v", err)
	}
	if task.Prompt != "run the nightly report" {
		t.Fatalf("task prompt = %q", task.Prompt)
	}
	rec, ok, _ := rdb.InstalledPackage(routd.InstanceWide, "tnpkg")
	if !ok || len(rec.Manifest["network_rules"]) != 1 || len(rec.Manifest["scheduled_tasks"]) != 1 {
		t.Fatalf("record does not own both row kinds: %+v", rec.Manifest)
	}
}

// The transaction is the other half. applyPackageGrants called die() inside its
// loop, so grant 3 of 5 failing left 1 and 2 applied, live, and unrecorded. A
// failing install must now leave NO row of the package behind — the manifest is
// validated whole before the first write, and the write itself is one apply tx.
func TestFailedInstallLeavesNoPartialRows(t *testing.T) {
	t.Cleanup(func() { dropInstance(t) })
	pkgChildRun()
	base := t.TempDir()
	t.Setenv("ARIZUKO_DATA_DIR", base)
	dataDir := seedPkgInstance(t, base, "pf")
	// Row one is valid. Row two carries a predicate on a folder: principal, which
	// only the agent socket can match and it passes no claims — refused, and the
	// refusal must take row one with it.
	src := writePkg(t, base, "partpkg",
		"acl:\n"+
			"  - principal: '@first'\n    action: reply\n    scope: eng/*\n    effect: allow\n"+
			"  - principal: 'folder:eng'\n    action: mcp:send\n    scope: eng\n    effect: allow\n"+
			"    predicate: onbehalf=google:42\n")

	if code := runPkgExpectingDeath(t, []string{"pf", "install", src, "--folder", "eng/**"}); code == 0 {
		t.Fatal("install with an unevaluable grant must fail")
	}
	if _, err := os.Stat(filepath.Join(dataDir, "services", "partpkg.yml")); err == nil {
		t.Error("a failed install left its compose fragment behind")
	}

	rdb, err := routd.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer rdb.Close()
	rows, err := store.NewOver(rdb.Obj()).ListACL("@first")
	if err != nil {
		t.Fatal(err)
	}
	n := len(rows)
	if n != 0 {
		t.Fatalf("acl rows from a failed install = %d, want 0 (die() mid-loop is what L44 fixes)", n)
	}
	if _, ok, _ := rdb.InstalledPackage(routd.InstanceWide, "partpkg"); ok {
		t.Fatal("a failed install left an installed-package record")
	}
}

// The same guarantee for a refusal that lands AFTER the fragment files are on
// disk: the missing-group rule fires against the DB, not the parser. The rows
// never land and the files come back off, so `generate` cannot pick up half a
// package.
func TestFailedInstallUnwindsWrittenFiles(t *testing.T) {
	t.Cleanup(func() { dropInstance(t) })
	pkgChildRun()
	base := t.TempDir()
	t.Setenv("ARIZUKO_DATA_DIR", base)
	dataDir := seedPkgInstance(t, base, "uw")
	src := writePkg(t, base, "latepkg",
		"acl:\n  - principal: '@late'\n    action: reply\n    scope: eng/*\n    effect: allow\n"+
			"web_routes:\n  - path_prefix: /late/\n    access: public\n    folder: nosuchgroup\n")

	if code := runPkgExpectingDeath(t, []string{"uw", "install", src, "--folder", "eng/**"}); code == 0 {
		t.Fatal("install naming a folder that is not a group must fail")
	}

	for _, n := range []string{"latepkg.yml", "latepkg.yaml"} {
		if _, err := os.Stat(filepath.Join(dataDir, "services", n)); err == nil {
			t.Errorf("a refused install left %s behind", n)
		}
	}
	rdb, err := routd.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer rdb.Close()
	rows, err := store.NewOver(rdb.Obj()).ListACL("@late")
	if err != nil {
		t.Fatal(err)
	}
	n := len(rows)
	if n != 0 {
		t.Fatalf("acl rows from a refused install = %d, want 0", n)
	}
}

// Four registered resources — audit, dead_letters, sessions, signing_keys —
// are read-only projections over other tables with no owner DB of their own.
// resreg.ParseManifest accepts any registered key, so a manifest could name one
// and reach the store opener, whose refusal fires after the fragment files are
// on disk and leaves them there for the next `generate` to pick up. The refusal
// belongs beside the SkipApplyRebuild one, on the near side of the first write,
// naming the key and the file (BUGS L199).
func TestInstallRefusesAResourceNoOwnerAppliesBeforeWriting(t *testing.T) {
	t.Cleanup(func() { dropInstance(t) })
	pkgChildRun()
	base := t.TempDir()
	t.Setenv("ARIZUKO_DATA_DIR", base)
	dataDir := seedPkgInstance(t, base, "np")
	src := writePkg(t, base, "projpkg", "sessions:\n  - familyid: x\n    sub: u\n")

	if code := runPkgExpectingDeath(t, []string{"np", "install", src}); code == 0 {
		t.Fatal("install of a manifest naming a resource no owner applies must fail")
	}
	for _, n := range []string{"projpkg.yml", "projpkg.yaml"} {
		if _, err := os.Stat(filepath.Join(dataDir, "services", n)); err == nil {
			t.Errorf("a refused install left %s behind", n)
		}
	}
	rdb, err := routd.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer rdb.Close()
	if _, ok, _ := rdb.InstalledPackage(routd.InstanceWide, "projpkg"); ok {
		t.Error("a refused install left an installed-package record")
	}
}

// Remove reverses exactly what the record names and nothing else: a route an
// operator added by hand at another path, and a grant that predates the
// package, both survive. Apply rebuilds a resource wholesale, so this is the
// property the merge-onto-the-live-projection model has to buy back.
func TestRemoveReversesOnlyRecordedRows(t *testing.T) {
	t.Cleanup(func() { dropInstance(t) })
	base := t.TempDir()
	t.Setenv("ARIZUKO_DATA_DIR", base)

	rdb0, err := routd.Open()
	if err != nil {
		t.Fatal(err)
	}
	st0 := store.NewOver(rdb0.Obj())
	if _, _, err := st0.SyncProxydRoutes([]store.ProxydRoute{
		{Path: "/mine/", Backend: "http://mine:8080", Auth: "user", Owner: store.RouteOwnerOperator}}); err != nil {
		t.Fatal(err)
	}
	if err := st0.AddACLRow(core.ACLRow{
		Principal: "@preexisting", Action: "reply", Scope: "eng/*", Effect: "allow"}); err != nil {
		t.Fatal(err)
	}
	rdb0.Close()

	src := writePkg(t, base, "rvpkg",
		"proxyd_routes:\n  - path: /rv/\n    backend: http://rvpkg:8080\n    auth: public\n"+
			"acl:\n  - principal: '@rv'\n    action: reply\n    scope: eng/*\n    effect: allow\n")
	cmdPackages([]string{"rv", "install", src, "--folder", "eng/**"})
	cmdPackages([]string{"rv", "remove", "rvpkg"})

	rdb, err := routd.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer rdb.Close()
	st := store.NewOver(rdb.Obj())
	routes, err := st.AllProxydRoutes()
	if err != nil {
		t.Fatal(err)
	}
	var mine, rv bool
	for _, r := range routes {
		switch r.Path {
		case "/mine/":
			mine = true
			if r.Owner != store.RouteOwnerOperator {
				t.Errorf("remove rewrote the operator's route owner: %+v", r)
			}
		case "/rv/":
			rv = true
		}
	}
	if !mine {
		t.Error("remove deleted a route the record never named")
	}
	if rv {
		t.Error("remove left the package's own route behind")
	}
	pre, err := st.ListACL("@preexisting")
	if err != nil {
		t.Fatal(err)
	}
	if len(pre) != 1 {
		t.Errorf("remove deleted a grant the record never named: %d rows left", len(pre))
	}
	own, err := st.ListACL("@rv")
	if err != nil {
		t.Fatal(err)
	}
	if len(own) != 0 {
		t.Errorf("remove left the package's own grant behind: %+v", own)
	}
}

// acl has no folder scope, so apply rebuilds it wholesale — every column the
// manifest row type omits comes back as its DEFAULT. grant_option (spec 5/33-paths-roles)
// was omitted, so routing the package install through apply would have quietly
// revoked every holder's right to re-delegate. The row type carries all nine
// columns now; this is the guard that keeps it that way.
func TestPackageInstallPreservesGrantOption(t *testing.T) {
	t.Cleanup(func() { dropInstance(t) })
	base := t.TempDir()
	t.Setenv("ARIZUKO_DATA_DIR", base)

	rdb0, err := routd.Open()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.NewOver(rdb0.Obj()).AddACLRow(core.ACLRow{
		Principal: "@delegator", Action: "admin", Scope: "eng/*", Effect: "allow", GrantOption: true}); err != nil {
		t.Fatal(err)
	}
	rdb0.Close()

	src := writePkg(t, base, "gopkg",
		"acl:\n  - principal: '@other'\n    action: reply\n    scope: ops\n    effect: allow\n")
	cmdPackages([]string{"go2", "install", src, "--folder", "ops"})

	rdb, err := routd.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer rdb.Close()
	rows, err := store.NewOver(rdb.Obj()).ListACL("@delegator")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("acl rows for @delegator = %d, want 1", len(rows))
	}
	if !rows[0].GrantOption {
		t.Fatal("installing a package cleared WITH GRANT OPTION on an unrelated grant")
	}
}

// The second reader of the one declaration: compose generation needs the routes
// before any daemon or DB exists, so it reads the same `<name>.yaml` the install
// applies. One declaration, two readers — not two formats (BUGS L44).
func TestGenerateReadsRoutesFromPackageManifest(t *testing.T) {
	base := t.TempDir()
	t.Setenv("ARIZUKO_DATA_DIR", base)
	dataDir := seedPkgInstance(t, base, "gr2")
	if err := os.WriteFile(filepath.Join(dataDir, ".env"), []byte("ASSISTANT_NAME=t\nWEB_PORT=8095\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := writePkg(t, base, "genpkg",
		"proxyd_routes:\n  - path: /gen/\n    backend: http://genpkg:8080\n    auth: public\n    strip_prefix: true\n")
	cmdPackages([]string{"gr2", "install", src})

	_, routes, err := compose.Generate(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range routes {
		if r.Path == "/gen/" {
			if r.Backend != "http://genpkg:8080" || !r.StripPrefix {
				t.Fatalf("route lost fields on the way through the manifest: %+v", r)
			}
			return
		}
	}
	t.Fatalf("generate did not emit the package's route; got %+v", routes)
}

// A scoped resource derives the folders it DELETEs from the rows the document
// still mentions. So withdrawing the LAST row in a folder made that folder
// vanish from the document, and apply deleted nothing there — `packages remove`
// reported success and left a live egress rule behind. Caught by the lab, not by
// go test, which is why this exists.
//
// The fix feeds the pre-drop projection's scopes into ApplyOpts.PruneScopes.
// Revert that and this test fails while every other remove test still passes,
// because acl and proxyd_routes are instance-global and never had the problem.
func TestRemoveWithdrawsTheLastRowInAScope(t *testing.T) {
	t.Cleanup(func() { dropInstance(t) })
	base := t.TempDir()
	t.Setenv("ARIZUKO_DATA_DIR", base)

	// The rule targets folder `main`, so the group must exist — apply's
	// preflight refuses a folder that is not a group.
	rdb0, err := routd.Open()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.NewOver(rdb0.Obj()).PutGroup(core.Group{Folder: "main"}); err != nil {
		t.Fatal(err)
	}
	rdb0.Close()

	src := writePkg(t, base, "scpkg",
		"network_rules:\n  - folder: main\n    target: scpkg.example.com\n")
	cmdPackages([]string{"sc", "install", src})

	count := func(t *testing.T) int {
		t.Helper()
		rdb, err := routd.Open()
		if err != nil {
			t.Fatal(err)
		}
		defer rdb.Close()
		n := ruleCount(t, store.NewOver(rdb.Obj()), "", "scpkg.example.com")
		return n
	}

	if got := count(t); got != 1 {
		t.Fatalf("install did not write the egress rule: %d rows", got)
	}
	cmdPackages([]string{"sc", "remove", "scpkg"})
	if got := count(t); got != 0 {
		t.Errorf("remove left the package's only rule in folder main: %d rows still live", got)
	}
}

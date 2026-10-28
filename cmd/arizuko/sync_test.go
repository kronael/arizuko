package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/store"
)

// TestPackageChild is the child of packageRuns and packageRefuses: cmdPackage
// ends a refusal with os.Exit, so a test that expects one runs it here.
func TestPackageChild(t *testing.T) {
	if args := os.Getenv("ARIZUKO_PKG_ARGS"); args != "" {
		cmdPackage(strings.Split(args, "\x1f"))
	}
}

func packageChild(t *testing.T, instance string, args []string) (string, error) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestPackageChild$")
	cmd.Env = append(os.Environ(), "ARIZUKO_PKG_ARGS="+strings.Join(append([]string{instance}, args...), "\x1f"))
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func packageRuns(t *testing.T, instance string, args ...string) {
	t.Helper()
	if output, err := packageChild(t, instance, args); err != nil {
		t.Fatalf("package %v failed: %s (%v)", args, output, err)
	}
}

// packageRefuses runs the verb and answers what it printed; a verb that
// succeeds fails the test.
func packageRefuses(t *testing.T, instance string, args ...string) string {
	t.Helper()
	output, err := packageChild(t, instance, args)
	if err == nil {
		t.Fatalf("package %v accepted: %s", args, output)
	}
	return output
}

// seedPackageInstance builds an instance with its tables, the group main and
// a catalog; the core package declares one egress rule.
func seedPackageInstance(t *testing.T, name string, catalog map[string]string) (string, *routd.DB) {
	t.Helper()
	dataDir := seedSyncInstance(t, name, catalog, nil)
	seedRoutdDB(t, dataDir)
	mustWrite(t, filepath.Join(os.Getenv("APP_SRC_DIR"), "ant", "arizuko.yml"),
		"name: core\nnetwork_rules:\n  - folder: ''\n    target: core.example\n")
	h, err := store.Handle(store.OwnerOnbod)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PrepareOwner(context.Background(), h, store.OwnerOnbod); err != nil {
		t.Fatal(err)
	}
	if err := resreg.EnsureSubsystem(context.Background(), h, h, resreg.SubsystemOnbod); err != nil {
		t.Fatal(err)
	}
	db := mustOpenRoutd()
	t.Cleanup(func() { _ = db.Close() })
	if err := store.NewOver(db.Obj()).PutGroup(core.Group{Folder: "main"}); err != nil {
		t.Fatal(err)
	}
	return dataDir, db
}

func taskCount(st *store.Store, id string) int {
	if _, ok := st.GetTask(id); ok {
		return 1
	}
	return 0
}

func selection(t *testing.T, db *routd.DB) map[string]routd.InstalledPackage {
	t.Helper()
	records, err := db.InstalledPackages()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]routd.InstalledPackage{}
	for _, record := range records {
		out[record.Folder+"/"+record.Name] = record
	}
	return out
}

func TestPackageAddSyncRm(t *testing.T) {
	dataDir, db := seedPackageInstance(t, "pkg", map[string]string{
		"demo.yml":          "services:\n  demo:\n    image: x\n",
		"other/arizuko.yml": "name: other\nnetwork_rules:\n  - folder: main\n    target: other.example\n",
		"demo/arizuko.yml":  "name: demo\nenv:\n  DEMO_KEY: {generate: true}\nnetwork_rules:\n  - folder: ''\n    target: one.example\nproxyd_routes:\n  - path: /demo/\n    backend: http://demo:8080\n    auth: public\n",
	})
	st := store.NewOver(db.Obj())
	group := filepath.Join(t.TempDir(), "grp")
	mustWrite(t, filepath.Join(group, "arizuko.yml"), "name: grp\nnetwork_rules:\n  - folder: main\n    target: grp.example\n"+
		"scheduled_tasks:\n  - id: grp-task\n    owner: main\n    chat_jid: 'local:main'\n    prompt: tick\n    cron: '0 * * * *'\n")
	mustWrite(t, filepath.Join(group, "grp.yml"), "services:\n  grp:\n    image: x\n")

	packageRuns(t, "pkg", "sync")
	if _, found, err := db.InstalledPackage("", "core"); err != nil || !found || ruleCount(t, st, "", "core.example") != 1 {
		t.Fatalf("core selection: found=%v, %v", found, err)
	}
	packageRuns(t, "pkg", "add", "catalog:demo")
	packageRuns(t, "pkg", "add", "catalog:other")
	packageRuns(t, "pkg", "add", "-g", "main", group)
	env, err := os.ReadFile(filepath.Join(dataDir, ".env"))
	if err != nil || !strings.Contains(string(env), "DEMO_KEY=") {
		t.Fatalf("host env %q: %v", env, err)
	}
	if ruleCount(t, st, "", "one.example") != 1 || ruleCount(t, st, "main", "other.example") != 1 ||
		ruleCount(t, st, "main", "grp.example") != 1 || taskCount(st, "grp-task") != 1 {
		t.Fatal("package rows not installed")
	}
	if _, err := os.Stat(filepath.Join(dataDir, "services", "grp.yml")); err != nil {
		t.Fatalf("group package service: %v", err)
	}
	routes, err := st.AllProxydRoutes()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(routes, func(r store.ProxydRoute) bool {
		return r.Path == "/demo/" && r.Owner == store.RouteOwnerPackage("demo")
	}) {
		t.Fatalf("package route missing: %+v", routes)
	}
	selected := selection(t, db)
	if len(selected) != 4 || selected["/demo"].InstalledAt >= selected["/other"].InstalledAt || selected["/other"].InstalledAt >= selected["main/grp"].InstalledAt {
		t.Fatalf("selection is not in the order of the adds: %+v", selected)
	}

	packageRuns(t, "pkg", "sync")
	if again := selection(t, db); !reflect.DeepEqual(again, selected) {
		t.Fatalf("a repeat sync changed the receipts:\n%+v\n%+v", selected, again)
	}

	mustWrite(t, filepath.Join(os.Getenv("HOST_APP_DIR"), "template", "services", "demo", "arizuko.yml"), "name: demo\ninstall: exit 1\n")
	if output := packageRefuses(t, "pkg", "sync"); !strings.Contains(output, "build") {
		t.Fatalf("failed build output: %s", output)
	}
	if after := selection(t, db); !reflect.DeepEqual(after, selected) || ruleCount(t, st, "", "one.example") != 1 {
		t.Fatal("a failed build changed receipts or withdrew the old row")
	}
	mustWrite(t, filepath.Join(os.Getenv("HOST_APP_DIR"), "template", "services", "demo", "arizuko.yml"), "name: demo\nnetwork_rules:\n  - folder: ''\n    target: two.example\n")
	packageRuns(t, "pkg", "sync")
	if ruleCount(t, st, "", "one.example") != 0 || ruleCount(t, st, "", "two.example") != 1 || ruleCount(t, st, "main", "other.example") != 1 {
		t.Fatal("package update did not replace rows")
	}
	if routes, err = st.AllProxydRoutes(); err != nil || slices.ContainsFunc(routes, func(r store.ProxydRoute) bool { return r.Path == "/demo/" }) {
		t.Fatalf("package update retained the withdrawn route: %+v, %v", routes, err)
	}
	if updated := selection(t, db); updated["/demo"].InstalledAt != selected["/demo"].InstalledAt {
		t.Fatal("a sync moved the package in the selection order")
	}

	packageRuns(t, "pkg", "rm", "-g", "main", "grp")
	if ruleCount(t, st, "main", "grp.example") != 0 || taskCount(st, "grp-task") != 0 {
		t.Fatal("rm left the group package's rows")
	}
	if _, err := os.Stat(filepath.Join(dataDir, "services", "grp.yml")); !os.IsNotExist(err) {
		t.Fatalf("rm left the group package's service: %v", err)
	}
	if ruleCount(t, st, "main", "other.example") != 1 {
		t.Fatal("rm of one package withdrew another's row")
	}
	packageRuns(t, "pkg", "rm", "other")
	packageRuns(t, "pkg", "rm", "demo")
	if ruleCount(t, st, "main", "other.example") != 0 || ruleCount(t, st, "", "two.example") != 0 || ruleCount(t, st, "", "core.example") != 1 {
		t.Fatal("rm left rows or withdrew core's")
	}
	if left := selection(t, db); len(left) != 1 || left["/core"].Name != "core" {
		t.Fatalf("selection after rm: %v", left)
	}
	auditRows := aclAuditRows(t, st, "config.apply")
	if last := auditRows[len(auditRows)-1][2]; !strings.Contains(last, "two.example") || !strings.Contains(last, "demo") {
		t.Fatalf("withdrawal audit missing package or row: %v", auditRows)
	}
}

// A group package ships services and rows like an instance package; withdrawing
// one scope keeps what the other scope still ships.
func TestPackageRmKeepsWhatAnotherScopeShips(t *testing.T) {
	dataDir, db := seedPackageInstance(t, "shared", nil)
	st := store.NewOver(db.Obj())
	source := filepath.Join(t.TempDir(), "shared")
	counter := filepath.Join(t.TempDir(), "counter")
	t.Setenv("INSTALL_COUNTER", counter)
	mustWrite(t, filepath.Join(source, "arizuko.yml"), "name: shared\ninstall: |\n  echo run >> \"$INSTALL_COUNTER\"\n  mkdir -p \"$PREFIX/bin\"\n  printf '#!/bin/sh\\necho built\\n' > \"$PREFIX/bin/tool\"\n  chmod +x \"$PREFIX/bin/tool\"\nnetwork_rules:\n  - folder: ''\n    target: shared.example\n")
	mustWrite(t, filepath.Join(source, "shared.yml"), "services:\n  shared:\n    image: x\n")
	mustWrite(t, filepath.Join(source, "skills", "one", "SKILL.md"), "skill")
	for _, scope := range [][]string{{}, {"-g", "main"}} {
		packageRuns(t, "shared", append(append([]string{"add"}, scope...), source)...)
	}
	for _, folder := range []string{"", "main"} {
		if _, found, err := db.InstalledPackage(folder, "shared"); err != nil || !found {
			t.Fatalf("receipt %q: %v, %v", folder, found, err)
		}
	}
	builds := func() int {
		count, err := os.ReadFile(counter)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Count(string(count), "run")
	}
	before := builds()
	packageRuns(t, "shared", "sync")
	if got := builds(); got != before+1 {
		t.Fatalf("a source selected in two scopes built %d times in one run", got-before)
	}
	output, err := exec.Command(filepath.Join(dataDir, "packages", "shared", "bin", "tool")).Output()
	if err != nil || string(output) != "built\n" {
		t.Fatalf("tool output %q: %v", output, err)
	}
	service := filepath.Join(dataDir, "services", "shared.yml")
	packageRuns(t, "shared", "rm", "-g", "main", "shared")
	if _, err := os.Stat(service); err != nil || ruleCount(t, st, "", "shared.example") != 1 {
		t.Fatalf("rm in one scope removed what the instance copy ships: %v", err)
	}
	packageRuns(t, "shared", "rm", "shared")
	if _, err := os.Stat(service); !os.IsNotExist(err) || ruleCount(t, st, "", "shared.example") != 0 {
		t.Fatalf("rm of the last copy left the service or the row: %v", err)
	}
}

// Only the installed_packages rows select. The group folder is the agent's own,
// so nothing written under it may start a build on the host (BUGS L630).
func TestPackageSelectionIgnoresGroupFiles(t *testing.T) {
	dataDir, db := seedPackageInstance(t, "l630", nil)
	marker := filepath.Join(t.TempDir(), "ran")
	t.Setenv("L630_MARKER", marker)
	evil := filepath.Join(t.TempDir(), "evil")
	mustWrite(t, filepath.Join(evil, "arizuko.yml"), "name: evil\ninstall: touch \"$L630_MARKER\"\n")
	for _, declaration := range []string{
		"packages:\n  - source: " + evil + "\n",
		"name: evil\ninstall: touch \"$L630_MARKER\"\n",
	} {
		mustWrite(t, filepath.Join(dataDir, "groups", "main", "arizuko.yml"), declaration)
		mustWrite(t, filepath.Join(dataDir, "arizuko.yml"), declaration)
		packageRuns(t, "l630", "sync")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("a group declaration started a build: %v", err)
	}
	if _, found, err := db.InstalledPackage("main", "evil"); err != nil || found {
		t.Fatalf("a group declaration selected a package: %v, %v", found, err)
	}
	packageRuns(t, "l630", "add", "-g", "main", evil)
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the operator's add did not build: %v", err)
	}
}

func TestPackageRefusals(t *testing.T) {
	fragment, err := os.ReadFile("../../template/services/extenddb/extenddb.yml")
	if err != nil {
		t.Fatal(err)
	}
	dataDir, db := seedPackageInstance(t, "guard", map[string]string{
		"extenddb.yml":      string(fragment),
		"routd.yml":         "services:\n  routd:\n    image: x\n",
		"guard.yml":         "services:\n  guard:\n    image: x\n",
		"guard/arizuko.yml": "name: guard\nenv:\n  REQUIRED_SERVICESYNC_KEY: {required: true}\nnetwork_rules:\n  - folder: ''\n    target: service.example\n",
	})
	t.Setenv("REQUIRED_SERVICESYNC_KEY", "")
	unsafe := filepath.Join(t.TempDir(), "unsafe")
	for name, test := range map[string]struct {
		rows, want string
	}{
		"acl":            {"acl:\n  - principal: '@ops'\n    action: admin\n    scope: ops\n    effect: allow\n", "never grants"},
		"core route":     {"proxyd_routes:\n  - path: /dash/\n    backend: http://unsafe:8080\n    auth: public\n", "core surface"},
		"owned route":    {"proxyd_routes:\n  - path: /unsafe/\n    backend: http://unsafe:8080\n    auth: public\n    owner: operator\n", "owner"},
		"selection":      {"packages:\n  - source: /elsewhere\n", "packages"},
		"unknown folder": {"network_rules:\n  - folder: nowhere\n    target: a.example\n", "nowhere"},
	} {
		mustWrite(t, filepath.Join(unsafe, "arizuko.yml"), "name: unsafe\n"+test.rows)
		if output := packageRefuses(t, "guard", "add", unsafe); !strings.Contains(output, test.want) {
			t.Fatalf("%s: output %q lacks %q", name, output, test.want)
		}
		assertNothingInstalled(t, dataDir, "unsafe")
	}

	other := filepath.Join(t.TempDir(), "other")
	mustWrite(t, filepath.Join(unsafe, "arizuko.yml"), "name: unsafe\n")
	mustWrite(t, filepath.Join(other, "arizuko.yml"), "name: unsafe\n")
	packageRuns(t, "guard", "add", unsafe)
	for name, test := range map[string]struct {
		args []string
		want string
	}{
		"same name, other source": {[]string{"add", other}, "already uses source"},
		"same source twice":       {[]string{"add", unsafe}, "conflicts with selection"},
		"unknown group":           {[]string{"add", "-g", "nowhere", other}, "unknown group"},
		"unselected":              {[]string{"rm", "other"}, "not selected"},
		"core":                    {[]string{"rm", "core"}, "required"},
		"core service name":       {[]string{"add", "catalog:routd"}, "core service"},
		"no verb":                 {nil, "usage: arizuko package"},
		"unknown verb":            {[]string{"frob"}, "usage: arizuko package"},
		"sync with a group":       {[]string{"sync", "-g", "main"}, "usage: arizuko package"},
		"extra argument":          {[]string{"rm", "unsafe", "extra"}, "usage: arizuko package"},
		"required env":            {[]string{"add", "catalog:guard"}, "REQUIRED_SERVICESYNC_KEY"},
	} {
		if output := packageRefuses(t, "guard", test.args...); !strings.Contains(output, test.want) {
			t.Fatalf("%s: output %q lacks %q", name, output, test.want)
		}
	}
	if _, err := os.Stat(filepath.Join(dataDir, "packages", "guard")); !os.IsNotExist(err) {
		t.Errorf("required service env refused after asset install: %v", err)
	}
	if ruleCount(t, store.NewOver(db.Obj()), "", "service.example") != 0 {
		t.Error("required service env refused after applying the package rows")
	}
	if _, found, err := db.InstalledPackage("", "guard"); err != nil || found {
		t.Errorf("required service env wrote a receipt: %v, %v", found, err)
	}
	if got := len(selection(t, db)); got != 2 {
		t.Fatalf("refusals changed the selection: %d records", got)
	}
}

func TestPackageServiceContainment(t *testing.T) {
	fragment, err := os.ReadFile("../../template/services/extenddb/extenddb.yml")
	if err != nil {
		t.Fatal(err)
	}
	dataDir, db := seedPackageInstance(t, "catalog", map[string]string{"extenddb.yml": string(fragment)})
	service := filepath.Join(dataDir, "services", "extenddb.yml")
	mustWrite(t, service, "operator fragment")
	if output := packageRefuses(t, "catalog", "add", "catalog:extenddb"); !strings.Contains(output, "already there") {
		t.Fatalf("catalog collision accepted: %s", output)
	}
	packageRuns(t, "catalog", "add", "-f", "catalog:extenddb")
	if got, err := os.ReadFile(service); err != nil || string(got) != string(fragment) {
		t.Fatalf("-f did not replace the unmanaged fragment: %q, %v", got, err)
	}
	if _, found, err := db.InstalledPackage("", "extenddb"); err != nil || !found {
		t.Fatalf("catalog receipt: found=%v, %v", found, err)
	}
	packageRuns(t, "catalog", "sync")
	mustWrite(t, service, "edited fragment")
	if output := packageRefuses(t, "catalog", "sync"); !strings.Contains(output, "locally edited") {
		t.Fatalf("dirty catalog fragment accepted: %s", output)
	}
	mustWrite(t, service, string(fragment))

	external := filepath.Join(t.TempDir(), "external")
	mustWrite(t, filepath.Join(external, "arizuko.yml"), "name: external\n")
	mustWrite(t, filepath.Join(external, "external.yml"), strings.ReplaceAll(string(fragment), "extenddb", "external"))
	if output := packageRefuses(t, "catalog", "add", external); !strings.Contains(output, "external_db") {
		t.Fatalf("external fragment accepted: %s", output)
	}
	assertNothingInstalled(t, dataDir, "external")
}

// Two packages ship skills/base, as core does. The overrides follow the order
// the operator added the packages in, whatever their scope, and a group repeats
// no line the instance already printed.
func TestPackageSyncPrintsOverrides(t *testing.T) {
	overrides := func(output string) []string {
		var lines []string
		for _, line := range strings.Split(output, "\n") {
			if strings.Contains(line, " overrides ") {
				lines = append(lines, line)
			}
		}
		return lines
	}
	for _, test := range []struct {
		name string
		adds [][]string
		want []string
	}{
		{"instance first", [][]string{{"catalog:beta"}, {"-g", "main", "catalog:alpha"}}, []string{
			"instance: beta overrides core on skills/base",
			"main: alpha overrides beta on skills/base",
		}},
		{"group first", [][]string{{"-g", "main", "catalog:alpha"}, {"catalog:beta"}}, []string{
			"instance: beta overrides core on skills/base",
			"main: alpha overrides core on skills/base",
			"main: beta overrides alpha on skills/base",
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			skill := "skills/base/SKILL.md"
			seedPackageInstance(t, "over", map[string]string{
				"alpha/arizuko.yml": "name: alpha\n", "alpha/" + skill: "alpha",
				"beta/arizuko.yml": "name: beta\n", "beta/" + skill: "beta",
			})
			var output string
			for _, add := range test.adds {
				var err error
				if output, err = packageChild(t, "over", append([]string{"add"}, add...)); err != nil {
					t.Fatalf("add %v: %s (%v)", add, output, err)
				}
			}
			if got := overrides(output); !slices.Equal(got, test.want) {
				t.Fatalf("add output %q\n got %q\nwant %q", output, got, test.want)
			}
			output, err := packageChild(t, "over", []string{"sync"})
			if err != nil || !slices.Equal(overrides(output), test.want) {
				t.Fatalf("sync output %q: %v", output, err)
			}
		})
	}
}

// Each package sync rewrites the group/ files of the winning package, whatever
// the agent did to them; a file a package stops shipping goes, a file of the
// group's own stays, and rm withdraws what the package wrote.
func TestPackageSyncRewritesGroupFiles(t *testing.T) {
	dataDir, db := seedPackageInstance(t, "payload", nil)
	groupDir := filepath.Join(dataDir, "groups", "main")
	first := filepath.Join(t.TempDir(), "first")
	second := filepath.Join(t.TempDir(), "second")
	mustWrite(t, filepath.Join(first, "arizuko.yml"), "name: first\n")
	mustWrite(t, filepath.Join(first, "group", "PERSONA.md"), "first persona")
	mustWrite(t, filepath.Join(first, "group", "facts", "sla.md"), "four hours")
	mustWrite(t, filepath.Join(second, "arizuko.yml"), "name: second\n")
	mustWrite(t, filepath.Join(second, "group", "PERSONA.md"), "second persona")

	packageRuns(t, "payload", "add", "-g", "main", first)
	mustWrite(t, filepath.Join(groupDir, "mine.md"), "the group's own")
	if output, err := packageChild(t, "payload", []string{"add", "-g", "main", second}); err != nil || !strings.Contains(output, "main: second overrides first on group/PERSONA.md") {
		t.Fatalf("add second: %s (%v)", output, err)
	}
	for rel, want := range map[string]string{"PERSONA.md": "second persona", "facts/sla.md": "four hours", "mine.md": "the group's own"} {
		if got, err := os.ReadFile(filepath.Join(groupDir, filepath.FromSlash(rel))); err != nil || string(got) != want {
			t.Fatalf("%s = %q, %v; want %q", rel, got, err, want)
		}
	}
	selected := selection(t, db)
	if got := selected["main/first"].Manifest[container.ManifestGroupFiles]; !slices.Equal(got, []string{"facts/sla.md"}) {
		t.Fatalf("first receipt lists %v", got)
	}
	if got := selected["main/second"].Manifest[container.ManifestGroupFiles]; !slices.Equal(got, []string{"PERSONA.md"}) {
		t.Fatalf("second receipt lists %v", got)
	}
	for _, record := range selected {
		for key := range record.AssetHashes {
			if strings.Contains(key, "PERSONA") || strings.Contains(key, "sla") {
				t.Fatalf("receipt hashes group payload: %v", record.AssetHashes)
			}
		}
	}

	for _, rel := range []string{"PERSONA.md", "facts/sla.md"} {
		mustWrite(t, filepath.Join(groupDir, filepath.FromSlash(rel)), "agent edit")
	}
	packageRuns(t, "payload", "sync")
	for rel, want := range map[string]string{"PERSONA.md": "second persona", "facts/sla.md": "four hours"} {
		if got, err := os.ReadFile(filepath.Join(groupDir, filepath.FromSlash(rel))); err != nil || string(got) != want {
			t.Fatalf("after sync %s = %q, %v; want %q", rel, got, err, want)
		}
	}

	if err := os.Remove(filepath.Join(first, "group", "facts", "sla.md")); err != nil {
		t.Fatal(err)
	}
	packageRuns(t, "payload", "sync")
	if _, err := os.Stat(filepath.Join(groupDir, "facts", "sla.md")); !os.IsNotExist(err) {
		t.Fatalf("a file no package ships stayed: %v", err)
	}
	packageRuns(t, "payload", "rm", "-g", "main", "second")
	packageRuns(t, "payload", "rm", "-g", "main", "first")
	if _, err := os.Stat(filepath.Join(groupDir, "PERSONA.md")); !os.IsNotExist(err) {
		t.Fatalf("rm left the package's file: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(groupDir, "mine.md")); err != nil || string(got) != "the group's own" {
		t.Fatalf("rm touched the group's own file: %q, %v", got, err)
	}
}

// A file the group already holds is the group's: sync refuses to replace it
// unless -f, and refuses before it writes anything.
func TestPackageSyncRefusesUnmanagedGroupFile(t *testing.T) {
	dataDir, _ := seedPackageInstance(t, "refuse", nil)
	groupDir := filepath.Join(dataDir, "groups", "main")
	source := filepath.Join(t.TempDir(), "source")
	mustWrite(t, filepath.Join(source, "arizuko.yml"), "name: source\n")
	mustWrite(t, filepath.Join(source, "group", "PERSONA.md"), "package persona")
	mustWrite(t, filepath.Join(groupDir, "PERSONA.md"), "mine")

	if output := packageRefuses(t, "refuse", "add", "-g", "main", source); !strings.Contains(output, "PERSONA.md") {
		t.Fatalf("refusal does not name the file: %s", output)
	}
	if got, _ := os.ReadFile(filepath.Join(groupDir, "PERSONA.md")); string(got) != "mine" {
		t.Fatalf("a refused sync replaced the file: %q", got)
	}
	packageRuns(t, "refuse", "add", "-f", "-g", "main", source)
	if got, _ := os.ReadFile(filepath.Join(groupDir, "PERSONA.md")); string(got) != "package persona" {
		t.Fatalf("-f did not replace the file: %q", got)
	}
}

// A group file that predates every package is the group's, even when its bytes
// equal the payload: a refusal leaves the receipts, rows and files as they
// were, a package that stops shipping it never deletes it, and -f makes it the
// package's (BUGS L628).
func TestPackageSyncDoesNotAdoptEqualBytes(t *testing.T) {
	dataDir, db := seedPackageInstance(t, "adopt", nil)
	st := store.NewOver(db.Obj())
	groupDir := filepath.Join(dataDir, "groups", "main")
	source := filepath.Join(t.TempDir(), "source")
	manifest := func(target string) {
		mustWrite(t, filepath.Join(source, "arizuko.yml"), "name: source\nnetwork_rules:\n  - folder: main\n    target: "+target+"\n")
	}
	manifest("one.example")
	mustWrite(t, filepath.Join(source, "group", "facts", "x.md"), "fact")
	mustWrite(t, filepath.Join(source, "group", "PERSONA.md"), "same")
	mustWrite(t, filepath.Join(groupDir, "PERSONA.md"), "same")

	if output := packageRefuses(t, "adopt", "add", "-g", "main", source); !strings.Contains(output, "PERSONA.md") {
		t.Fatalf("refusal does not name the file: %s", output)
	}
	if _, found := selection(t, db)["main/source"]; found || ruleCount(t, st, "main", "one.example") != 0 {
		t.Fatal("a refused add left a receipt or a rule row")
	}
	if _, err := os.Stat(filepath.Join(groupDir, "facts", "x.md")); !os.IsNotExist(err) {
		t.Fatalf("a refused add wrote another file: %v", err)
	}

	if err := os.Remove(filepath.Join(source, "group", "PERSONA.md")); err != nil {
		t.Fatal(err)
	}
	packageRuns(t, "adopt", "add", "-g", "main", source)
	packageRuns(t, "adopt", "sync")
	if got := selection(t, db)["main/source"].Manifest[container.ManifestGroupFiles]; !slices.Equal(got, []string{"facts/x.md"}) {
		t.Fatalf("receipt lists %v", got)
	}
	if got, err := os.ReadFile(filepath.Join(groupDir, "PERSONA.md")); err != nil || string(got) != "same" {
		t.Fatalf("the group's own file = %q, %v", got, err)
	}

	manifest("two.example")
	mustWrite(t, filepath.Join(source, "group", "PERSONA.md"), "same")
	packageRefuses(t, "adopt", "sync")
	if got := selection(t, db)["main/source"].Manifest[container.ManifestGroupFiles]; !slices.Equal(got, []string{"facts/x.md"}) || ruleCount(t, st, "main", "two.example") != 0 {
		t.Fatalf("a refused sync changed the receipt %v or the rules", got)
	}

	packageRuns(t, "adopt", "rm", "-g", "main", "source")
	packageRuns(t, "adopt", "add", "-f", "-g", "main", source)
	if got := selection(t, db)["main/source"].Manifest[container.ManifestGroupFiles]; !slices.Equal(got, []string{"PERSONA.md", "facts/x.md"}) {
		t.Fatalf("-f receipt lists %v", got)
	}
	if err := os.Remove(filepath.Join(source, "group", "PERSONA.md")); err != nil {
		t.Fatal(err)
	}
	packageRuns(t, "adopt", "sync")
	if _, err := os.Stat(filepath.Join(groupDir, "PERSONA.md")); !os.IsNotExist(err) {
		t.Fatalf("a file the package stopped shipping stayed: %v", err)
	}
}

// A group package that ships settings keeps syncing: the receipt kind for the
// merged keys is not a resource, so the next sync does not look it up as one
// (BUGS L633).
func TestPackageSyncGroupSettingsTwice(t *testing.T) {
	dataDir, db := seedPackageInstance(t, "settings", nil)
	source := filepath.Join(t.TempDir(), "source")
	mustWrite(t, filepath.Join(source, "arizuko.yml"), "name: source\n")
	mustWrite(t, filepath.Join(source, "group", ".claude", "settings.json"), `{"outputStyle":"plain"}`)

	packageRuns(t, "settings", "add", "-g", "main", source)
	packageRuns(t, "settings", "sync")
	packageRuns(t, "settings", "sync")
	if got := selection(t, db)["main/source"].Manifest["setting"]; !slices.Equal(got, []string{"outputStyle"}) {
		t.Fatalf("receipt lists settings %v", got)
	}
	if got, err := os.ReadFile(filepath.Join(dataDir, "groups", "main", ".claude", "settings.json")); err != nil || !strings.Contains(string(got), `"outputStyle": "plain"`) {
		t.Fatalf("settings = %q, %v", got, err)
	}
}

// Sync records what this sync wrote: a package that stops shipping its group
// files and settings leaves no stale list in its receipt.
func TestPackageSyncReceiptListsOnlyWhatItWrote(t *testing.T) {
	_, db := seedPackageInstance(t, "stale", nil)
	source := filepath.Join(t.TempDir(), "source")
	mustWrite(t, filepath.Join(source, "arizuko.yml"), "name: source\n")
	mustWrite(t, filepath.Join(source, "group", "facts", "a.md"), "fact")
	mustWrite(t, filepath.Join(source, "group", ".claude", "settings.json"), `{"outputStyle":"plain"}`)

	packageRuns(t, "stale", "add", "-g", "main", source)
	manifest := selection(t, db)["main/source"].Manifest
	if len(manifest[container.ManifestGroupFiles]) == 0 || len(manifest["setting"]) == 0 {
		t.Fatalf("first sync receipt: %v", manifest)
	}
	for _, rel := range []string{"facts/a.md", ".claude/settings.json"} {
		if err := os.Remove(filepath.Join(source, "group", filepath.FromSlash(rel))); err != nil {
			t.Fatal(err)
		}
	}
	packageRuns(t, "stale", "sync")
	manifest = selection(t, db)["main/source"].Manifest
	if len(manifest[container.ManifestGroupFiles]) != 0 || len(manifest["setting"]) != 0 {
		t.Fatalf("receipt keeps what the sync no longer wrote: %v", manifest)
	}
}

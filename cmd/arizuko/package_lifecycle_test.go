package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/store"
)

func unsealPackageFixtures(t *testing.T, dataDir string) {
	t.Helper()
	t.Cleanup(func() {
		for _, name := range []string{"packages", "package-selections"} {
			err := filepath.WalkDir(filepath.Join(dataDir, name), func(path string, entry os.DirEntry, err error) error {
				if os.IsNotExist(err) {
					return nil
				}
				if err != nil {
					return err
				}
				if entry.IsDir() {
					return os.Chmod(path, 0o755)
				}
				return nil
			})
			if err != nil {
				t.Error(err)
			}
		}
	})
}

func selectedSkill(t *testing.T, dataDir, target, packageName, skill string) string {
	t.Helper()
	lock, err := container.LockPackages(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	selected, err := container.ReadPackageSelection(dataDir, target)
	if err != nil {
		t.Fatal(err)
	}
	for _, snapshot := range selected {
		if snapshot.Name == packageName {
			dir, err := container.ResolvePackage(dataDir, snapshot)
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(dir, "skills", skill, "SKILL.md"))
			if err != nil {
				t.Fatal(err)
			}
			return string(data)
		}
	}
	t.Fatalf("package %s is not selected for %s", packageName, target)
	return ""
}

// Local and Git skills-only sources select immutable bytes, upgrade, and withdraw without copying skills.
func TestPackageLifecycleSkillsOnly(t *testing.T) {
	for _, sourceKind := range []string{"local", "git"} {
		t.Run(sourceKind, func(t *testing.T) {
			dataDir, base := mixInstance(t, nil, nil)
			source := filepath.Join(base, "unrelated-directory")
			mustWrite(t, filepath.Join(source, "arizuko.toml"), "name = \"tools\"\n")
			mustWrite(t, filepath.Join(source, "skills", "probe", "SKILL.md"), "v1")
			origin := source
			if sourceKind == "git" {
				runLifecycleGit(t, source, "init")
				runLifecycleGit(t, source, "add", ".")
				runLifecycleGit(t, source, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "commit", "-m", "first")
				origin = "git+file://" + source
			}
			cmdPackages([]string{"t", "install", origin})
			if got := selectedSkill(t, dataDir, "", "tools", "probe"); got != "v1" {
				t.Fatal(got)
			}
			for _, path := range []string{filepath.Join(dataDir, "skills", "probe"), filepath.Join(dataDir, "groups", "main", ".claude", "skills", "probe")} {
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatalf("package flattened into %s: %v", path, err)
				}
			}
			mustWrite(t, filepath.Join(source, "skills", "probe", "SKILL.md"), "v2")
			if sourceKind == "git" {
				runLifecycleGit(t, source, "add", ".")
				runLifecycleGit(t, source, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "commit", "-m", "second")
			}
			if got := selectedSkill(t, dataDir, "", "tools", "probe"); got != "v1" {
				t.Fatalf("source changed active bytes: %q", got)
			}
			cmdPackages([]string{"t", "upgrade", "tools"})
			if got := selectedSkill(t, dataDir, "", "tools", "probe"); got != "v2" {
				t.Fatal(got)
			}
			cmdPackages([]string{"t", "remove", "tools"})
			cmdPackages([]string{"t", "sync"})
			selected, err := container.ReadPackageSelection(dataDir, "")
			if err != nil || len(selected) != 0 {
				t.Fatalf("removed package resurrected: %+v, %v", selected, err)
			}
		})
	}
}

func runLifecycleGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v, %s", args, err, out)
	}
}

// An exact group removal preserves metadata, comments, and the other ordered selection through sync.
func TestPackageLifecycleGroupSelection(t *testing.T) {
	dataDir, base := mixInstance(t, map[string]map[string]string{
		"one": {"arizuko.toml": "name = \"one\"\n", "skills/first/SKILL.md": "first", "PERSONA.md": "first persona"},
		"two": {"arizuko.toml": "name = \"two\"\n", "skills/second/SKILL.md": "second", "PERSONA.md": "second persona"},
	}, []string{"one", "two"})
	path := filepath.Join(dataDir, "groups", "main", "arizuko.toml")
	first := filepath.Join(base, "src", "one")
	second := filepath.Join(base, "src", "two")
	prefix := "# mine\nname = \"unrelated\"\ncustom = { owner = \"me\" }\n"
	entry := "[[product]] # second header\nsource = \"" + second + "\" # second source\n"
	suffix := "\n[extension]\nkeep = [1, 2]\n"
	body := prefix + "[[product]] # first header\nsource = \"" + first + "\" # first source\n" + entry + suffix
	mustWrite(t, path, body)
	cmdProducts([]string{"t", "apply", "main"})
	cmdPackages([]string{"t", "remove", "one", "--group", "main"})
	cmdPackages([]string{"t", "sync"})
	selected, err := container.ReadPackageSelection(dataDir, "main")
	if err != nil || len(selected) != 1 || selected[0].Name != "two" {
		t.Fatalf("removed entry resurrected: %+v, %v", selected, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.HasPrefix(string(data), prefix) || !strings.Contains(string(data), entry+suffix) ||
		!strings.Contains(string(data), "# first header") || !strings.Contains(string(data), "# first source") || strings.Contains(string(data), first) {
		t.Fatalf("lossy selection edit: %q, %v", data, err)
	}
	if got := readGroupFile(t, dataDir, "main", "PERSONA.md"); got != "first persona" {
		t.Fatalf("removal changed group-owned persona: %q", got)
	}
	if got := selectedSkill(t, dataDir, "main", "two", "second"); got != "second" {
		t.Fatal(got)
	}
	if other, err := container.ReadPackageSelection(dataDir, "main/child"); err != nil || len(other) != 0 {
		t.Fatalf("selection inherited by child: %+v, %v", other, err)
	}
}

// Missing sources and skill collisions refuse before payload or active membership changes.
func TestPackageLifecyclePreflight(t *testing.T) {
	dataDir, base := mixInstance(t, map[string]map[string]string{
		"one": {"arizuko.toml": "name = \"one\"\n", "skills/probe/SKILL.md": "v1", "PERSONA.md": "original"},
	}, []string{"one"})
	cmdProducts([]string{"t", "apply", "main"})
	before := treeHashes(t, filepath.Join(dataDir, "groups", "main"))
	selected, err := container.ReadPackageSelection(dataDir, "main")
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(base, "src", "one")
	if err := os.Rename(source, source+"-absent"); err != nil {
		t.Fatal(err)
	}
	if out, err := runManifestCLI(t, []string{"products", "t", "apply", "main"}); err == nil || !strings.Contains(out, "prepare") {
		t.Fatalf("missing source accepted: %v, %s", err, out)
	}
	if after, err := container.ReadPackageSelection(dataDir, "main"); err != nil || !reflect.DeepEqual(after, selected) {
		t.Fatalf("failed preflight changed selection: %+v, %v", after, err)
	}
	if after := treeHashes(t, filepath.Join(dataDir, "groups", "main")); !reflect.DeepEqual(before, after) {
		t.Fatal("source failure changed payload")
	}
	if err := os.Rename(source+"-absent", source); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(base, "other")
	mustWrite(t, filepath.Join(other, "arizuko.toml"), "name = \"other\"\n")
	mustWrite(t, filepath.Join(other, "skills", "probe", "SKILL.md"), "collision")
	if out, err := runManifestCLI(t, []string{"packages", "t", "install", other, "--group", "main"}); err == nil || !strings.Contains(out, "skill") {
		t.Fatalf("collision accepted: %v, %s", err, out)
	}
	if after := treeHashes(t, filepath.Join(dataDir, "groups", "main")); !reflect.DeepEqual(before, after) {
		t.Fatal("collision changed group payload or declaration")
	}
}

type packageReceiptFailure struct {
	objstore.Store
	fail               bool
	allow              int
	activationObstacle string
}

func (failure *packageReceiptFailure) GuardedBatch(ctx context.Context, checks []objstore.Check, writes []objstore.Write) (bool, error) {
	for _, write := range writes {
		if failure.fail && write.Entity == store.InstalledPackages {
			if failure.allow > 0 {
				failure.allow--
				continue
			}
			return false, errors.New("injected receipt failure")
		}
	}
	applied, err := failure.Store.GuardedBatch(ctx, checks, writes)
	if err == nil && applied && failure.activationObstacle != "" {
		err = os.WriteFile(failure.activationObstacle, []byte("fixture activation failure"), 0o644)
	}
	return applied, err
}

// A failed receipt leaves selected bytes visible, reports incompleteness, and repairs without replacing the active link.
func TestPackageLifecycleReceiptRepair(t *testing.T) {
	dataDir, base := mixInstance(t, nil, nil)
	source := filepath.Join(base, "source")
	mustWrite(t, filepath.Join(source, "arizuko.toml"), "name = \"tools\"\n")
	mustWrite(t, filepath.Join(source, "skills", "probe", "SKILL.md"), "v1")
	rdb := mustOpenRoutd()
	defer rdb.Close()
	failure := &packageReceiptFailure{Store: rdb.Obj()}
	db := routd.OpenOver(failure)
	lock := lockPackageOperation(dataDir)
	defer lock.Close()
	candidate, err := preparePackageCandidate(dataDir, source, source, "local")
	if err != nil {
		t.Fatal(err)
	}
	if err := installManagedPackage(dataDir, db, candidate, nil); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(source, "skills", "probe", "SKILL.md"), "v2")
	candidate, err = preparePackageCandidate(dataDir, source, source, "local")
	if err != nil {
		t.Fatal(err)
	}
	failure.fail = true
	err = installManagedPackage(dataDir, db, candidate, nil)
	if err == nil || !strings.Contains(err.Error(), "incomplete") || !strings.Contains(err.Error(), "active") {
		t.Fatalf("receipt failure hidden: %v", err)
	}
	selected, err := container.ReadPackageSelection(dataDir, "")
	if err != nil || !reflect.DeepEqual(selected, []container.PackageSnapshot{candidate.snapshot}) {
		t.Fatalf("receipt failure rolled back files: %+v, %v", selected, err)
	}
	if _, _, err := existingPackageCandidates(dataDir, "", rdb); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("stale receipt reported current: %v", err)
	}
	active := filepath.Join(dataDir, "package-active", "system")
	before, err := os.Lstat(active)
	if err != nil {
		t.Fatal(err)
	}
	failure.fail = false
	if err := installManagedPackage(dataDir, db, candidate, nil); err != nil {
		t.Fatal(err)
	}
	after, err := os.Lstat(active)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("receipt repair replaced active selection: %v", err)
	}
	if _, _, err := existingPackageCandidates(dataDir, "", rdb); err != nil {
		t.Fatal(err)
	}
}

// Selection edits replace only a source value and refuse inline-array forms without changing bytes.
func TestPackageLifecycleSelectionEdits(t *testing.T) {
	data := []byte("# root\nname='mine'\n[[product]] # header\nsource='old' # source\nextra='keep'\n[custom]\nvalue=42\n")
	updated, err := editProductSelection(data, 0, "new")
	if err != nil || string(updated) != strings.Replace(string(data), "source='old'", "source=\"new\"", 1) {
		t.Fatalf("source replacement lost unrelated bytes: %q, %v", updated, err)
	}
	for _, data := range [][]byte{data, []byte("product = [{source='old'}]\n"), []byte("product=[]\n")} {
		if _, err := editProductSelection(data, 0, ""); err == nil {
			t.Fatalf("unsupported removal accepted: %s", data)
		}
	}
}

// A named group upgrade leaves the other active source unchanged; install replaces only its own source value.
func TestPackageLifecycleGroupInstallUpgrade(t *testing.T) {
	dataDir, base := mixInstance(t, map[string]map[string]string{
		"one": {"arizuko.toml": "name='one'\n", "skills/first/SKILL.md": "first v1"},
		"two": {"arizuko.toml": "name='two'\n", "skills/second/SKILL.md": "second v1"},
	}, []string{"one", "two"})
	cmdProducts([]string{"t", "apply", "main"})
	mustWrite(t, filepath.Join(base, "src", "one", "skills", "first", "SKILL.md"), "first v2")
	mustWrite(t, filepath.Join(base, "src", "two", "skills", "second", "SKILL.md"), "second v2")
	cmdPackages([]string{"t", "upgrade", "one", "--group", "main"})
	if got := selectedSkill(t, dataDir, "main", "one", "first"); got != "first v2" {
		t.Fatal(got)
	}
	if got := selectedSkill(t, dataDir, "main", "two", "second"); got != "second v1" {
		t.Fatalf("named upgrade changed unrelated package: %q", got)
	}
	path := filepath.Join(dataDir, "groups", "main", "arizuko.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := "# operator\ncustom='keep'\n" + strings.Replace(string(data), "[[product]]", "[[product]] # entry", 1)
	mustWrite(t, path, body)
	source := filepath.Join(base, "replacement")
	mustWrite(t, filepath.Join(source, "arizuko.toml"), "name='one'\n")
	mustWrite(t, filepath.Join(source, "skills", "first", "SKILL.md"), "replacement")
	cmdPackages([]string{"t", "install", source, "--group", "main", "--folder", "main/**"})
	updated, err := os.ReadFile(path)
	want := strings.Replace(body, filepath.Join(base, "src", "one"), source, 1)
	if err != nil || string(updated) != want {
		t.Fatalf("install changed unrelated manifest bytes: %q, %v", updated, err)
	}
	if got := selectedSkill(t, dataDir, "main", "one", "first"); got != "replacement" {
		t.Fatal(got)
	}
	if system, err := container.ReadPackageSelection(dataDir, ""); err != nil || len(system) != 0 {
		t.Fatalf("--folder changed package target: %+v, %v", system, err)
	}
}

// A manifest package keeps repeatable grant bounds independent of its system target and removes the applied ACL row.
func TestPackageLifecycleGrantBounds(t *testing.T) {
	dataDir, base := mixInstance(t, nil, nil)
	source := filepath.Join(base, "source")
	mustWrite(t, filepath.Join(source, "arizuko.toml"), "name='bounded'\n")
	mustWrite(t, filepath.Join(source, "bounded.yaml"), "acl:\n  - principal: '@ops'\n    action: mcp:reply\n    scope: eng/*\n    effect: allow\n")
	if out, err := runManifestCLI(t, []string{"packages", "t", "install", source}); err == nil || !strings.Contains(out, "--folder") {
		t.Fatalf("unbounded grant accepted: %v, %s", err, out)
	}
	if selected, err := container.ReadPackageSelection(dataDir, ""); err != nil || len(selected) != 0 {
		t.Fatalf("refused grant activated files: %+v, %v", selected, err)
	}
	cmdPackages([]string{"t", "install", source, "--folder", "ops/**", "--folder", "eng/**"})
	rdb := mustOpenRoutd()
	defer rdb.Close()
	rows, err := store.NewOver(rdb.Obj()).ListACL("@ops")
	if err != nil || len(rows) != 1 || rows[0].Scope != "eng/*" {
		t.Fatalf("bounded grant not applied: %+v, %v", rows, err)
	}
	cmdPackages([]string{"t", "remove", "bounded"})
	rows, err = store.NewOver(rdb.Obj()).ListACL("@ops")
	if err != nil || len(rows) != 0 {
		t.Fatalf("owned grant survived removal: %+v, %v", rows, err)
	}
}

// A failed removal receipt leaves the package inactive; sync refuses resurrection and removal retry repairs the receipt.
func TestPackageLifecycleRemovalReceiptRepair(t *testing.T) {
	dataDir, base := mixInstance(t, nil, nil)
	source := filepath.Join(base, "source")
	mustWrite(t, filepath.Join(source, "arizuko.toml"), "name='tools'\n")
	mustWrite(t, filepath.Join(source, "skills", "probe", "SKILL.md"), "v1")
	rdb := mustOpenRoutd()
	defer rdb.Close()
	failure := &packageReceiptFailure{Store: rdb.Obj()}
	db := routd.OpenOver(failure)
	lock := lockPackageOperation(dataDir)
	defer lock.Close()
	candidate, err := preparePackageCandidate(dataDir, source, source, "local")
	if err != nil {
		t.Fatal(err)
	}
	if err := installManagedPackage(dataDir, db, candidate, nil); err != nil {
		t.Fatal(err)
	}
	failure.fail = true
	if err := removeManagedPackage(dataDir, "", "tools", db); err == nil || !strings.Contains(err.Error(), "inactive") {
		t.Fatalf("receipt cleanup failure hidden: %v", err)
	}
	receipt, found, err := rdb.InstalledPackage("", "tools")
	if err != nil || !found {
		t.Fatalf("failed cleanup lost receipt: %+v, %v", receipt, err)
	}
	result := reapplyPackage(filepath.Join(dataDir, "services"), db, receipt)
	if result.Refused == nil || !strings.Contains(result.Refused.Error(), "inactive") {
		t.Fatalf("sync resurrected an incomplete removal: %+v", result)
	}
	selected, err := container.ReadPackageSelection(dataDir, "")
	if err != nil || len(selected) != 0 {
		t.Fatalf("removed package reactivated: %+v, %v", selected, err)
	}
	failure.fail = false
	if err := removeManagedPackage(dataDir, "", "tools", db); err != nil {
		t.Fatal(err)
	}
	if _, found, err := rdb.InstalledPackage("", "tools"); found || err != nil {
		t.Fatalf("removal retry kept receipt: %v, %v", found, err)
	}
}

// Invalid settings and runbook markers refuse before any group payload is seeded.
func TestPackageLifecycleBlendPreflight(t *testing.T) {
	for _, scenario := range []string{"settings-collision", "mcp-settings", "runbook-markers"} {
		t.Run(scenario, func(t *testing.T) {
			files := map[string]map[string]string{
				"one": {"arizuko.toml": "name='one'\n", "PERSONA.md": "seed", ".claude/settings.json": "{\"env\":{}}"},
				"two": {"arizuko.toml": "name='two'\n", ".claude/settings.json": "{\"env\":{}}"},
			}
			order := []string{"one", "two"}
			if scenario == "mcp-settings" {
				files["one"][".claude/settings.json"] = "{\"mcpServers\":{}}"
				order = []string{"one"}
			}
			if scenario == "runbook-markers" {
				files["one"]["CLAUDE.md"] = "source runbook"
				order = []string{"one"}
			}
			dataDir, _ := mixInstance(t, files, order)
			group := filepath.Join(dataDir, "groups", "main")
			if scenario == "runbook-markers" {
				mustWrite(t, filepath.Join(group, "CLAUDE.md"), "<!-- arizuko:package:one BEGIN -->\n")
			}
			before := treeHashes(t, group)
			if out, err := runManifestCLI(t, []string{"products", "t", "apply", "main"}); err == nil || !strings.Contains(out, "Failed: blend") {
				t.Fatalf("invalid blend accepted: %v, %s", err, out)
			}
			if after := treeHashes(t, group); !reflect.DeepEqual(before, after) {
				t.Fatalf("failed blend changed group payload: %v -> %v", before, after)
			}
			if selected, err := container.ReadPackageSelection(dataDir, "main"); err != nil || len(selected) != 0 {
				t.Fatalf("failed blend activated packages: %+v, %v", selected, err)
			}
		})
	}
}

// Preserved custom seeds and settings stay unclaimed; runbook receipts hash the region actually present after source changes.
func TestPackageLifecyclePayloadReceipts(t *testing.T) {
	dataDir, base := mixInstance(t, map[string]map[string]string{
		"one": {"arizuko.toml": "name='one'\n", "PERSONA.md": "source persona",
			"CLAUDE.md": "first runbook", ".claude/settings.json": "{\"env\":{\"OWNER\":\"source\"}}"},
	}, []string{"one"})
	group := filepath.Join(dataDir, "groups", "main")
	mustWrite(t, filepath.Join(group, "PERSONA.md"), "operator persona")
	mustWrite(t, filepath.Join(group, ".claude", "settings.json"), "{\"env\":{\"OWNER\":\"operator\"}}")
	cmdProducts([]string{"t", "apply", "main"})
	mustWrite(t, filepath.Join(base, "src", "one", "CLAUDE.md"), "second runbook")
	cmdProducts([]string{"t", "apply", "main"})
	rdb := mustOpenRoutd()
	defer rdb.Close()
	receipt, found, err := rdb.InstalledPackage("main", "one")
	if err != nil || !found {
		t.Fatalf("missing receipt: %v, %v", found, err)
	}
	if _, owned := receipt.AssetHashes["file:PERSONA.md"]; owned || len(receipt.Manifest["setting"]) != 0 {
		t.Fatalf("receipt claims operator state: %+v", receipt)
	}
	want := sha256hex([]byte("<!-- arizuko:package:one BEGIN -->\nfirst runbook\n<!-- arizuko:package:one END -->"))
	if got := receipt.AssetHashes["region:CLAUDE.md"]; got != want {
		t.Fatalf("receipt describes unapplied source bytes: %q, want %q", got, want)
	}
}

// An operator's existing persona remains local state even when its bytes match a package seed.
func TestPackageLifecyclePreservesMatchingOperatorSeed(t *testing.T) {
	dataDir, base := mixInstance(t, map[string]map[string]string{
		"one": {"arizuko.toml": "name='one'\n", "PERSONA.md": "operator persona"},
	}, []string{"one"})
	group := filepath.Join(dataDir, "groups", "main")
	mustWrite(t, filepath.Join(group, "PERSONA.md"), "operator persona")
	cmdProducts([]string{"t", "apply", "main"})
	if err := os.Remove(filepath.Join(base, "src", "one", "PERSONA.md")); err != nil {
		t.Fatal(err)
	}
	cmdProducts([]string{"t", "apply", "main"})
	actual, err := os.ReadFile(filepath.Join(group, "PERSONA.md"))
	if err != nil || string(actual) != "operator persona" {
		t.Fatalf("package update deleted operator persona: %q, %v", actual, err)
	}
}

// Removing a group package withdraws its managed settings while retaining the operator's other settings.
func TestPackageLifecycleRemoveWithdrawsSettings(t *testing.T) {
	dataDir, _ := mixInstance(t, map[string]map[string]string{
		"one": {"arizuko.toml": "name='one'\n", ".claude/settings.json": "{\"env\":{\"PACKAGE_MODE\":\"fixture\"}}"},
	}, []string{"one"})
	path := filepath.Join(dataDir, "groups", "main", ".claude", "settings.json")
	mustWrite(t, path, "{\"language\":\"en\"}")
	cmdProducts([]string{"t", "apply", "main"})
	cmdPackages([]string{"t", "remove", "one", "--group", "main"})
	actual, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(actual), "PACKAGE_MODE") || !strings.Contains(string(actual), "language") {
		t.Fatalf("package settings survived removal or operator settings changed: %s, %v", actual, err)
	}
}

// Removal, desired withdrawal and source omission retain operator settings and the other package's setting.
func TestPackageLifecycleSettingsPreservation(t *testing.T) {
	for _, operation := range []string{"remove", "withdraw", "source-omission"} {
		for _, scenario := range []string{"owned", "matching", "different", "edited"} {
			t.Run(operation+"/"+scenario, func(t *testing.T) {
				dataDir, base := mixInstance(t, map[string]map[string]string{
					"one": {"arizuko.toml": "name='one'\n",
						".claude/settings.json": `{"env":{"PACKAGE_MODE":"fixture"}}`},
					"two": {"arizuko.toml": "name='two'\n",
						".claude/settings.json": `{"outputStyle":"remaining"}`},
				}, []string{"one", "two"})
				group := filepath.Join(dataDir, "groups", "main")
				path := filepath.Join(group, ".claude", "settings.json")
				settings := `{"language":"en"}`
				switch scenario {
				case "matching":
					settings = `{"language":"en","env":{"PACKAGE_MODE":"fixture"}}`
				case "different":
					settings = `{"language":"en","env":{"OWNER":"operator"}}`
				}
				mustWrite(t, path, settings)
				cmdProducts([]string{"t", "apply", "main"})
				if scenario == "edited" {
					mustWrite(t, path, `{"language":"en","env":{"OWNER":"operator"},"outputStyle":"remaining"}`)
				}
				cmdProducts([]string{"t", "apply", "main"})
				switch operation {
				case "remove":
					cmdPackages([]string{"t", "remove", "one", "--group", "main"})
				case "withdraw":
					mustWrite(t, filepath.Join(group, "arizuko.toml"),
						"[[product]]\nsource="+strconv.Quote(filepath.Join(base, "src", "two"))+"\n")
					cmdProducts([]string{"t", "apply", "main"})
				case "source-omission":
					if err := os.Remove(filepath.Join(base, "src", "one", ".claude", "settings.json")); err != nil {
						t.Fatal(err)
					}
					cmdProducts([]string{"t", "apply", "main"})
				}
				cmdPackages([]string{"t", "sync"})
				actual := map[string]any{}
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(data, &actual); err != nil {
					t.Fatal(err)
				}
				want := map[string]any{"language": "en", "outputStyle": "remaining"}
				switch scenario {
				case "matching":
					want["env"] = map[string]any{"PACKAGE_MODE": "fixture"}
				case "different", "edited":
					want["env"] = map[string]any{"OWNER": "operator"}
				}
				if !reflect.DeepEqual(actual, want) {
					t.Fatalf("settings = %v, want %v", actual, want)
				}
			})
		}
	}
}

// A failed group removal keeps its ownership receipt until retry completes without restoring settings.
func TestPackageLifecycleSettingsRemovalRetry(t *testing.T) {
	dataDir, _ := mixInstance(t, map[string]map[string]string{
		"one": {"arizuko.toml": "name='one'\n",
			".claude/settings.json": `{"env":{"PACKAGE_MODE":"fixture"}}`},
	}, []string{"one"})
	path := filepath.Join(dataDir, "groups", "main", ".claude", "settings.json")
	mustWrite(t, path, `{"language":"en"}`)
	cmdProducts([]string{"t", "apply", "main"})
	rdb := mustOpenRoutd()
	defer rdb.Close()
	failure := &packageReceiptFailure{Store: rdb.Obj(), fail: true}
	db := routd.OpenOver(failure)
	lock := lockPackageOperation(dataDir)
	defer lock.Close()
	if err := removeManagedPackage(dataDir, "main", "one", db); err == nil || !strings.Contains(err.Error(), "inactive") {
		t.Fatalf("removal receipt failure hidden: %v", err)
	}
	selected, err := container.ReadPackageSelection(dataDir, "main")
	if err != nil || len(selected) != 0 {
		t.Fatalf("failed removal still selected: %v, %v", selected, err)
	}
	if _, found, err := rdb.InstalledPackage("main", "one"); err != nil || !found {
		t.Fatalf("failed cleanup lost ownership: %v, %v", found, err)
	}
	failure.fail = false
	if err := removeManagedPackage(dataDir, "main", "one", db); err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(actual), "PACKAGE_MODE") || !strings.Contains(string(actual), "language") {
		t.Fatalf("removal retry changed settings: %s, %v", actual, err)
	}
	if _, found, err := rdb.InstalledPackage("main", "one"); err != nil || found {
		t.Fatalf("retry kept ownership receipt: %v, %v", found, err)
	}
}

func TestPackageLifecycleSettingsReceiptFailureChild(t *testing.T) {
	if os.Getenv("ARIZUKO_L574_RECEIPT_FAILURE") == "" {
		return
	}
	rdb := mustOpenRoutd()
	defer rdb.Close()
	failure := &packageReceiptFailure{Store: rdb.Obj(), fail: true}
	if os.Getenv("ARIZUKO_L574_RECEIPT_FAILURE") == "receipt" {
		failure.allow = 1
	}
	if os.Getenv("ARIZUKO_L574_RECEIPT_FAILURE") == "activation" {
		failure.fail = false
		failure.activationObstacle = filepath.Join(os.Getenv("DATA_DIR"), "package-active")
	}
	db := routd.OpenOver(failure)
	dataDir := os.Getenv("DATA_DIR")
	lock := lockPackageOperation(dataDir)
	defer lock.Close()
	applyProductMixLocked(dataDir, db, "main", nil, nil)
}

func runSettingsReceiptFailure(t *testing.T, phase string) string {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestPackageLifecycleSettingsReceiptFailureChild$")
	cmd.Env = append(os.Environ(), "ARIZUKO_PKG_ARGS=fault-fixture", "ARIZUKO_L574_RECEIPT_FAILURE="+phase)
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "Failed:") {
		t.Fatalf("receipt failure hidden: %v, %s", err, output)
	}
	t.Logf("%s failure: %s", phase, output)
	return string(output)
}

// A failed apply receipt followed by the same apply and removal leaves no package settings.
func TestPackageLifecycleSettingsReceiptFailureRetry(t *testing.T) {
	dataDir, _ := mixInstance(t, map[string]map[string]string{
		"one": {"arizuko.toml": "name='one'\n",
			".claude/settings.json": `{"env":{"PACKAGE_MODE":"fixture"}}`},
	}, []string{"one"})
	path := filepath.Join(dataDir, "groups", "main", ".claude", "settings.json")
	mustWrite(t, path, `{"language":"en"}`)
	runSettingsReceiptFailure(t, "receipt")
	actual, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(actual), "PACKAGE_MODE") {
		t.Fatalf("failure did not follow settings write: %s, %v", actual, err)
	}
	rdb := mustOpenRoutd()
	defer rdb.Close()
	receipt, found, err := rdb.InstalledPackage("main", "one")
	if err != nil || !found || !container.HasPendingSettings(receipt.AssetHashes) ||
		len(receipt.Manifest["setting"]) != 0 || receipt.AssetHashes["setting:env"] != "" {
		t.Fatalf("failed receipt did not retain prospective ownership: %+v, %v", receipt, err)
	}
	cmdProducts([]string{"t", "apply", "main"})
	cmdPackages([]string{"t", "remove", "one", "--group", "main"})
	actual, err = os.ReadFile(path)
	if err != nil || strings.Contains(string(actual), "PACKAGE_MODE") || !strings.Contains(string(actual), "language") {
		t.Fatalf("failed receipt retry stranded package settings: %s, %v", actual, err)
	}
}

// A failed preparatory store write leaves group payloads and active membership unchanged.
func TestPackageLifecycleSettingsPreparationFailure(t *testing.T) {
	dataDir, _ := mixInstance(t, map[string]map[string]string{
		"one": {"arizuko.toml": "name='one'\n", "PERSONA.md": "seed",
			".claude/settings.json": `{"env":{"PACKAGE_MODE":"fixture"}}`},
	}, []string{"one"})
	group := filepath.Join(dataDir, "groups", "main")
	mustWrite(t, filepath.Join(group, ".claude", "settings.json"), `{"language":"en"}`)
	before := treeHashes(t, group)
	output := runSettingsReceiptFailure(t, "prepare")
	if !strings.Contains(output, "payloads unchanged") {
		t.Fatalf("preparation failure not identified: %s", output)
	}
	if after := treeHashes(t, group); !reflect.DeepEqual(before, after) {
		t.Fatalf("failed preparation changed payloads: %v -> %v", before, after)
	}
	selected, err := container.ReadPackageSelection(dataDir, "main")
	if err != nil || len(selected) != 0 {
		t.Fatalf("failed preparation selected files: %v, %v", selected, err)
	}
	rdb := mustOpenRoutd()
	defer rdb.Close()
	if _, found, err := rdb.InstalledPackage("main", "one"); err != nil || found {
		t.Fatalf("failed preparation left a receipt: %v, %v", found, err)
	}
	cmdProducts([]string{"t", "apply", "main"})
	cmdPackages([]string{"t", "remove", "one", "--group", "main"})
	actual := readGroupFile(t, dataDir, "main", ".claude/settings.json")
	if strings.Contains(actual, "PACKAGE_MODE") || !strings.Contains(actual, "language") {
		t.Fatalf("preparation failure retry stranded settings: %s", actual)
	}
}

func preparePendingSettingsFixture(t *testing.T, dataDir, source string, rdb *routd.DB) packageCandidate {
	t.Helper()
	candidate, err := preparePackageCandidate(dataDir, source, source, "local")
	if err != nil {
		t.Fatal(err)
	}
	products := []container.Product{{Name: candidate.snapshot.Name, Dir: candidate.dir, Snapshot: &candidate.snapshot}}
	blend, err := container.PrepareProductBlend(filepath.Join(dataDir, "groups", "main"), products, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareSettingsReceipts("main", rdb, []packageCandidate{candidate}, blend.Blended); err != nil {
		t.Fatal(err)
	}
	return candidate
}

// Pending settings survive before and after payload writes; retry and withdrawal preserve intervening operator edits.
func TestPackageLifecycleSettingsPendingRecovery(t *testing.T) {
	for _, phase := range []string{"before-payload", "after-payload"} {
		for _, action := range []string{"retry", "remove", "withdraw", "retry-edited", "remove-edited"} {
			t.Run(phase+"/"+action, func(t *testing.T) {
				dataDir, base := mixInstance(t, map[string]map[string]string{
					"one": {"arizuko.toml": "name='one'\n",
						".claude/settings.json": `{"env":{"PACKAGE_MODE":"fixture"}}`},
				}, []string{"one"})
				group := filepath.Join(dataDir, "groups", "main")
				path := filepath.Join(group, ".claude", "settings.json")
				mustWrite(t, path, `{"language":"en"}`)
				rdb := mustOpenRoutd()
				defer rdb.Close()
				if phase == "before-payload" {
					before := treeHashes(t, group)
					preparePendingSettingsFixture(t, dataDir, filepath.Join(base, "src", "one"), rdb)
					if after := treeHashes(t, group); !reflect.DeepEqual(before, after) {
						t.Fatalf("preparation changed payloads: %v -> %v", before, after)
					}
				} else {
					runSettingsReceiptFailure(t, "receipt")
				}
				receipt, found, err := rdb.InstalledPackage("main", "one")
				wantHash := sha256hex([]byte(`{"PACKAGE_MODE":"fixture"}`))
				if err != nil || !found || receipt.AssetHashes[container.PendingSettingPrefix+"env"] != wantHash ||
					len(receipt.Manifest["setting"]) != 0 || receipt.AssetHashes["setting:env"] != "" {
					t.Fatalf("pending state claims applied settings: %+v, %v", receipt, err)
				}
				if _, _, err := existingPackageCandidates(dataDir, "main", rdb); err == nil || !strings.Contains(err.Error(), "incomplete") {
					t.Fatalf("pending state reported complete: %v", err)
				}
				edited := strings.HasSuffix(action, "-edited")
				if edited {
					mustWrite(t, path, `{"language":"en","env":{"OWNER":"operator"}}`)
				}
				if strings.HasPrefix(action, "retry") {
					cmdProducts([]string{"t", "apply", "main"})
					receipt, found, err := rdb.InstalledPackage("main", "one")
					if err != nil || !found || container.HasPendingSettings(receipt.AssetHashes) {
						t.Fatalf("retry did not settle pending state: %+v, %v", receipt, err)
					}
					if !edited && receipt.AssetHashes["setting:env"] != wantHash {
						t.Fatalf("retry lost inserted setting ownership: %+v", receipt)
					}
					if edited && (receipt.AssetHashes["setting:env"] != "" || len(receipt.Manifest["setting"]) != 0) {
						t.Fatalf("retry claimed an operator edit: %+v", receipt)
					}
				}
				if action == "withdraw" {
					mustWrite(t, filepath.Join(group, "arizuko.toml"), "")
					cmdProducts([]string{"t", "apply", "main"})
				} else {
					cmdPackages([]string{"t", "remove", "one", "--group", "main"})
				}
				want := map[string]any{"language": "en"}
				if edited {
					want["env"] = map[string]any{"OWNER": "operator"}
				}
				var actual map[string]any
				if err := json.Unmarshal([]byte(readGroupFile(t, dataDir, "main", ".claude/settings.json")), &actual); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(actual, want) {
					t.Fatalf("pending recovery settings = %v, want %v", actual, want)
				}
				if _, found, err := rdb.InstalledPackage("main", "one"); err != nil || found {
					t.Fatalf("recovery kept receipt: %v, %v", found, err)
				}
			})
		}
	}
}

// Pending proof refuses a different source or snapshot before retry writes any payload.
func TestPackageLifecycleSettingsPendingIdentity(t *testing.T) {
	for _, change := range []string{"source", "snapshot"} {
		t.Run(change, func(t *testing.T) {
			dataDir, base := mixInstance(t, map[string]map[string]string{
				"one": {"arizuko.toml": "name='one'\n",
					".claude/settings.json": `{"env":{"PACKAGE_MODE":"fixture"}}`},
			}, []string{"one"})
			group := filepath.Join(dataDir, "groups", "main")
			mustWrite(t, filepath.Join(group, ".claude", "settings.json"), `{"language":"en"}`)
			rdb := mustOpenRoutd()
			defer rdb.Close()
			source := filepath.Join(base, "src", "one")
			preparePendingSettingsFixture(t, dataDir, source, rdb)
			settings := `{"env":{"PACKAGE_MODE":"fixture"}}`
			if change == "source" {
				source = filepath.Join(base, "different-origin")
				mustWrite(t, filepath.Join(source, "arizuko.toml"), "name='one'\n")
				mustWrite(t, filepath.Join(group, "arizuko.toml"), "[[product]]\nsource="+strconv.Quote(source)+"\n")
			} else {
				settings = `{"env":{"PACKAGE_MODE":"changed"}}`
			}
			mustWrite(t, filepath.Join(source, ".claude", "settings.json"), settings)
			before := treeHashes(t, group)
			if out, err := runManifestCLI(t, []string{"products", "t", "apply", "main"}); err == nil ||
				!strings.Contains(out, "pending settings; retry the same source") {
				t.Fatalf("pending proof accepted a different %s: %v, %s", change, err, out)
			}
			if after := treeHashes(t, group); !reflect.DeepEqual(before, after) {
				t.Fatalf("refused pending retry changed payloads: %v -> %v", before, after)
			}
		})
	}
}

// An upgrade's pending receipt retains applied hashes and omits prospective keys from applied identities.
func TestPackageLifecycleSettingsPendingPreservesApplied(t *testing.T) {
	dataDir, base := mixInstance(t, map[string]map[string]string{
		"one": {"arizuko.toml": "name='one'\n",
			".claude/settings.json": `{"env":{"PACKAGE_MODE":"fixture"}}`},
	}, []string{"one"})
	path := filepath.Join(dataDir, "groups", "main", ".claude", "settings.json")
	mustWrite(t, path, `{"language":"en"}`)
	cmdProducts([]string{"t", "apply", "main"})
	rdb := mustOpenRoutd()
	defer rdb.Close()
	before, found, err := rdb.InstalledPackage("main", "one")
	if err != nil || !found {
		t.Fatalf("missing initial receipt: %v, %v", found, err)
	}
	replacement := filepath.Join(base, "replacement")
	mustWrite(t, filepath.Join(replacement, "arizuko.toml"), "name='one'\n")
	mustWrite(t, filepath.Join(replacement, ".claude", "settings.json"),
		`{"env":{"PACKAGE_MODE":"fixture"},"outputStyle":"pending"}`)
	mustWrite(t, filepath.Join(dataDir, "groups", "main", "arizuko.toml"),
		"[[product]]\nsource="+strconv.Quote(replacement)+"\n")
	runSettingsReceiptFailure(t, "receipt")
	pending, found, err := rdb.InstalledPackage("main", "one")
	if err != nil || !found || !reflect.DeepEqual(pending.Manifest, before.Manifest) {
		t.Fatalf("preparation changed applied identities: %+v -> %+v, %v", before, pending, err)
	}
	for key, hash := range before.AssetHashes {
		if pending.AssetHashes[key] != hash {
			t.Fatalf("preparation changed applied hash %s: %q -> %q", key, hash, pending.AssetHashes[key])
		}
	}
	if pending.AssetHashes["setting:outputStyle"] != "" ||
		pending.AssetHashes[container.PendingSettingPrefix+"outputStyle"] != sha256hex([]byte(`"pending"`)) {
		t.Fatalf("prospective key claims applied ownership: %+v", pending)
	}
	cmdPackages([]string{"t", "remove", "one", "--group", "main"})
	cmdPackages([]string{"t", "sync"})
	selected, err := container.ReadPackageSelection(dataDir, "main")
	if err != nil || len(selected) != 0 {
		t.Fatalf("pending replacement removal resurrected: %v, %v", selected, err)
	}
	actual, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(actual), "PACKAGE_MODE") ||
		strings.Contains(string(actual), "outputStyle") || !strings.Contains(string(actual), "language") {
		t.Fatalf("pending upgrade removal stranded owned settings: %s, %v", actual, err)
	}
}

// An activation failure leaves pending settings removable before any selection becomes active.
func TestPackageLifecycleSettingsActivationFailure(t *testing.T) {
	dataDir, _ := mixInstance(t, map[string]map[string]string{
		"one": {"arizuko.toml": "name='one'\n",
			".claude/settings.json": `{"env":{"PACKAGE_MODE":"fixture"}}`},
	}, []string{"one"})
	path := filepath.Join(dataDir, "groups", "main", ".claude", "settings.json")
	mustWrite(t, path, `{"language":"en"}`)
	obstacle := filepath.Join(dataDir, "package-active")
	runSettingsReceiptFailure(t, "activation")
	actual, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(actual), "PACKAGE_MODE") {
		t.Fatalf("failure did not follow payload write: %s, %v", actual, err)
	}
	if err := os.Remove(obstacle); err != nil {
		t.Fatal(err)
	}
	selected, err := container.ReadPackageSelection(dataDir, "main")
	if err != nil || len(selected) != 0 {
		t.Fatalf("activation failure selected files: %v, %v", selected, err)
	}
	cmdPackages([]string{"t", "remove", "one", "--group", "main"})
	actual, err = os.ReadFile(path)
	if err != nil || strings.Contains(string(actual), "PACKAGE_MODE") || !strings.Contains(string(actual), "language") {
		t.Fatalf("activation failure removal stranded settings: %s, %v", actual, err)
	}
}

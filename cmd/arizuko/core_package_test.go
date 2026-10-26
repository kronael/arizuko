package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/routd"
)

func seedCoreSource(t *testing.T, root string) string {
	t.Helper()
	for name, body := range map[string]string{
		"arizuko.toml":           "name = \"core\"\n",
		"CLAUDE.md":              "release manual",
		"skills/base/SKILL.md":   "release skill",
		"output-styles/brief.md": "release style",
	} {
		mustWrite(t, filepath.Join(root, "ant", name), body)
	}
	t.Setenv("APP_SRC_DIR", root)
	return filepath.Join(root, "ant")
}

// Upgrade core works without a prior receipt; sync follows the configured release without fetching ant as a path.
func TestCorePackageCLIUpgradeAndSync(t *testing.T) {
	dataDir, base := mixInstance(t, nil, nil)
	source := seedCoreSource(t, filepath.Join(base, "release"))
	if out, err := runManifestCLI(t, []string{"packages", "t", "upgrade", "core"}); err != nil {
		t.Fatalf("initial core upgrade failed: %v, %s", err, out)
	}
	if got := selectedSkill(t, dataDir, "", "core", "base"); got != "release skill" {
		t.Fatal(got)
	}
	database, err := routd.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	first, found, err := database.InstalledPackage("", "core")
	if err != nil || !found || first.Source != "ant" {
		t.Fatalf("wrong release identity: %+v, %v, %v", first, found, err)
	}
	mustWrite(t, filepath.Join(source, "skills/base/SKILL.md"), "next release")
	if out, err := runManifestCLI(t, []string{"packages", "t", "sync"}); err != nil {
		t.Fatalf("core sync failed: %v, %s", err, out)
	}
	if got := selectedSkill(t, dataDir, "", "core", "base"); got != "next release" {
		t.Fatal(got)
	}
	second, found, err := database.InstalledPackage("", "core")
	if err != nil || !found || second.Source != "ant" || second.Revision == first.Revision {
		t.Fatalf("core sync kept the old receipt: %+v, %v, %v", second, found, err)
	}
	before, err := os.Lstat(filepath.Join(dataDir, "package-active/system"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(source, source+"-absent"); err != nil {
		t.Fatal(err)
	}
	out, err := runManifestCLI(t, []string{"packages", "t", "sync"})
	if err == nil || !strings.Contains(out, "required core source") {
		t.Fatalf("missing release hidden: %v, %s", err, out)
	}
	after, err := os.Lstat(filepath.Join(dataDir, "package-active/system"))
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("failed sync changed the active graph: %v", err)
	}
	got, _, err := database.InstalledPackage("", "core")
	if err != nil || !reflect.DeepEqual(got, second) {
		t.Fatalf("failed sync changed receipt: %+v, %v", got, err)
	}
	selected, err := container.ReadPackageSelection(dataDir, "")
	if err != nil || len(selected) != 1 || selected[0].Digest != second.Revision {
		t.Fatalf("wrong active core: %+v, %v", selected, err)
	}
}

// Empty-record sync initializes core before deciding whether optional packages need work.
func TestCorePackageCLIEmptySync(t *testing.T) {
	dataDir, _ := mixInstance(t, nil, nil)
	if out, err := runManifestCLI(t, []string{"packages", "t", "sync"}); err != nil {
		t.Fatalf("empty sync failed: %v, %s", err, out)
	}
	if got := selectedSkill(t, dataDir, "", "core", "base"); got != "release skill" {
		t.Fatal(got)
	}
}

// Foreign, group and legacy core operations refuse before changing payloads, declarations or active selections.
func TestCorePackageCLIReservedIdentity(t *testing.T) {
	for _, operation := range []string{
		"install", "group-install", "apply", "remove", "group-remove",
		"group-upgrade", "legacy-install",
	} {
		t.Run(operation, func(t *testing.T) {
			dataDir, base := mixInstance(t, nil, nil)
			cmdPackages([]string{"t", "upgrade", "core"})
			group := filepath.Join(dataDir, "groups/main")
			mustWrite(t, filepath.Join(group, "PERSONA.md"), "operator persona")
			source := filepath.Join(base, "foreign")
			mustWrite(t, filepath.Join(source, "arizuko.toml"), "name = \"core\"\n")
			mustWrite(t, filepath.Join(source, "skills/foreign/SKILL.md"), "foreign skill")
			mustWrite(t, filepath.Join(source, "PERSONA.md"), "foreign persona")
			args := []string{"packages", "t", "install", source}
			switch operation {
			case "group-install":
				args = append(args, "--group", "main")
			case "apply":
				mustWrite(t, filepath.Join(group, "arizuko.toml"), "# caller-owned\n[[product]]\nsource = \""+source+"\"\n")
				args = []string{"products", "t", "apply", "main"}
			case "remove":
				args = []string{"packages", "t", "remove", "core"}
			case "group-remove":
				args = []string{"packages", "t", "remove", "core", "--group", "main"}
			case "group-upgrade":
				args = []string{"packages", "t", "upgrade", "core", "--group", "main"}
			case "legacy-install":
				source = filepath.Join(base, "core")
				mustWrite(t, filepath.Join(source, "core.yml"), "services:\n  core:\n    image: fixture\n")
				args = []string{"packages", "t", "install", source}
			}
			before := treeHashes(t, group)
			selected, err := container.ReadPackageSelection(dataDir, "")
			if err != nil {
				t.Fatal(err)
			}
			out, err := runManifestCLI(t, args)
			if err == nil || !strings.Contains(out, "core") || !strings.Contains(out, "packages upgrade core") {
				t.Fatalf("reserved core operation accepted: %v, %s", err, out)
			}
			if after := treeHashes(t, group); !reflect.DeepEqual(before, after) {
				t.Fatal("reserved core operation changed group payload or declaration")
			}
			if after, err := container.ReadPackageSelection(dataDir, ""); err != nil || !reflect.DeepEqual(selected, after) {
				t.Fatalf("reserved operation changed system selection: %+v, %v", after, err)
			}
			if selected, err := container.ReadPackageSelection(dataDir, "main"); err != nil || len(selected) != 0 {
				t.Fatalf("reserved operation selected a group core: %+v, %v", selected, err)
			}
			if _, err := os.Lstat(filepath.Join(dataDir, "services/core.yml")); !os.IsNotExist(err) {
				t.Fatalf("reserved operation wrote a service: %v", err)
			}
		})
	}
}

// Preflight reserves active and configured core skills.
// A refusal preserves optional payloads and the active graph.
func TestCorePackageCLIReservesConfiguredRelease(t *testing.T) {
	for _, operation := range []string{
		"install", "group-install", "apply", "install-active-skill",
		"group-install-active-skill",
	} {
		t.Run(operation, func(t *testing.T) {
			dataDir, base := mixInstance(t, nil, nil)
			cmdPackages([]string{"t", "upgrade", "core"})
			selected, err := container.ReadPackageSelection(dataDir, "")
			if err != nil {
				t.Fatal(err)
			}
			active := filepath.Join(dataDir, "package-active/system")
			beforeLink, err := os.Lstat(active)
			if err != nil {
				t.Fatal(err)
			}
			mustWrite(t, filepath.Join(base, "release/ant/skills/future/SKILL.md"), "new required skill")
			skill := "future"
			if strings.HasSuffix(operation, "active-skill") {
				skill = "base"
				err := os.Rename(filepath.Join(base, "release/ant/skills/base"),
					filepath.Join(base, "release/ant/retired-base"))
				if err != nil {
					t.Fatal(err)
				}
			}
			source := filepath.Join(base, "optional")
			mustWrite(t, filepath.Join(source, "arizuko.toml"), "name = \"optional\"\n")
			mustWrite(t, filepath.Join(source, "skills", skill, "SKILL.md"), "conflicting optional skill")
			mustWrite(t, filepath.Join(source, "PERSONA.md"), "optional persona")
			group := filepath.Join(dataDir, "groups/main")
			mustWrite(t, filepath.Join(group, "PERSONA.md"), "operator persona")
			args := []string{"packages", "t", "install", source}
			switch operation {
			case "install", "install-active-skill":
				mustWrite(t, filepath.Join(source, "optional.yml"), "services:\n  optional:\n    image: fixture\n")
				mustWrite(t, filepath.Join(source, "optional.yaml"), "env:\n  OPTIONAL_SECRET:\n    generate: true\n")
			case "group-install", "group-install-active-skill":
				args = append(args, "--group", "main")
			case "apply":
				mustWrite(t, filepath.Join(group, "arizuko.toml"),
					"# operator metadata\nname = \"mine\"\n[[product]]\nsource = \""+source+"\"\n")
				args = []string{"products", "t", "apply", "main"}
			}
			before := treeHashes(t, group)
			out, err := runManifestCLI(t, args)
			if err == nil || !strings.Contains(out, skill) || !strings.Contains(out, "core") {
				t.Fatalf("configured core skill collision hidden: %v, %s", err, out)
			}
			if after := treeHashes(t, group); !reflect.DeepEqual(before, after) {
				t.Fatal("skill collision changed group payload or declaration")
			}
			if after, err := container.ReadPackageSelection(dataDir, ""); err != nil || !reflect.DeepEqual(selected, after) {
				t.Fatalf("preflight selected the new core: %+v, %v", after, err)
			}
			afterLink, err := os.Lstat(active)
			if err != nil || !os.SameFile(beforeLink, afterLink) {
				t.Fatalf("preflight replaced active reference: %v", err)
			}
			if _, err := os.Lstat(filepath.Join(dataDir, "services/optional.yml")); !os.IsNotExist(err) {
				t.Fatalf("collision wrote optional service: %v", err)
			}
			data, err := os.ReadFile(filepath.Join(dataDir, ".env"))
			if err != nil && !os.IsNotExist(err) || strings.Contains(string(data), "OPTIONAL_SECRET") {
				t.Fatalf("collision provisioned optional credentials: %v", err)
			}
		})
	}
}

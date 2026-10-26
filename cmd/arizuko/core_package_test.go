package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/store"
)

type coreReceiptFailure struct {
	objstore.Store
}

func (database coreReceiptFailure) GuardedBatch(ctx context.Context,
	checks []objstore.Check, writes []objstore.Write) (bool, error) {
	for _, write := range writes {
		if write.Entity == store.InstalledPackages {
			return false, errors.New("receipt write refused")
		}
	}
	return database.Store.GuardedBatch(ctx, checks, writes)
}

func TestCorePackageGroupReceiptFailure(t *testing.T) {
	dataDir, _ := mixInstance(t, nil, nil)
	endpoint, err := url.Parse(os.Getenv("DYNAMODB_URL"))
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(endpoint)
	var refuse atomic.Bool
	refuse.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if refuse.Load() && strings.HasSuffix(r.Header.Get("X-Amz-Target"), ".TransactWriteItems") &&
			bytes.Contains(body, []byte("installed_packages")) {
			w.Header().Set("Content-Type", "application/x-amz-json-1.0")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"__type":"AccessDeniedException","message":"receipt write refused"}`))
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		proxy.ServeHTTP(w, r)
	}))
	defer server.Close()
	t.Setenv("DYNAMODB_URL", server.URL)
	args := []string{"group", "t", "add", "telegram:777", "new"}
	out, err := runManifestCLI(t, args)
	if err == nil || !strings.Contains(out, "active but its receipt failed") {
		t.Fatalf("receipt failure hidden: %v %s", err, out)
	}
	active := filepath.Join(dataDir, "package-active/system")
	before, err := os.Lstat(active)
	if err != nil {
		t.Fatal(err)
	}
	db := instanceStore(t)
	if _, exists := db.GroupByFolder("new"); exists {
		t.Fatal("receipt failure published group")
	}
	refuse.Store(false)
	if out, err := runManifestCLI(t, args); err != nil {
		t.Fatalf("receipt repair failed: %v %s", err, out)
	}
	after, err := os.Lstat(active)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("repair replaced the active reference: %v", err)
	}
	if _, found, err := db.InstalledPackage("", "core"); err != nil || !found {
		t.Fatalf("repair omitted core receipt: %v", err)
	}
}

func TestCorePackageArchiveFailure(t *testing.T) {
	for _, failure := range []string{"source", "receipt"} {
		t.Run(failure, func(t *testing.T) {
			archive := coreArchive(t, true)
			dataDir, stores := openInstance(t)
			database := stores[resreg.SubsystemRoutd]
			if failure == "source" {
				t.Setenv("APP_SRC_DIR", t.TempDir())
			} else {
				stores[resreg.SubsystemRoutd] = store.NewOver(coreReceiptFailure{database.Obj()})
			}
			_, err := applyArchive(t.Context(), stores, dataDir, bytes.NewReader(archive), true, nil)
			if err == nil {
				t.Fatal("core failure hidden")
			}
			if _, exists := database.GroupByFolder("restored"); exists {
				t.Fatal("core failure published restored group")
			}
			if failure == "receipt" {
				selected, err := container.ReadPackageSelection(dataDir, "")
				if err != nil || len(selected) != 1 {
					t.Fatalf("receipt failure did not reach activation: %+v %v", selected, err)
				}
			}
		})
	}
}

// Create selects core without a store; generate completes its receipt without legacy skills.
func TestCorePackageCreateBeforeStore(t *testing.T) {
	for _, product := range []string{"", "sample"} {
		t.Run("product="+product, func(t *testing.T) {
			base := t.TempDir()
			dataDir := filepath.Join(base, "arizuko_fresh")
			unsealPackageFixtures(t, dataDir)
			release := filepath.Join(base, "release")
			seedCoreSource(t, release)
			t.Setenv("HOST_APP_DIR", release)
			t.Setenv("ARIZUKO_DATA_DIR", base)
			t.Setenv("DATA_DIR", dataDir)
			args := []string{"create", "fresh"}
			if product != "" {
				sample := filepath.Join(release, "ant/examples/sample")
				mustWrite(t, filepath.Join(sample, "arizuko.toml"), "name = \"sample\"\n")
				mustWrite(t, filepath.Join(sample, "PERSONA.md"), "product persona")
				args = append(args, "--product", product)
			}
			endpoint := os.Getenv("DYNAMODB_URL")
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				http.Error(w, "store does not exist", http.StatusServiceUnavailable)
			}))
			defer server.Close()
			t.Setenv("DYNAMODB_URL", server.URL)
			if out, err := runManifestCLI(t, args); err != nil {
				t.Fatalf("create failed: %v, %s", err, out)
			}
			if requests.Load() != 0 {
				t.Fatalf("create made %d pre-store requests", requests.Load())
			}
			if got := selectedSkill(t, dataDir, "", "core", "base"); got != "release skill" {
				t.Fatal(got)
			}
			t.Setenv("DYNAMODB_URL", endpoint)
			t.Cleanup(func() { dropInstance(t) })
			syncPackageSkills(dataDir)
			db, err := routd.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			receipt, found, err := db.InstalledPackage("", "core")
			selected, serr := container.ReadPackageSelection(dataDir, "")
			if err != nil || serr != nil || !found || len(selected) != 1 ||
				receipt.Source != "ant" || receipt.Revision != selected[0].Digest {
				t.Fatalf("completion missing: %+v %+v %v %v", selected, receipt, err, serr)
			}
		})
	}
}

func TestCorePackageCreateAndGroupFailure(t *testing.T) {
	dataDir, base := mixInstance(t, nil, nil)
	t.Setenv("APP_SRC_DIR", filepath.Join(base, "absent-release"))
	for _, args := range [][]string{{"create", "t"}, {"group", "t", "add", "telegram:777", "new"}} {
		out, err := runManifestCLI(t, args)
		if err == nil || !strings.Contains(out, "required core source") ||
			strings.Contains(out, "created instance") {
			t.Fatalf("failure hidden: %v, %s", err, out)
		}
	}
	db := instanceStore(t)
	if _, exists := db.GroupByFolder("new"); exists {
		t.Fatal("failed group setup published a row")
	}
	if _, err := os.Stat(filepath.Join(dataDir, "groups/new")); !os.IsNotExist(err) {
		t.Fatalf("failed setup created a group skeleton: %v", err)
	}
}

func TestCorePackageGroupAdd(t *testing.T) {
	dataDir, _ := mixInstance(t, nil, nil)
	if out, err := runManifestCLI(t, []string{"group", "t", "add", "telegram:777", "new"}); err != nil {
		t.Fatalf("group add failed: %v, %s", err, out)
	}
	db := instanceStore(t)
	receipt, found, err := db.InstalledPackage("", "core")
	selected, serr := container.ReadPackageSelection(dataDir, "")
	if err != nil || serr != nil || !found || len(selected) != 1 ||
		receipt.Source != "ant" || receipt.Revision != selected[0].Digest {
		t.Fatalf("group receipt missing: %+v %+v %v %v", selected, receipt, err, serr)
	}
}

func TestCorePackageGenerateWithoutLegacySkills(t *testing.T) {
	dataDir := generateInstance(t, "WEB_PORT=8095\n")
	if _, err := os.Stat(filepath.Join(dataDir, "skills")); !os.IsNotExist(err) {
		t.Fatalf("fixture has a legacy skills tree: %v", err)
	}
	generateCompose(dataDir)
	db := instanceStore(t)
	selected, err := container.ReadPackageSelection(dataDir, "")
	receipt, found, rerr := db.InstalledPackage("", "core")
	if err != nil || rerr != nil || !found || len(selected) != 1 ||
		receipt.Source != "ant" || receipt.Revision != selected[0].Digest {
		t.Fatalf("generate omitted core: %+v %+v %v %v", selected, receipt, err, rerr)
	}
}

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

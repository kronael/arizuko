package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/store"
)

// `packages add` provisions what the package's manifest needs from .env and
// applies its rows through the same path `install` uses: a `generate` key is
// minted into .env, so the instance comes up on the next generate with no
// hand-written secret (the litellm package used to install cleanly and then
// refuse to start until the operator wrote MODEL_GATEWAY_KEY by hand), and a
// declared row lands with a record that names it, so `remove` can withdraw it.
func TestPackagesAddProvisionsEnvAndAppliesRows(t *testing.T) {
	t.Cleanup(func() { dropInstance(t) })
	dataDir := seedSyncInstance(t, "mint",
		map[string]string{
			"litellm.yml": "services:\n  litellm:\n    image: x\n",
			"litellm/arizuko.yml": "name: litellm\n" + ("env:\n  MODEL_GATEWAY_KEY: {generate: true}\n" +
				"network_rules:\n  - folder: ''\n    target: api.anthropic.com\n"),
		},
		map[string]string{})
	seedRoutdDB(t, dataDir)

	cmdPackages([]string{"mint", "add", "litellm"})

	dotenv, err := os.ReadFile(filepath.Join(dataDir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(dotenv), "MODEL_GATEWAY_KEY=") {
		t.Fatalf("add did not mint MODEL_GATEWAY_KEY into .env:\n%s", dotenv)
	}
	if _, err := os.Lstat(filepath.Join(dataDir, "packages", "litellm", "arizuko.yml")); err != nil {
		t.Errorf("manifest not linked beside the fragment: %v", err)
	}
	rec := mustRec(t, dataDir, "litellm")
	if rec.Source != catalogOrigin || len(rec.Manifest["network_rules"]) != 1 {
		t.Fatalf("add must record the rows it applied: %+v", rec)
	}
	rdb, err := routd.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer rdb.Close()
	// The floor a routd.db is born with carries this very rule, and the package
	// declares it too — so the count is one either way and what `remove` has to
	// prove is that the row it recorded is gone.
	if n := ruleCount(t, store.NewOver(rdb.Obj()), "", "api.anthropic.com"); n != 1 {
		t.Fatalf("catalog package's network rule not applied: %d rules name it", n)
	}

	cmdPackages([]string{"mint", "remove", "litellm"})
	if n := ruleCount(t, store.NewOver(rdb.Obj()), "", "api.anthropic.com"); n != 0 {
		t.Error("remove must withdraw the row the record names")
	}
	if _, err := os.Lstat(filepath.Join(dataDir, "services", "litellm.yml")); err == nil {
		t.Error("remove must drop the linked fragment")
	}
}

// seedRoutdDB gives an instance the routd.db every real one has.
func seedRoutdDB(t *testing.T, dataDir string) {
	t.Helper()
	t.Cleanup(func() { dropInstance(t) })
	if err := os.MkdirAll(filepath.Join(dataDir, "store"), 0o755); err != nil {
		t.Fatal(err)
	}
	rdb, err := routd.Open()
	if err != nil {
		t.Fatal(err)
	}
	rdb.Close()
}

// A `required` need with no operator value refuses at install time, naming the
// key, and leaves no fragment behind — never a services/ entry that generate
// would include and docker would then fail to start.
func TestPackagesInstallRefusesUnsetRequiredEnv(t *testing.T) {
	pkgChildRun()
	base := t.TempDir()
	t.Setenv("ARIZUKO_DATA_DIR", base)
	dataDir := seedPkgInstance(t, base, "req")
	src := writePkg(t, base, "reqpkg", "env:\n  REQPKG_TOKEN: {required: true}\n")

	if code := runPkgExpectingDeath(t, []string{"req", "install", src}); code == 0 {
		t.Fatal("install of a package whose required env is unset must fail")
	}
	if _, err := os.Stat(filepath.Join(dataDir, "services", "reqpkg.yml")); err == nil {
		t.Error("a refused install must write no fragment")
	}

	if err := os.WriteFile(filepath.Join(dataDir, ".env"), []byte("REQPKG_TOKEN=abc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmdPackages([]string{"req", "install", src})
	if _, err := os.Stat(filepath.Join(dataDir, "services", "reqpkg.yml")); err != nil {
		t.Errorf("install with the value set must succeed: %v", err)
	}
}

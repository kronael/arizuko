package container

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/kronael/arizuko/resreg/resources"
)

func TestServicePackageInventoryUsesManifest(t *testing.T) {
	root := t.TempDir()
	for name, body := range map[string]string{
		"core": "name: core\n",
		"gateway": "name: gateway\nenv:\n  NEWS_FEED_TOKEN: {required: true, hint: Group secret}\n" +
			"proxyd_routes:\n  - path: /gateway/\n    backend: http://gateway:8080\n    auth: user\n",
	} {
		dir := filepath.Join(root, "packages", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ManifestFile), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	manifest, err := ReadProductManifest(filepath.Join(root, "packages", "gateway"))
	if err != nil {
		t.Fatal(err)
	}
	rows, ok := manifest.Documents[0].Rows[resources.ProxydRoutesName].([]resources.ProxydRoutesRow)
	if !ok || len(rows) != 1 || rows[0].Path != "/gateway/" {
		t.Fatalf("typed rows: %+v", manifest)
	}
	if !manifest.Env["NEWS_FEED_TOKEN"].Required {
		t.Fatal("env requirement lost")
	}
	inventory, err := ReadPackageInventory(root, []string{"core", "gateway"})
	if err != nil || len(inventory.Packages) != 2 {
		t.Fatalf("inventory: %+v, %v", inventory, err)
	}
	path := filepath.Join(root, "packages", "gateway", ManifestFile)
	if err := os.WriteFile(path, []byte("name: gateway\ngroups: []\n---\ninvites: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPackageInventory(root, []string{"core", "gateway"}); err != nil {
		t.Fatalf("separate owner documents: %v", err)
	}
	for _, body := range []string{
		"name: gateway\nproxyd_route: []\n",
		"name: gateway\nproxyd_routes:\n  - path: /gateway/\n    strip_prefx: true\n",
		"name: gateway\ngroups: []\ninvites: []\n",
		"name: gateway\npackages:\n  - source: nested\n",
		"brand: Gateway\n",
	} {
		if err := os.WriteFile(filepath.Join(root, "packages", "gateway", ManifestFile), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadPackageInventory(root, []string{"core", "gateway"}); err == nil {
			t.Fatalf("inventory accepted %q", body)
		}
	}
}

func TestPackageInventoryLastPackageWins(t *testing.T) {
	root := t.TempDir()
	for name, files := range map[string][]string{
		"core": {"skills/base/SKILL.md", "hooks/h.sh", "bin/tool"},
		"a":    {"skills/base/SKILL.md", "bin/tool"},
		"b":    {"skills/base/SKILL.md", "hooks/h.sh"},
	} {
		dir := filepath.Join(root, "packages", name)
		files = append(files, ManifestFile)
		for _, file := range files {
			path := filepath.Join(dir, file)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("name: "+name+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	inventory, err := ReadPackageInventory(root, []string{"core", "a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	wantAssets := []PackageAsset{{"skills/base", "b"}, {"hooks/h.sh", "b"}, {"bin/tool", "a"}}
	wantOverrides := []string{
		"a overrides core on skills/base", "a overrides core on bin/tool",
		"b overrides a on skills/base", "b overrides core on hooks/h.sh",
	}
	if !reflect.DeepEqual(inventory.Assets, wantAssets) || !reflect.DeepEqual(inventory.Overrides, wantOverrides) {
		t.Fatalf("selected order core, a, b:\n%+v\n%v", inventory.Assets, inventory.Overrides)
	}
	inventory, err = ReadPackageInventory(root, []string{"core", "b", "a"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []PackageAsset{{"skills/base", "a"}, {"hooks/h.sh", "b"}, {"bin/tool", "a"}}; !reflect.DeepEqual(inventory.Assets, want) {
		t.Fatalf("selected order core, b, a: %+v", inventory.Assets)
	}
}

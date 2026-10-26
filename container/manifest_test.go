package container

import (
	"os"
	"path/filepath"
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

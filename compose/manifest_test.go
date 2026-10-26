package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kronael/arizuko/resreg"
)

func TestGenerateRequiresInstalledPackageManifest(t *testing.T) {
	dir := seed(t, "")
	write(t, dir, "services/worker.yml", "services:\n  worker:\n    image: x\n")
	path := filepath.Join(dir, "packages", "worker", "arizuko.yml")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Generate(dir); err == nil || !strings.Contains(err.Error(), "arizuko.yml") {
		t.Fatalf("missing package declaration: %v", err)
	}
}

// Every shipped manifest parses through the one decoder both readers use, and
// the two packages whose defaults used to be Go switches now declare them —
// the ttsd auto-enable and the gateway key are manifest facts, not code.
func TestCatalogManifestsDeclareTheirNeeds(t *testing.T) {
	entries, err := os.ReadDir(templateServices)
	if err != nil {
		t.Fatal(err)
	}
	manifests := map[string]Manifest{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m, err := ReadManifest(filepath.Join(templateServices, e.Name(), "arizuko.yml"))
		if err != nil {
			t.Errorf("%s: %v", e.Name(), err)
			continue
		}
		manifests[e.Name()] = m
	}
	want := map[string]map[string]resreg.EnvNeed{
		"litellm": {"MODEL_GATEWAY_KEY": {Generate: true}, "MODEL_GATEWAY_URL": {Default: "http://litellm:4000"}},
		"ttsd":    {"TTS_ENABLED": {Default: "true"}, "TTS_BASE_URL": {Default: "http://ttsd:8880"}},
	}
	for name, needs := range want {
		for key, need := range needs {
			if got := manifests[name].Env[key]; got != need {
				t.Errorf("%s.yaml env %s = %+v, want %+v", name, key, got, need)
			}
		}
	}
}

// CatalogDir prefers the checkout HOST_APP_DIR names when it is visible and
// otherwise the copy baked into the image — never a path derived from where
// this process runs.
func TestCatalogDirFallsBackToTheBakedCopy(t *testing.T) {
	host := t.TempDir()
	if got := CatalogDir(host); got != filepath.Join(bakedAppSrc, "template", "services") {
		t.Errorf("no template/services under HOST_APP_DIR: got %q, want the baked copy", got)
	}
	if err := os.MkdirAll(filepath.Join(host, "template", "services"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := CatalogDir(host); got != filepath.Join(host, "template", "services") {
		t.Errorf("visible checkout: got %q", got)
	}
	if got := CatalogDir(""); got != filepath.Join(bakedAppSrc, "template", "services") {
		t.Errorf("unset HOST_APP_DIR: got %q", got)
	}
}

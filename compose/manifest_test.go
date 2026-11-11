package compose

import (
	"io/fs"
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
	if _, _, err := Generate(dir, "test"); err == nil || !strings.Contains(err.Error(), "arizuko.yml") {
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

// The binary carries every service package under template/services, byte for
// byte, and a name that is not one of them, or that walks a path, is no
// release service.
func TestReleaseServiceIsTheShippedDirectory(t *testing.T) {
	entries, err := os.ReadDir(templateServices)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sub, ok := ReleaseService(e.Name())
		if !ok {
			t.Fatalf("%s: not carried by the binary", e.Name())
		}
		for _, file := range []string{"arizuko.yml", e.Name() + ".yml"} {
			want, err := os.ReadFile(filepath.Join(templateServices, e.Name(), file))
			if err != nil {
				t.Fatal(err)
			}
			if got, err := fs.ReadFile(sub, file); err != nil || string(got) != string(want) {
				t.Errorf("%s/%s: carried bytes differ: %v", e.Name(), file, err)
			}
		}
	}
	for _, name := range []string{"", ".", "..", "../gitd", "gitd/", "gitd/arizuko.yml", "macrohype"} {
		if _, ok := ReleaseService(name); ok {
			t.Errorf("%q accepted as a release service", name)
		}
	}
}

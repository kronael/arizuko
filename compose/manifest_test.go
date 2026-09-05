package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kronael/arizuko/resreg"
)

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
		if !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		m, err := ReadManifest(filepath.Join(templateServices, e.Name()))
		if err != nil {
			t.Errorf("%s: %v", e.Name(), err)
			continue
		}
		manifests[strings.TrimSuffix(e.Name(), ".yaml")] = m
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

// A base fragment linked before its manifest shipped — every adapter on the
// fleet on 2026-09-05 — gets the manifest linked on the next generate; a
// sidecar the operator edited stays a real file.
func TestRelinkCatalogLinksTheMissingSidecarOfALinkedBase(t *testing.T) {
	manifest := "proxyd_routes:\n  - path: /dash/teled/\n    backend: http://teled:8080\n    auth: user\n"
	svcDir, tmplDir := seedCatalog(t,
		map[string]string{"teled.yml": teledV2, "teled.yaml": manifest},
		map[string]string{})
	hostDir := filepath.Join(t.TempDir(), "hostview", "template", "services")
	if err := os.Symlink(filepath.Join(hostDir, "teled.yml"), filepath.Join(svcDir, "teled.yml")); err != nil {
		t.Fatal(err)
	}

	relinked, _, err := RelinkCatalog(svcDir, tmplDir, hostDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(relinked) != 0 {
		t.Errorf("relinked = %v, want none: the base was already linked", relinked)
	}
	isSymlinkTo(t, filepath.Join(svcDir, "teled.yaml"), filepath.Join(hostDir, "teled.yaml"))

	edited, tmplDir := seedCatalog(t,
		map[string]string{"teled.yml": teledV2, "teled.yaml": manifest},
		map[string]string{"teled.yaml": manifest + "  - path: /mine/\n    backend: http://teled:8080\n    auth: user\n"})
	if err := os.Symlink(filepath.Join(hostDir, "teled.yml"), filepath.Join(edited, "teled.yml")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := RelinkCatalog(edited, tmplDir, hostDir); err != nil {
		t.Fatal(err)
	}
	isRealFile(t, filepath.Join(edited, "teled.yaml"))
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

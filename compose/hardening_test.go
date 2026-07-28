package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// C6: a malformed compose-managed marker (begin with no end) must fail loud,
// never silently drop the operator lines after it.
func TestManagedEnvMalformedMarkerFailsLoud(t *testing.T) {
	dir := t.TempDir()
	env := filepath.Join(dir, ".env")
	mustWrite(t, env, "A=1\n"+managedBegin+"\nCOMPOSE_PROFILES=x\nSECRET=keepme\n")
	if err := writeManagedEnv(dir, "app", "flav", []string{"web"}); err == nil {
		t.Fatal("expected error on unbalanced managed marker, got nil")
	}
	b, _ := os.ReadFile(env)
	if !strings.Contains(string(b), "SECRET=keepme") {
		t.Fatalf("malformed-marker path mutated .env: %q", b)
	}
}

// C6: a well-formed block is stripped and rewritten; operator lines before and
// after it survive, and exactly one block remains.
func TestManagedEnvPreservesTailAfterBlock(t *testing.T) {
	dir := t.TempDir()
	env := filepath.Join(dir, ".env")
	mustWrite(t, env, "A=1\n"+managedBegin+"\nOLD=1\n"+managedEnd+"\nKEEP=yes\n")
	if err := writeManagedEnv(dir, "app", "flav", []string{"web"}); err != nil {
		t.Fatal(err)
	}
	s := readFile(t, env)
	if !strings.Contains(s, "A=1") || !strings.Contains(s, "KEEP=yes") {
		t.Fatalf("operator lines lost: %q", s)
	}
	if n := strings.Count(s, managedBegin); n != 1 {
		t.Fatalf("managed block count = %d, want 1: %q", n, s)
	}
	if strings.Contains(s, "OLD=1") {
		t.Fatalf("stale managed value survived: %q", s)
	}
}

// C3: convertLegacyTOML refuses to overwrite a native .yml sitting next to the
// .toml (a mixed/partial-migration state) rather than choosing a source.
func TestLegacyConvertRefusesExistingYML(t *testing.T) {
	sd := filepath.Join(t.TempDir(), "services")
	if err := os.MkdirAll(sd, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(sd, "teled.toml"), "image = \"arizuko:latest\"\nentrypoint = [\"teled\"]\n")
	mustWrite(t, filepath.Join(sd, "teled.yml"), "services:\n  teled:\n    image: arizuko:latest\n")
	if err := convertLegacyTOML(sd); err == nil {
		t.Fatal("expected error when .toml and .yml coexist, got nil")
	}
}

// C4: convertLegacyTOML backs the .toml up rather than deleting it, so an
// incompatible host can roll back the conversion.
func TestLegacyConvertBacksUpTOML(t *testing.T) {
	sd := filepath.Join(t.TempDir(), "services")
	if err := os.MkdirAll(sd, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(sd, "teled.toml"), "image = \"arizuko:latest\"\nentrypoint = [\"teled\"]\n")
	if err := convertLegacyTOML(sd); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(sd, "teled.toml.bak")); err != nil {
		t.Fatal("expected teled.toml.bak backup")
	}
	if _, err := os.Stat(filepath.Join(sd, "teled.toml")); !os.IsNotExist(err) {
		t.Fatal("original .toml should be renamed away")
	}
	if _, err := os.Stat(filepath.Join(sd, "teled.yml")); err != nil {
		t.Fatal("expected teled.yml")
	}
}

// C8: a route sidecar with a misspelled field must error, not emit a
// behaviorally wrong route.
func TestProxydRoutesRejectUnknownField(t *testing.T) {
	sd := filepath.Join(t.TempDir(), "services")
	if err := os.MkdirAll(sd, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(sd, "slakd-routes.json"), `[{"path":"/slack/","backend":"http://slakd:8080","auth":"public","strip_prefx":true}]`)
	if _, err := collectProxydRoutes(sd, []string{"slakd"}, map[string]string{}); err == nil {
		t.Fatal("expected error on unknown route field, got nil")
	}
}

// C8: redirect_to (part of proxyd's shape) is now accepted.
func TestProxydRoutesAcceptRedirectTo(t *testing.T) {
	sd := filepath.Join(t.TempDir(), "services")
	if err := os.MkdirAll(sd, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(sd, "x-routes.json"), `[{"path":"/x/","backend":"http://x:8080","auth":"public","redirect_to":"/y/"}]`)
	routes, err := collectProxydRoutes(sd, []string{"x"}, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range routes {
		if r.RedirectTo == "/y/" {
			return
		}
	}
	t.Fatal("redirect_to not parsed")
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

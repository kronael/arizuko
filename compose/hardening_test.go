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

// C8: a package manifest with a misspelled field must error, not emit a
// behaviorally wrong route. The strictness is resreg.ParseYAML's, the same
// decoder `arizuko apply` uses — one parser, so the two readers of a package's
// one declaration cannot disagree about what is valid (BUGS L44).
func TestProxydRoutesRejectUnknownField(t *testing.T) {
	sd := filepath.Join(t.TempDir(), "services")
	if err := os.MkdirAll(sd, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Dir(sd), "packages/slakd/arizuko.yml",
		"proxyd_routes:\n  - path: /slack/\n    backend: http://slakd:8080\n    auth: public\n    strip_prefx: true\n")
	if _, err := collectProxydRoutes(sd, "", []string{"slakd"}, map[string]string{}); err == nil {
		t.Fatal("expected error on unknown route field, got nil")
	}
}

// A misspelled RESOURCE key is the same class one level up: a package that
// meant `scheduled_tasks` and wrote `scheduled_task` must fail generate, not
// install a manifest half of which does nothing.
func TestProxydRoutesRejectUnknownResource(t *testing.T) {
	sd := filepath.Join(t.TempDir(), "services")
	if err := os.MkdirAll(sd, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Dir(sd), "packages/x/arizuko.yml", "scheduled_task:\n  - id: t1\n")
	if _, err := collectProxydRoutes(sd, "", []string{"x"}, map[string]string{}); err == nil {
		t.Fatal("expected error on unknown resource key, got nil")
	}
}

// C11: a per-daemon env-file write failure must fail Generate loud, never
// succeed with stale/missing daemon env files (the runed `/login` outage
// class). Blocking MkdirAll("env") by pre-seeding a same-named file
// reproduces the failure without touching permissions (portable in CI).
func TestGenerateFailsLoudOnEnvWriteFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "services"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dir, ".env"), "ASSISTANT_NAME=test\n")
	mustWrite(t, filepath.Join(dir, "env"), "not a directory")
	if _, _, err := Generate(dir); err == nil {
		t.Fatal("expected Generate to fail when per-daemon env files can't be written, got nil")
	}
}

// C8: redirect_to (part of proxyd's shape) is now accepted.
func TestProxydRoutesAcceptRedirectTo(t *testing.T) {
	sd := filepath.Join(t.TempDir(), "services")
	if err := os.MkdirAll(sd, 0o755); err != nil {
		t.Fatal(err)
	}
	// The path has to be the package's OWN ground: `/x/` is a core surface, and
	// a package declaring one is refused before redirect_to is parsed (L46).
	write(t, filepath.Dir(sd), "packages/xpkg/arizuko.yml",
		"proxyd_routes:\n  - path: /xpkg/\n    backend: http://x:8080\n    auth: public\n    redirect_to: /y/\n")
	routes, err := collectProxydRoutes(sd, "", []string{"xpkg"}, map[string]string{})
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

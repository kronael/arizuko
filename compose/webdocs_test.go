package compose

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstallWebDocs(t *testing.T) {
	src, data := t.TempDir(), t.TempDir()
	for p, c := range map[string]string{"index.html": "a", "concepts/routing.html": "b"} {
		full := filepath.Join(src, p)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dst := filepath.Join(data, "web", "pub", "arizuko")
	for range 2 { // the second run proves idempotence
		if err := InstallWebDocs(data, src); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(filepath.Join(dst, "concepts", "routing.html")); string(b) != "b" {
			t.Fatalf("nested file not installed: %q", b)
		}
	}
	// A release edit reaches the instance; an instance-only file stays.
	_ = os.WriteFile(filepath.Join(src, "index.html"), []byte("a2"), 0o644)
	_ = os.WriteFile(filepath.Join(dst, "extra.html"), []byte("x"), 0o644)
	if err := InstallWebDocs(data, src); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "index.html")); string(b) != "a2" {
		t.Errorf("edit not installed: %q", b)
	}
	if _, err := os.Stat(filepath.Join(dst, "extra.html")); err != nil {
		t.Errorf("instance file removed: %v", err)
	}
	if err := InstallWebDocs(t.TempDir(), filepath.Join(src, "absent")); err != nil {
		t.Errorf("missing src must be a no-op: %v", err)
	}
}

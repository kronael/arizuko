package main

import (
	"os"
	"path/filepath"
	"testing"
)

// C1: package names that could traverse outside services/ must be rejected.
func TestPkgNameRE(t *testing.T) {
	good := []string{"teled", "teled-rhias", "ttsd", "a_b.c", "x1"}
	bad := []string{"../docker-compose", "..", ".hidden", "a/b", "a b", "", "-lead", "a\nb"}
	for _, n := range good {
		if !pkgNameRE.MatchString(n) {
			t.Errorf("pkgNameRE rejected valid name %q", n)
		}
	}
	for _, n := range bad {
		if pkgNameRE.MatchString(n) {
			t.Errorf("pkgNameRE accepted unsafe name %q", n)
		}
	}
}

// C7: writeFileAtomic replaces content whole; a reader never sees a partial file.
func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "frag.yml")
	if err := writeFileAtomic(p, []byte("services:\n  x: {}\n")); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(p, []byte("v2\n")); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "v2\n" {
		t.Fatalf("content = %q, want v2", b)
	}
}

// C5: depends_on on a non-base, non-present service is detected (ttsd → kokoro).
func TestDependsOnParse(t *testing.T) {
	frag := []byte("services:\n  ttsd:\n    depends_on: [kokoro]\n    restart: on-failure\n")
	m := dependsOnRE.FindSubmatch(frag)
	if m == nil {
		t.Fatal("depends_on line not matched")
	}
	if got := string(m[1]); got != "kokoro" {
		t.Fatalf("dep = %q, want kokoro", got)
	}
	// a base-daemon dep is not flagged
	if baseDaemons["kokoro"] {
		t.Fatal("kokoro must not be a base daemon (it is a package)")
	}
	if !baseDaemons["routd"] {
		t.Fatal("routd must be a base daemon")
	}
}

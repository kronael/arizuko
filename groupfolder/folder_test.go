package groupfolder

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsValidFolder(t *testing.T) {
	valid := []string{"main", "main/code", "test-group", "A1_b2"}
	for _, f := range valid {
		if !isValidFolder(f) {
			t.Errorf("expected valid: %q", f)
		}
	}

	invalid := []string{
		"", " main", "main ", "../etc", "a\\b",
		".hidden", "share", "main/share", "SHARE",
		"a/" + string(make([]byte, 65)),
	}
	for _, f := range invalid {
		if isValidFolder(f) {
			t.Errorf("expected invalid: %q", f)
		}
	}
}

func TestEnsureWithinBase(t *testing.T) {
	if err := ensureWithinBase("/srv/groups", "/srv/groups/main"); err != nil {
		t.Fatal(err)
	}
	if err := ensureWithinBase("/srv/groups", "/srv/other"); err == nil {
		t.Fatal("expected error for path outside base")
	}
}

func TestResolverGroupPath(t *testing.T) {
	dir := t.TempDir()
	r := &Resolver{GroupsDir: dir, DataDir: dir}

	p, err := r.GroupPath("main")
	if err != nil {
		t.Fatal(err)
	}
	if p != filepath.Join(dir, "main") {
		t.Fatalf("unexpected path: %s", p)
	}

	_, err = r.GroupPath("../escape")
	if err == nil {
		t.Fatal("expected error for path traversal")
	}

	_, err = r.GroupPath("share")
	if err == nil {
		t.Fatal("expected error for reserved folder")
	}
}

func TestResolverIpcPath(t *testing.T) {
	dir := t.TempDir()
	r := &Resolver{GroupsDir: dir, DataDir: dir}

	p, err := r.IpcPath("main")
	if err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(dir, "ipc", "main")
	if p != expected {
		t.Fatalf("expected %s, got %s", expected, p)
	}

	_, err = r.IpcPath("")
	if err == nil {
		t.Fatal("expected error for empty folder")
	}
}

func TestResolverPathTraversal(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "groups"), 0o755)
	r := &Resolver{GroupsDir: filepath.Join(dir, "groups"), DataDir: dir}

	attacks := []string{
		"../../../etc/passwd",
		"main/../../etc",
		"main/../../../root",
	}
	for _, a := range attacks {
		_, err := r.GroupPath(a)
		if err == nil {
			t.Errorf("expected rejection for %q", a)
		}
	}
}

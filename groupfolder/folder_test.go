package groupfolder

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsValidFolder(t *testing.T) {
	valid := []string{
		"main", "main/code", "test-group", "A1_b2",
		".hidden", "tg-123456", "wa-5551234@s.whatsapp.net",
		"atlas/tg-123456",
	}
	for _, f := range valid {
		if !isValidFolder(f) {
			t.Errorf("expected valid: %q", f)
		}
	}

	invalid := []string{
		"", " main", "main ", "../etc", "a\\b",
		"share", "main/share", "SHARE",
		"a/" + string(make([]byte, 129)),
		"a/",
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
	r := &Resolver{GroupsDir: dir, IpcDir: filepath.Join(dir, "ipc")}

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
	r := &Resolver{GroupsDir: dir, IpcDir: filepath.Join(dir, "ipc")}

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

func TestIsRoot(t *testing.T) {
	if !IsRoot("main") {
		t.Fatal("main should be root")
	}
	if IsRoot("main/code") {
		t.Fatal("main/code should not be root")
	}
	if IsRoot("main/sub/deep") {
		t.Fatal("nested should not be root")
	}
}

func TestResolverPathTraversal(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "groups"), 0o755)
	r := &Resolver{GroupsDir: filepath.Join(dir, "groups"), IpcDir: filepath.Join(dir, "ipc")}

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

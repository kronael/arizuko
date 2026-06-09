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
		"*", "**", "main/*", "sub/**",
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

func TestParentOf(t *testing.T) {
	cases := []struct{ folder, want string }{
		{"main", ""},
		{"main/content", "main"},
		{"rhias/nemo", "rhias"},
		{"a/b/c", "a/b"},
		{"", ""},
	}
	for _, c := range cases {
		if got := ParentOf(c.folder); got != c.want {
			t.Errorf("ParentOf(%q) = %q, want %q", c.folder, got, c.want)
		}
	}
}

func TestNameOf(t *testing.T) {
	cases := []struct{ folder, want string }{
		{"main", "main"},
		{"main/content", "content"},
		{"a/b/c", "c"},
		{"", ""},
	}
	for _, c := range cases {
		if got := NameOf(c.folder); got != c.want {
			t.Errorf("NameOf(%q) = %q, want %q", c.folder, got, c.want)
		}
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

// ParseVhostAliases keeps valid host=world entries (lowercased host) and skips
// malformed / invalid ones loudly — a typo'd alias must never 302 traffic to
// /pub/<garbage>/ nor misreport a folder's web presence (BUG #6).
func TestParseVhostAliases(t *testing.T) {
	got := ParseVhostAliases("fab.krons.cx=atlas, FOO.example.com=bar/sub ,bad,=x,y=, ")
	want := map[string]string{"fab.krons.cx": "atlas", "foo.example.com": "bar/sub"}
	if len(got) != len(want) {
		t.Fatalf("parsed %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("alias[%q] = %q, want %q", k, got[k], v)
		}
	}

	// invalid hostname (space) + invalid world (`..`) are dropped; valid survives.
	got = ParseVhostAliases("ok.example.com=atlas,bad host=x,foo.com=../escape")
	if len(got) != 1 || got["ok.example.com"] != "atlas" {
		t.Fatalf("validation: got %v, want only {ok.example.com:atlas}", got)
	}

	if m := ParseVhostAliases(""); len(m) != 0 {
		t.Errorf("empty input → %v, want empty map", m)
	}
}

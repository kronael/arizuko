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

func TestContains(t *testing.T) {
	cases := []struct {
		parent, child string
		want          bool
	}{
		{"acme", "acme", true},
		{"acme", "acme/eng", true},
		{"acme", "acme/eng/sre", true},
		{"acme", "acmecorp", false},     // the "/" is what makes it containment
		{"acme", "acmecorp/eng", false}, // …and it must hold one level down too
		{"acme/eng", "acme", false},     // containment is not symmetric
		{"acme", "other", false},
		{"", "anything", false}, // empty parent contains nothing, never everything
		{"acme", "", false},
	}
	for _, c := range cases {
		if got := Contains(c.parent, c.child); got != c.want {
			t.Errorf("Contains(%q, %q) = %v, want %v", c.parent, c.child, got, c.want)
		}
	}
}

// WebJID and JidFolder are the write and read halves of ONE grammar, so they
// must be inverses. Seven call sites spelled `"web:" + folder` themselves
// across four packages, and this grammar has drifted before: a `"web:"+folder`
// rebuild on the read side sent a suffixed chat's frames to a hub key nobody
// held (BUGS L30, L148).
func TestWebJIDAndJidFolderAreInverses(t *testing.T) {
	for _, folder := range []string{
		"main",
		"acme/eng",
		"acme/eng/sre/oncall",
		"main/widget",
	} {
		jid := WebJID(folder)
		if got := JidFolder(jid); got != folder {
			t.Errorf("JidFolder(WebJID(%q)) = %q, want %q", folder, got, folder)
		}
	}
}

// The prefix is the grammar, so it is asserted literally: a reader outside this
// package matches on it, and changing it silently would break every stored JID.
func TestWebJIDCarriesTheWebScheme(t *testing.T) {
	if got := WebJID("acme/eng"); got != "web:acme/eng" {
		t.Fatalf("WebJID = %q, want web:acme/eng", got)
	}
}

// HookJID and JidFolder are the write and read halves of the hook grammar,
// so they must be inverses for every folder shape and a single-segment source.
func TestHookJIDAndJidFolderAreInverses(t *testing.T) {
	for _, folder := range []string{
		"main",
		"acme/eng",
		"acme/eng/sre/oncall",
	} {
		jid := HookJID(folder, "github")
		if got := JidFolder(jid); got != folder {
			t.Errorf("JidFolder(HookJID(%q, github)) = %q, want %q", folder, got, folder)
		}
	}
}

func TestHookJIDCarriesTheHookScheme(t *testing.T) {
	if got := HookJID("acme/eng", "github"); got != "hook:acme/eng/github" {
		t.Fatalf("HookJID = %q, want hook:acme/eng/github", got)
	}
}

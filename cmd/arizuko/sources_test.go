package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kronael/arizuko/container"
)

type tarEntry struct{ name, body string }

func tgzOf(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.body)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// A source names no shared checkout: catalog:, a relative path and a git URL
// other than github.com are refused before anything is installed, and the
// catalog: refusal says what to select instead.
func TestSourceRefusals(t *testing.T) {
	_, db := seedPackageInstance(t, "refused", map[string]string{"brain/arizuko.yml": "name: brain\n"})
	for src, want := range map[string]string{
		"catalog:gitd":            `select this release's service as "gitd"`,
		"catalog:brain":           "github.com/<owner>/<repo>/<dir>@<ref>",
		"packages/brain":          "must be github.com/<owner>/<repo>[/<dir>][@<ref>]",
		"git+https://h/a/b":       "must be github.com/<owner>/<repo>[/<dir>][@<ref>]",
		"https://github.com/a/b":  "must be github.com/<owner>/<repo>[/<dir>][@<ref>]",
		"brain.tgz":               "must be an absolute path or an http(s) URL",
		"github.com/a":            "names no repository",
		"github.com/a/b/../c@v1":  "bad path segment",
		"github.com/a/b@--upload": "bad ref",
		"nosuchservice":           "must be github.com/<owner>/<repo>[/<dir>][@<ref>]",
	} {
		if out := packageRefuses(t, "refused", "add", src); !strings.Contains(out, want) {
			t.Errorf("%s: output %q lacks %q", src, out, want)
		}
	}
	if got := len(selection(t, db)); got != 0 {
		t.Fatalf("a refused source was selected: %d records", got)
	}
}

func TestParseGitHubSource(t *testing.T) {
	for src, want := range map[string]gitSource{
		"github.com/kronael/arizuko/packages/macrohype@v0.75.8": {"https://github.com/kronael/arizuko", "packages/macrohype", "v0.75.8"},
		"github.com/kronael/tools":                              {"https://github.com/kronael/tools", "", ""},
		"github.com/kronael/tools@main":                         {"https://github.com/kronael/tools", "", "main"},
		"github.com/o/r/a/b.c@0123abcd":                         {"https://github.com/o/r", "a/b.c", "0123abcd"},
		"github.com/o/r/a/b":                                    {"https://github.com/o/r", "a/b", ""},
	} {
		got, err := parseGitHubSource(src)
		if err != nil || got != want {
			t.Errorf("%s: got %+v, %v; want %+v", src, got, err, want)
		}
	}
	for _, src := range []string{
		"github.com/", "github.com/o", "github.com/o/", "github.com/o/r@", "github.com/o/r/@v1",
		"github.com/o/r//sub", "github.com/o/r/./sub", "github.com/o/r/sub/..", "github.com/o/r?ref=v1",
		"github.com/o/r@-v", "github.com/o/r@a:b", "github.com/o/r@a b", "github.com/o/r@v1@v2",
		"gitlab.com/o/r",
	} {
		if got, err := parseGitHubSource(src); err == nil {
			t.Errorf("%s: accepted as %+v", src, got)
		}
	}
}

// fetchGit takes the named ref and directory from a repository, shallow, and
// records the commit; no ref is the default branch. A local bare repository
// stands in for github.com.
func TestFetchGitTakesTheRefAndDirectory(t *testing.T) {
	work, bare := t.TempDir(), t.TempDir()
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git(work, "init", "-q", "-b", "main")
	mustWrite(t, filepath.Join(work, "packages", "x", "arizuko.yml"), "name: x1\n")
	git(work, "add", ".")
	git(work, "commit", "-q", "-m", "one")
	git(work, "tag", "-a", "v1", "-m", "v1")
	tagged := git(work, "rev-parse", "HEAD")
	mustWrite(t, filepath.Join(work, "packages", "x", "arizuko.yml"), "name: x2\n")
	git(work, "commit", "-q", "-am", "two")
	head := git(work, "rev-parse", "HEAD")
	git(bare, "clone", "-q", "--bare", work, ".")

	for _, test := range []struct{ ref, revision, name string }{
		{"v1", tagged, "x1"},
		{"main", head, "x2"},
		{"", head, "x2"},
	} {
		dir, revision, cleanup, err := fetchGit(gitSource{remote: "file://" + bare, dir: "packages/x", ref: test.ref})
		if err != nil {
			t.Fatalf("ref %q: %v", test.ref, err)
		}
		m, err := container.ReadProductManifest(dir)
		cleanup()
		if err != nil || m.Name != test.name || revision != test.revision {
			t.Errorf("ref %q: name %q revision %s (%v); want %q %s", test.ref, m.Name, revision, err, test.name, test.revision)
		}
	}
	for _, source := range []gitSource{
		{remote: "file://" + bare, dir: "packages/nowhere", ref: "v1"},
		{remote: "file://" + bare, dir: "packages/x", ref: "v9"},
	} {
		if _, _, _, err := fetchGit(source); err == nil {
			t.Errorf("%+v fetched", source)
		}
	}
}

// group add -p selects the package its source declares, by that source.
func TestGroupAddSelectsProductSource(t *testing.T) {
	_, db := seedPackageInstance(t, "team", map[string]string{"brain/arizuko.yml": "name: brain\n"})
	cmdGroup([]string{"team", "add", "local:team", "team", "-p", sourceDir("brain")})
	rec, found, err := db.InstalledPackage("team", "brain")
	if err != nil || !found || rec.Source != sourceDir("brain") {
		t.Fatalf("brain selection: %+v found=%v err=%v", rec, found, err)
	}
	packageRuns(t, "team", "sync")
}

func TestTgzInstallsLocalAndRemote(t *testing.T) {
	_, db := seedPackageInstance(t, "tgz", map[string]string{})
	archive := tgzOf(t,
		tarEntry{"pkgdir/arizuko.yml", "name: fromtgz\n"},
		tarEntry{"pkgdir/skills/x/SKILL.md", "x\n"})
	sum := sha256.Sum256(archive)

	local := filepath.Join(t.TempDir(), "fromtgz.tgz")
	if err := os.WriteFile(local, archive, 0o644); err != nil {
		t.Fatal(err)
	}
	packageRuns(t, "tgz", "add", local)
	rec, found, err := db.InstalledPackage("", "fromtgz")
	if err != nil || !found || rec.Revision != hex.EncodeToString(sum[:]) {
		t.Fatalf("local tgz record: %+v found=%v err=%v", rec, found, err)
	}

	remote := tgzOf(t, tarEntry{"arizuko.yml", "name: remote\n"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(remote)
	}))
	defer srv.Close()
	packageRuns(t, "tgz", "add", srv.URL+"/remote.tgz?x=1")
	if _, found, err := db.InstalledPackage("", "remote"); err != nil || !found {
		t.Fatalf("remote tgz not installed: %v", err)
	}
	// sync re-fetches the recorded source.
	packageRuns(t, "tgz", "sync")
}

func TestTgzRefusals(t *testing.T) {
	_, db := seedPackageInstance(t, "tgzbad", map[string]string{})
	evil := tgzOf(t, tarEntry{"../escape.txt", "x"}, tarEntry{"arizuko.yml", "name: evil\n"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(evil)
	}))
	defer srv.Close()
	if out := packageRefuses(t, "tgzbad", "add", srv.URL+"/evil.tgz"); !strings.Contains(out, "unsafe archive entry") {
		t.Fatalf("output %q lacks traversal refusal", out)
	}
	if _, found, _ := db.InstalledPackage("", "evil"); found {
		t.Fatal("traversal archive installed")
	}
	for _, name := range []string{"a.zip", "a.tar.gz"} {
		if out := packageRefuses(t, "tgzbad", "add", srv.URL+"/"+name); !strings.Contains(out, "only .tgz") {
			t.Fatalf("%s: output %q lacks the format refusal", name, out)
		}
	}
	if _, _, err := unpackTgz(nil); err == nil {
		t.Fatal("empty archive unpacked")
	}
}

func TestUnpackTgzRefusesUnsafeEntries(t *testing.T) {
	for _, name := range []string{"../x", "/abs", "a/../../x"} {
		if _, cleanup, err := unpackTgz(tgzOf(t, tarEntry{name, "x"})); err == nil {
			cleanup()
			t.Errorf("entry %q accepted", name)
		}
	}
}

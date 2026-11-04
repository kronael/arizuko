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

	"github.com/kronael/arizuko/core"
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

func TestCatalogResolvesProduct(t *testing.T) {
	_, db := seedPackageInstance(t, "prod", map[string]string{})
	mustWrite(t, filepath.Join(os.Getenv("APP_SRC_DIR"), "ant", "examples", "brain", "arizuko.yml"), "name: brain\n")
	packageRuns(t, "prod", "add", "catalog:brain")
	rec, found, err := db.InstalledPackage("", "brain")
	if err != nil || !found || rec.Source != "catalog:brain" {
		t.Fatalf("brain record: %+v found=%v err=%v", rec, found, err)
	}
}

func TestCatalogNameInBothPlacesErrors(t *testing.T) {
	_, db := seedPackageInstance(t, "both", map[string]string{"dup.yml": "services:\n  dup:\n    image: x\n"})
	mustWrite(t, filepath.Join(os.Getenv("APP_SRC_DIR"), "ant", "examples", "dup", "arizuko.yml"), "name: dup\n")
	if out := packageRefuses(t, "both", "add", "catalog:dup"); !strings.Contains(out, "ambiguous") {
		t.Fatalf("output %q lacks ambiguous", out)
	}
	if _, found, _ := db.InstalledPackage("", "dup"); found {
		t.Fatal("ambiguous name installed")
	}
	if out := packageRefuses(t, "both", "add", "catalog:nowhere"); !strings.Contains(out, "no service package") {
		t.Fatalf("output %q lacks not-found", out)
	}
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

func TestGitSourcesStayGit(t *testing.T) {
	for src, git := range map[string]bool{
		"github.com/a/b":         true,
		"git@github.com:a/b.git": true,
		"git+https://h/a/b":      true,
		"https://h/a/b.git":      true,
		"http://h/a/b":           true,
		"https://h/a/b.tgz":      true, // classified git; resolveProductSource routes archives first
		"/abs/path":              false,
		"ant/examples/x":         false,
		"catalog:teled":          false,
	} {
		if isGitURL(src) != git {
			t.Errorf("isGitURL(%q) = %v", src, !git)
		}
	}
	if archiveExt("https://h/a/b.git") != "" || archiveExt("https://h/a.tgz?x=1") != ".tgz" || archiveExt("/p/a.TGZ") != ".tgz" {
		t.Error("archiveExt misclassifies")
	}

	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "x"}} {
		if args[0] == "add" {
			mustWrite(t, filepath.Join(repo, "arizuko.yml"), "name: gitpkg\n")
		}
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git %v: %v %s", args, err, out)
		}
	}
	dir, rev, cleanup := resolveProductSource(&core.Config{}, "git+file://"+repo)
	defer cleanup()
	if len(rev) != 40 {
		t.Errorf("git revision %q", rev)
	}
	if _, err := os.Stat(filepath.Join(dir, "arizuko.yml")); err != nil {
		t.Errorf("clone lacks manifest: %v", err)
	}
}

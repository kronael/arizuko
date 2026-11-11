package main

import (
	"archive/tar"
	"bytes"
	"cmp"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/kronael/arizuko/compose"
	"github.com/kronael/arizuko/container"
)

const (
	// revBundled marks a service package resolved from the release this binary
	// carries; only those may claim core route paths (stampPkgRoutes).
	revBundled = "bundled"

	archiveMaxBytes = 64 << 20
	archiveTimeout  = 60 * time.Second

	sourceForms = "github.com/<owner>/<repo>[/<dir>][@<ref>], an absolute path, " +
		"a .tgz, or the name of a service package this release ships"
)

func isHTTP(src string) bool {
	return strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://")
}

// archiveExt returns the archive suffix of src's path (a URL's query is
// ignored), or "" when it names no archive.
func archiveExt(src string) string {
	p := src
	if isHTTP(src) {
		u, err := url.Parse(src)
		if err != nil {
			return ""
		}
		p = u.Path
	}
	p = strings.ToLower(p)
	for _, ext := range []string{".tgz", ".tar.gz", ".zip"} {
		if strings.HasSuffix(p, ext) {
			return ext
		}
	}
	return ""
}

// resolveProductSource turns a package source into a local directory and a
// revision; sourceForms lists the forms. cleanup removes any temp dir this
// call made.
func resolveProductSource(src string) (dir, revision string, cleanup func()) {
	if name, ok := strings.CutPrefix(src, "catalog:"); ok {
		if _, shipped := compose.ReleaseService(name); shipped {
			die("Failed: source %q: catalog: is not a source; select this release's service as %q", src, name)
		}
		die("Failed: source %q: catalog: is not a source; install the package from "+
			"github.com/<owner>/<repo>/<dir>@<ref>", src)
	}
	if service, ok := compose.ReleaseService(src); ok {
		tmp, err := os.MkdirTemp("", "arizuko-pkg-")
		if err != nil {
			die("Failed: temp dir: %v", err)
		}
		cleanup = func() { _ = os.RemoveAll(tmp) }
		if err := os.CopyFS(tmp, service); err != nil {
			cleanup()
			die("Failed: unpack %s: %v", src, err)
		}
		return tmp, revBundled, cleanup
	}
	if ext := archiveExt(src); ext != "" {
		if ext != ".tgz" {
			die("Failed: archive %q: only .tgz archives are supported (got %s)", src, ext)
		}
		if !isHTTP(src) && !filepath.IsAbs(src) {
			die("Failed: archive %q must be an absolute path or an http(s) URL", src)
		}
		return fetchTgz(src)
	}
	if strings.HasPrefix(src, "github.com/") {
		source, err := parseGitHubSource(src)
		if err != nil {
			die("Failed: %v", err)
		}
		dir, revision, cleanup, err := fetchGit(source)
		if err != nil {
			die("Failed: fetch %s: %v", src, err)
		}
		return dir, revision, cleanup
	}
	if !filepath.IsAbs(src) {
		die("Failed: source %q must be %s", src, sourceForms)
	}
	return filepath.Clean(src), "local", func() {}
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// gitSource is a package in a git repository: the remote, the directory inside
// it that is the package root ("" is the root), and the ref ("" is the
// remote's default branch).
type gitSource struct {
	remote, dir, ref string
}

var githubSegmentRE = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// parseGitHubSource reads github.com/<owner>/<repo>[/<dir>][@<ref>], the Go
// module query form: a path, then @ and a version. On github.com the repository
// is always the first two segments, so the rest of the path is the directory.
func parseGitHubSource(src string) (gitSource, error) {
	rest, ok := strings.CutPrefix(src, "github.com/")
	if !ok {
		return gitSource{}, fmt.Errorf("source %q is not on github.com", src)
	}
	path, ref, hasRef := strings.Cut(rest, "@")
	if hasRef && (ref == "" || strings.HasPrefix(ref, "-") || strings.ContainsAny(ref, ": \t\n\\@")) {
		return gitSource{}, fmt.Errorf("source %q: bad ref %q", src, ref)
	}
	segments := strings.Split(path, "/")
	if len(segments) < 2 {
		return gitSource{}, fmt.Errorf("source %q names no repository; want %s", src, sourceForms)
	}
	for _, segment := range segments {
		if segment == "." || segment == ".." || !githubSegmentRE.MatchString(segment) {
			return gitSource{}, fmt.Errorf("source %q: bad path segment %q", src, segment)
		}
	}
	return gitSource{
		remote: "https://github.com/" + segments[0] + "/" + segments[1],
		dir:    strings.Join(segments[2:], "/"),
		ref:    ref,
	}, nil
}

// fetchGit fetches one ref of a repository, shallow, into a temp dir and
// returns the package directory inside it and the commit the ref resolved to.
// Recursive removal is of arizuko's OWN temp dir only.
func fetchGit(source gitSource) (dir, revision string, cleanup func(), err error) {
	tmp, err := os.MkdirTemp("", "arizuko-pkg-")
	if err != nil {
		return "", "", nil, err
	}
	cleanup = func() { _ = os.RemoveAll(tmp) }
	ref := cmp.Or(source.ref, "HEAD")
	for _, args := range [][]string{
		{"init", "-q"},
		{"fetch", "-q", "--depth", "1", "--", source.remote, ref},
		{"checkout", "-q", "FETCH_HEAD"},
	} {
		if _, err := runGit(tmp, args...); err != nil {
			cleanup()
			return "", "", nil, err
		}
	}
	head, err := runGit(tmp, "rev-parse", "HEAD")
	if err != nil {
		cleanup()
		return "", "", nil, err
	}
	dir = filepath.Join(tmp, filepath.FromSlash(source.dir))
	if !isDir(dir) {
		cleanup()
		return "", "", nil, fmt.Errorf("%s has no directory %q at %s", source.remote, source.dir, ref)
	}
	return dir, strings.TrimSpace(string(head)), cleanup, nil
}

// runGit runs git in dir, never prompting for credentials.
func runGit(dir string, args ...string) ([]byte, error) {
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := command.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("git %s: %w: %s", args[0], err, bytes.TrimSpace(exitErr.Stderr))
		}
		return nil, fmt.Errorf("git %s: %w", args[0], err)
	}
	return out, nil
}

// fetchTgz reads a .tgz (URL or absolute file), unpacks it into a temp dir
// and records the archive's sha256 as the revision.
func fetchTgz(src string) (dir, revision string, cleanup func()) {
	var data []byte
	var err error
	if isHTTP(src) {
		data, err = downloadArchive(src)
	} else {
		data, err = readFileCapped(src)
	}
	if err != nil {
		die("Failed: fetch %s: %v", src, err)
	}
	dir, cleanup, err = unpackTgz(data)
	if err != nil {
		die("Failed: unpack %s: %v", src, err)
	}
	sum := sha256.Sum256(data)
	return dir, hex.EncodeToString(sum[:]), cleanup
}

func readFileCapped(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return readCapped(f)
}

func readCapped(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, archiveMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > archiveMaxBytes {
		return nil, fmt.Errorf("archive larger than %d bytes", archiveMaxBytes)
	}
	return data, nil
}

func downloadArchive(src string) ([]byte, error) {
	client := &http.Client{Timeout: archiveTimeout}
	resp, err := client.Get(src)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %s", resp.Status)
	}
	return readCapped(resp.Body)
}

// unpackTgz extracts data into a fresh temp dir and returns the package root:
// the archive's single top-level directory, else the temp dir itself. An
// entry that is absolute, climbs out with `..`, or is a link is refused, and
// the unpacked size is capped.
func unpackTgz(data []byte) (dir string, cleanup func(), err error) {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return "", nil, err
	}
	tmp, err := os.MkdirTemp("", "arizuko-pkg-")
	if err != nil {
		return "", nil, err
	}
	cleanup = func() { _ = os.RemoveAll(tmp) }
	fail := func(err error) (string, func(), error) {
		cleanup()
		return "", nil, err
	}
	tr := tar.NewReader(zr)
	room := int64(archiveMaxBytes)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fail(err)
		}
		if !filepath.IsLocal(filepath.FromSlash(h.Name)) {
			return fail(fmt.Errorf("unsafe archive entry %q", h.Name))
		}
		target := filepath.Join(tmp, filepath.FromSlash(h.Name))
		switch h.Typeflag {
		case tar.TypeDir:
			err = os.MkdirAll(target, 0o755)
		case tar.TypeReg:
			if err = os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fail(err)
			}
			var n int64
			n, err = writeEntry(tr, target, room)
			room -= n
		case tar.TypeXGlobalHeader:
		default:
			err = fmt.Errorf("unsupported archive entry %q (type %c)", h.Name, h.Typeflag)
		}
		if err != nil {
			return fail(err)
		}
	}
	entries, err := os.ReadDir(tmp)
	if err != nil {
		return fail(err)
	}
	if len(entries) == 1 && entries[0].IsDir() {
		return filepath.Join(tmp, entries[0].Name()), cleanup, nil
	}
	return tmp, cleanup, nil
}

func writeEntry(r io.Reader, target string, room int64) (int64, error) {
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, io.LimitReader(r, room+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return n, err
	}
	if n > room {
		return n, fmt.Errorf("archive unpacks to more than %d bytes", archiveMaxBytes)
	}
	return n, nil
}

// readSourceManifest resolves a package source to read its arizuko.yml.
func readSourceManifest(src string) container.ProductManifest {
	dir, _, cleanup := resolveProductSource(src)
	defer cleanup()
	m, err := container.ReadProductManifest(dir)
	if err != nil {
		die("Failed: read %s: %v", src, err)
	}
	if !pkgNameRE.MatchString(m.Name) {
		die("Failed: invalid package name %q", m.Name)
	}
	return m
}

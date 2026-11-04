package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kronael/arizuko/compose"
	"github.com/kronael/arizuko/core"
)

const (
	// revCatalog marks a service fragment resolved from the bundled catalog;
	// only those may claim core route paths (stampPkgRoutes).
	revCatalog = "catalog"

	archiveMaxBytes = 64 << 20
	archiveTimeout  = 60 * time.Second
)

// isGitURL reports whether src is a remote git source rather than a local dir.
func isGitURL(src string) bool {
	return strings.HasPrefix(src, "git+") || strings.HasPrefix(src, "git@") ||
		strings.HasPrefix(src, "github.com/") || strings.Contains(src, "://")
}

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
// revision. Forms: catalog:<name> (a service fragment under template/services
// or a product under ant/examples; a name in both is an error), an absolute
// path, a path under HOST_APP_DIR, a git source, and a .tgz archive (local
// file or http(s) URL). cleanup removes any temp dir this call made.
func resolveProductSource(cfg *core.Config, src string) (dir, revision string, cleanup func()) {
	noop := func() {}
	if name, ok := strings.CutPrefix(src, "catalog:"); ok {
		mustPkgName(name)
		svc := filepath.Join(compose.CatalogDir(cfg.HostAppDir), name)
		prod := filepath.Join(cfg.EffectiveAppSrcDir(), "ant", "examples", name)
		inSvc, inProd := isDir(svc), isDir(prod)
		switch {
		case inSvc && inProd:
			die("Failed: catalog:%s is ambiguous: both %s and %s exist", name, svc, prod)
		case inSvc:
			return svc, revCatalog, noop
		case inProd:
			return prod, "release", noop
		}
		die("Failed: catalog:%s: no service package in %s and no product in %s", name, svc, prod)
	}
	ext := archiveExt(src)
	if ext != "" && ext != ".tgz" {
		die("Failed: archive %q: only .tgz archives are supported (got %s)", src, ext)
	}
	if !isGitURL(src) && !filepath.IsAbs(src) && !strings.Contains(src, "/") {
		die("Failed: source %q must be a path, URL, or catalog:<name>", src)
	}
	if ext == ".tgz" {
		return fetchTgz(cfg, src)
	}
	if isGitURL(src) {
		return cloneGit(src)
	}
	if !filepath.IsAbs(src) {
		if cfg.HostAppDir == "" {
			die("Failed: relative product source %q needs HOST_APP_DIR set in .env", src)
		}
		src = filepath.Join(cfg.HostAppDir, src)
	}
	abs, err := filepath.Abs(src)
	if err != nil {
		die("Failed: resolve source %s: %v", src, err)
	}
	return abs, "local", noop
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// cloneGit shallow-clones a git source to a temp dir and records its HEAD.
// Recursive removal is of arizuko's OWN temp dir only.
func cloneGit(src string) (dir, revision string, cleanup func()) {
	remote := strings.TrimPrefix(src, "git+")
	if strings.HasPrefix(remote, "github.com/") {
		remote = "https://" + remote
	}
	tmp, err := os.MkdirTemp("", "arizuko-pkg-")
	if err != nil {
		die("Failed: temp dir: %v", err)
	}
	cleanup = func() { _ = os.RemoveAll(tmp) }
	if out, err := exec.Command("git", "clone", "--depth", "1", remote, tmp).CombinedOutput(); err != nil {
		cleanup()
		die("Failed: git clone %s: %v\n%s", remote, err, out)
	}
	rev, err := exec.Command("git", "-C", tmp, "rev-parse", "HEAD").Output()
	if err != nil {
		cleanup()
		die("Failed: git rev-parse %s: %v", remote, err)
	}
	return tmp, strings.TrimSpace(string(rev)), cleanup
}

// fetchTgz reads a .tgz (URL or file), unpacks it into a temp dir and
// records the archive's sha256 as the revision.
func fetchTgz(cfg *core.Config, src string) (dir, revision string, cleanup func()) {
	var data []byte
	var err error
	if isHTTP(src) {
		data, err = downloadArchive(src)
	} else {
		path := src
		if !filepath.IsAbs(path) {
			if cfg.HostAppDir == "" {
				die("Failed: relative product source %q needs HOST_APP_DIR set in .env", src)
			}
			path = filepath.Join(cfg.HostAppDir, path)
		}
		data, err = readFileCapped(path)
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

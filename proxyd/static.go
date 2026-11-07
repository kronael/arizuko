package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/kronael/arizuko/auth"
)

func isDeniedPath(name string) bool {
	for _, part := range strings.Split(filepath.ToSlash(name), "/") {
		if part == ".git" || part == ".env" || strings.HasPrefix(part, ".env.") || strings.HasSuffix(part, ".db") {
			return true
		}
	}
	return false
}

type staticFile struct{ *os.File }

func (file staticFile) Readdir(int) ([]os.FileInfo, error) { return nil, os.ErrPermission }

type staticFS struct {
	root              *os.Root
	rootPath, allowed string
}

func (fs staticFS) Open(name string) (http.File, error) {
	name = strings.TrimPrefix(name, "/")
	if isDeniedPath(name) || strings.Contains(name, "\\") {
		return nil, os.ErrNotExist
	}
	file, err := fs.root.Open(name)
	if err != nil {
		return nil, err
	}
	target, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", file.Fd()))
	target = strings.TrimSuffix(target, " (deleted)")
	rel, relErr := filepath.Rel(fs.rootPath, target)
	rel = filepath.ToSlash(rel)
	if err != nil || relErr != nil || isDeniedPath(rel) || (rel != fs.allowed && !strings.HasPrefix(rel, fs.allowed+"/")) {
		// Read-only file; close errors cannot affect the response.
		_ = file.Close()
		return nil, os.ErrNotExist
	}

	return staticFile{file}, nil
}

func staticFiles(dir string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if !strings.HasPrefix(name, "pub/") && !strings.HasPrefix(name, "priv/") {
			name = "pub/" + name
		}
		if isDeniedPath(name) || strings.Contains(name, "\\") {
			http.NotFound(w, r)
			return
		}
		for _, part := range strings.Split(name, "/") {
			if part == ".." {
				http.NotFound(w, r)
				return
			}
		}
		allowed := "pub"
		if strings.HasPrefix(name, "priv/") {
			var groups []string
			if json.Unmarshal([]byte(r.Header.Get("X-User-Groups")), &groups) != nil {
				http.NotFound(w, r)
				return
			}
			parts := strings.Split(strings.TrimPrefix(name, "priv/"), "/")
			allowed = ""
			for i := range parts {
				if auth.MatchGroups(groups, strings.Join(parts[:i+1], "/")) {
					allowed = "priv/" + strings.Join(parts[:i+1], "/")
					break
				}
			}
			if allowed == "" {
				http.NotFound(w, r)
				return
			}
		}
		rootPath, err := filepath.Abs(dir)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		rootPath, err = filepath.EvalSymlinks(rootPath)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		root, err := os.OpenRoot(rootPath)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		// Read-only root; close errors cannot affect the response.
		defer root.Close() //nolint:errcheck
		req := r.Clone(r.Context())
		url := *r.URL
		req.URL = &url
		req.URL.Path = "/" + name
		req.URL.RawPath = ""
		fs := staticFS{root: root, rootPath: rootPath, allowed: allowed}
		file, err := fs.Open(name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		info, err := file.Stat()
		// Read-only file; close errors cannot affect the response.
		_ = file.Close()
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if info.IsDir() {
			if !strings.HasSuffix(r.URL.Path, "/") {
				loc := r.URL.EscapedPath() + "/"
				if r.URL.RawQuery != "" {
					loc += "?" + r.URL.RawQuery
				}
				http.Redirect(w, r, loc, http.StatusMovedPermanently)
				return
			}
			index, err := fs.Open(filepath.Join(name, "index.html"))
			if err != nil {
				http.NotFound(w, r)
				return
			}
			indexInfo, err := index.Stat()
			// Read-only file; close errors cannot affect the response.
			_ = index.Close()
			if err != nil || !indexInfo.Mode().IsRegular() {
				http.NotFound(w, r)
				return
			}
		}
		http.FileServer(fs).ServeHTTP(w, req)
	})
}

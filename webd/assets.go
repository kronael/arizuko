package main

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
)

//go:embed assets
var assetsFS embed.FS

// assetsSub is the embed-rooted view, hiding the "assets/" prefix.
var assetsSub fs.FS = mustSub(assetsFS, "assets")

func mustSub(f embed.FS, dir string) fs.FS {
	s, err := fs.Sub(f, dir)
	if err != nil {
		panic(err)
	}
	return s
}

// assetETag is the content hash of every embedded asset, computed once.
var assetETag = computeETags()

func computeETags() map[string]string {
	out := map[string]string{}
	_ = fs.WalkDir(assetsSub, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(assetsSub, p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		out[p] = `"` + hex.EncodeToString(sum[:8]) + `"`
		return nil
	})
	return out
}

// handleAssets serves embedded files under /assets/{path...}.
// Path-traversal is structurally impossible: only keys present in the
// embedded FS are served. CORS is permissive (`*`) — these are public
// shared static files (the SDK and friends), no trust surface.
func (s *server) handleAssets(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("path")
	name = strings.TrimPrefix(name, "/")
	if name == "" || strings.Contains(name, "..") {
		http.NotFound(w, r)
		return
	}

	data, err := fs.ReadFile(assetsSub, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	h := w.Header()
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	ct := mime.TypeByExtension(path.Ext(name))
	if ct == "" {
		ct = "application/octet-stream"
	}
	// mime.TypeByExtension returns `text/javascript` on some platforms;
	// normalise to the IANA-registered `application/javascript` for stability.
	if strings.HasPrefix(ct, "text/javascript") {
		ct = "application/javascript; charset=utf-8"
	}
	h.Set("Content-Type", ct)
	h.Set("Cache-Control", "public, max-age=3600")
	if etag, ok := assetETag[name]; ok {
		h.Set("ETag", etag)
		if match := r.Header.Get("If-None-Match"); match != "" && match == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	_, _ = w.Write(data)
}

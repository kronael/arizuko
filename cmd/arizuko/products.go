package main

import (
	"path/filepath"
	"strings"

	"github.com/kronael/arizuko/compose"
	"github.com/kronael/arizuko/core"
)

func resolveProductSource(cfg *core.Config, src string) (dir, revision string, cleanup func()) {
	if name, ok := strings.CutPrefix(src, "catalog:"); ok {
		mustPkgName(name)
		return filepath.Join(compose.CatalogDir(cfg.HostAppDir), name), "catalog", func() {}
	}
	if !isGitURL(src) && !filepath.IsAbs(src) && !strings.Contains(src, "/") {
		die("Failed: source %q must be a path, URL, or catalog:<name>", src)
	}
	if !isGitURL(src) && !filepath.IsAbs(src) {
		if cfg.HostAppDir == "" {
			die("Failed: relative product source %q needs HOST_APP_DIR set in .env", src)
		}
		src = filepath.Join(cfg.HostAppDir, src)
	}
	return fetchSource(src)
}

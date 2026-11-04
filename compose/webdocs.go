package compose

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// WebDocsDir returns the release's arizuko docs tree: HOST_APP_DIR's when it
// holds one, else the copy the image bakes under bakedAppSrc.
func WebDocsDir(hostAppDir string) string {
	if hostAppDir != "" {
		if dir := filepath.Join(hostAppDir, "template", "web", "pub", "arizuko"); isDir(dir) {
			return dir
		}
	}
	return filepath.Join(bakedAppSrc, "template", "web", "pub", "arizuko")
}

// InstallWebDocs copies the docs tree src into dataDir/web/pub/arizuko, the
// directory the `arizuko` skill reads. It runs on every generate, so a release
// updates every instance's docs; a file already identical is left untouched
// and a file the instance holds that the release lacks is left in place. A
// missing src is a no-op: a checkout without the tree has nothing to install.
func InstallWebDocs(dataDir, src string) error {
	if !isDir(src) {
		return nil
	}
	dst := filepath.Join(dataDir, "web", "pub", "arizuko")
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if old, err := os.ReadFile(target); err == nil && bytes.Equal(old, data) {
			return nil
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return fmt.Errorf("install docs %s: %w", rel, err)
		}
		return nil
	})
}

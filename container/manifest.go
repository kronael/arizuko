package container

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/kronael/arizuko/resreg"
	_ "github.com/kronael/arizuko/resreg/resources"
)

const ManifestFile = "arizuko.yml"

const PackageNamePattern = `^[A-Za-z0-9_][A-Za-z0-9_.-]{0,62}$`

type ProductSource = resreg.PackageSource

type ProductManifest = resreg.Manifest

func ReadManifest(path string) (ProductManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ProductManifest{}, err
	}
	manifest, err := resreg.ParseManifest(data)
	if err != nil {
		return manifest, fmt.Errorf("read %s: %w", path, err)
	}
	return manifest, nil
}

// ReadProductManifest reads source metadata and refuses nested product selections.
func ReadProductManifest(dir string) (ProductManifest, error) {
	path := filepath.Join(dir, ManifestFile)
	manifest, err := ReadManifest(path)
	if err != nil {
		return manifest, err
	}
	if manifest.Name == "" {
		return manifest, fmt.Errorf("%s requires a package name", path)
	}
	if len(manifest.Packages) > 0 {
		return manifest, fmt.Errorf("%s selects nested products; select those sources explicitly in the group's own %s",
			path, ManifestFile)
	}
	return manifest, nil
}

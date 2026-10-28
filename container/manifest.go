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

type ProductManifest = resreg.Manifest

// ReadProductManifest reads a package definition.
func ReadProductManifest(dir string) (ProductManifest, error) {
	path := filepath.Join(dir, ManifestFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return ProductManifest{}, err
	}
	manifest, err := resreg.ParseDeclaration(data)
	if err != nil {
		return manifest, fmt.Errorf("read %s: %w", path, err)
	}
	if manifest.Name == "" {
		return manifest, fmt.Errorf("%s requires a package name", path)
	}
	return manifest, nil
}

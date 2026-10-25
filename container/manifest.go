package container

import (
	"fmt"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

const ManifestFile = "arizuko.toml"

const PackageNamePattern = `^[A-Za-z0-9_][A-Za-z0-9_.-]{0,62}$`

type ProductSource struct {
	Source string `toml:"source"`
}

type ProductManifest struct {
	Name    string   `toml:"name,omitempty"`
	Brand   string   `toml:"brand,omitempty"`
	Tagline string   `toml:"tagline,omitempty"`
	Skills  []string `toml:"skills,omitempty"`
	Env     []struct {
		Key      string `toml:"key"`
		Required bool   `toml:"required"`
		Hint     string `toml:"hint"`
	} `toml:"env,omitempty"`
	Product []ProductSource `toml:"product,omitempty"`
}

// ReadProductManifest reads source metadata and refuses nested product selections.
func ReadProductManifest(dir string) (ProductManifest, error) {
	path := filepath.Join(dir, ManifestFile)
	var manifest ProductManifest
	if _, err := toml.DecodeFile(path, &manifest); err != nil {
		return manifest, fmt.Errorf("read %s: %w", path, err)
	}
	if len(manifest.Product) > 0 {
		return manifest, fmt.Errorf("%s selects nested products; select those sources explicitly in the group's own %s",
			path, ManifestFile)
	}
	return manifest, nil
}

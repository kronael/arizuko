package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/routd"
)

func readProductMix(groupDir string) (container.ProductManifest, bool) {
	path := filepath.Join(groupDir, container.ManifestFile)
	var mix container.ProductManifest
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return mix, false
	}
	var err error
	mix, err = container.ReadManifest(path)
	if err != nil {
		die("Failed: parse %s: %v", path, err)
	}
	return mix, true
}

func writeProductMix(groupDir string, mix container.ProductManifest) error {
	data, err := resreg.EncodeManifest(mix)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(groupDir, 0o755); err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(groupDir, container.ManifestFile), data)
}

func declareProduct(groupDir, productDir, source string) {
	if err := container.CheckProductAssets(source, productDir); err != nil {
		die("Failed: validate product: %v", err)
	}
	manifest, exists := readProductMix(groupDir)
	if exists {
		if len(manifest.Packages) == 1 && manifest.Packages[0].Source == source {
			return
		}
		die("Failed: %s conflicts with --product", filepath.Join(groupDir, container.ManifestFile))
	}
	manifest.Packages = []container.ProductSource{{Source: source}}
	if err := writeProductMix(groupDir, manifest); err != nil {
		die("Failed: write group manifest: %v", err)
	}
}

func resolveProductSource(cfg *core.Config, src string) (dir, revision string, cleanup func()) {
	if !isGitURL(src) && !filepath.IsAbs(src) {
		if cfg.HostAppDir == "" {
			die("Failed: relative product source %q needs HOST_APP_DIR set in .env", src)
		}
		src = filepath.Join(cfg.HostAppDir, src)
	}
	return fetchSource(src)
}

func applyProductMix(dataDir string, rdb *routd.DB, folder string) {
	if folder == "." || !groupfolder.IsValidFolder(folder) {
		die("Failed: invalid folder %q", folder)
	}
	cfg, err := core.LoadConfigFrom(dataDir)
	if err != nil {
		die("Failed: load config: %v", err)
	}
	groupDir := filepath.Join(cfg.GroupsDir, folder)
	mix, exists := readProductMix(groupDir)
	if !exists {
		die("Failed: %s has no %s", groupDir, container.ManifestFile)
	}
	records, err := rdb.InstalledPackages()
	if err != nil {
		die("Failed: list packages: %v", err)
	}
	prior := map[string]map[string]string{}
	for _, record := range records {
		if record.Folder == folder {
			prior[record.Name] = record.AssetHashes
		}
	}
	var products []container.Product
	for _, entry := range mix.Packages {
		if entry.Source == "" {
			die("Failed: empty product source")
		}
		dir, revision, cleanup := resolveProductSource(cfg, entry.Source)
		defer cleanup()
		candidate, err := preparePackageCandidate(dir, entry.Source, revision)
		if err == nil {
			err = installManagedPackage(dataDir, folder, rdb, candidate, entry.Folders)
		}
		if err != nil {
			die("Failed: install %s: %v", entry.Source, err)
		}
		products = append(products, container.Product{Name: candidate.name, Dir: candidate.dir})
	}
	blended, err := container.ComposeGroup(cfg, folder, products, prior)
	if err != nil {
		die("Failed: blend %s: %v", folder, err)
	}
	for _, blend := range blended {
		record, _, err := rdb.InstalledPackage(folder, blend.Name)
		if err != nil {
			die("Failed: read package: %v", err)
		}
		for key, value := range blend.Manifest {
			record.Manifest[key] = value
		}
		for key, value := range blend.AssetHashes {
			record.AssetHashes[key] = value
		}
		if err := rdb.PutInstalledPackage(record); err != nil {
			die("Failed: record %s: %v", blend.Name, err)
		}
		for _, warning := range blend.Warnings {
			fmt.Fprintf(os.Stderr, "warning: %s\n", warning)
		}
	}
	for _, record := range records {
		if record.Folder == folder && !slices.ContainsFunc(products, func(product container.Product) bool {
			return product.Name == record.Name
		}) {
			if _, err := rdb.DeleteInstalledPackage(folder, record.Name); err != nil {
				die("Failed: remove %s: %v", record.Name, err)
			}
		}
	}
	fmt.Printf("composed %s from %d product(s)\n", folder, len(products))
}

func installGroupPackage(dataDir, folder string, candidate packageCandidate, rdb *routd.DB) {
	cfg, err := core.LoadConfigFrom(dataDir)
	if err != nil {
		die("Failed: load config: %v", err)
	}
	groupDir := filepath.Join(cfg.GroupsDir, folder)
	mix, _ := readProductMix(groupDir)
	if !slices.ContainsFunc(mix.Packages, func(entry container.ProductSource) bool {
		return entry.Source == candidate.source
	}) {
		mix.Packages = append(mix.Packages, container.ProductSource{Source: candidate.source})
	}
	if err := writeProductMix(groupDir, mix); err != nil {
		die("Failed: write group manifest: %v", err)
	}
	applyProductMix(dataDir, rdb, folder)
}

func listProducts(dataDir string, w io.Writer) error {
	cfg, err := core.LoadConfigFrom(dataDir)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if cfg.HostAppDir == "" {
		return errNoHostAppDir
	}
	root := filepath.Join(cfg.HostAppDir, "ant", "examples")
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("read catalog: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		manifest, err := container.ReadProductManifest(filepath.Join(root, entry.Name()))
		line := fmt.Sprintf("%-14s %-10s %s\n", manifest.Name, manifest.Brand, manifest.Tagline)
		if err != nil {
			line = fmt.Sprintf("%-14s (no readable %s: %v)\n", entry.Name(), container.ManifestFile, err)
		} else if manifest.Name != entry.Name() {
			return fmt.Errorf("product %s: %s declares name %q; the directory name is the catalog selector",
				entry.Name(), container.ManifestFile, manifest.Name)
		}
		if _, err := io.WriteString(w, line); err != nil {
			return fmt.Errorf("write catalog: %w", err)
		}
	}
	return nil
}

var errNoHostAppDir = errors.New("HOST_APP_DIR is unset in .env — it names the bundled catalog")

func cmdProducts(args []string) {
	usage := "arizuko products <instance> list | apply <folder>"
	need(args, 2, usage)
	dataDir := mustInstanceDir(args[0])
	switch args[1] {
	case "list":
		if err := listProducts(dataDir, os.Stdout); err != nil {
			die("Failed: products list: %v", err)
		}
	case "apply":
		need(args, 3, usage)
		rdb := mustOpenRoutd()
		defer func() { _ = rdb.Close() }()
		applyProductMix(dataDir, rdb, args[2])
	default:
		die("usage: " + usage)
	}
}

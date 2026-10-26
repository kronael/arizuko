package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/routd"
)

func readProductMix(groupDir string) (container.ProductManifest, bool) {
	path := filepath.Join(groupDir, container.ManifestFile)
	legacy := filepath.Join(groupDir, "products.toml")
	if _, err := os.Lstat(legacy); err == nil {
		die("Failed: move or merge entries from %s into %s, then remove %s", legacy, path, legacy)
	} else if !os.IsNotExist(err) {
		die("Failed: check %s: %v", legacy, err)
	}
	var mix container.ProductManifest
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return mix, false
	}
	if _, err := toml.DecodeFile(path, &mix); err != nil {
		die("Failed: parse %s: %v — edit the group manifest and run products apply", path, err)
	}
	return mix, true
}

func declareProduct(groupDir, productDir, source string) {
	if err := container.CheckProductAssets(source, productDir); err != nil {
		die("Failed: validate product: %v", err)
	}
	manifest, exists := readProductMix(groupDir)
	path := filepath.Join(groupDir, container.ManifestFile)
	if exists {
		if len(manifest.Product) == 1 && manifest.Product[0].Source == source {
			return
		}
		die("Failed: %s conflicts with --product; edit the group manifest and run products apply", path)
	}
	manifest.Product = []container.ProductSource{{Source: source}}
	var encoded strings.Builder
	if err := toml.NewEncoder(&encoded).Encode(manifest); err != nil {
		die("Failed: encode %s: %v", path, err)
	}
	if err := os.MkdirAll(groupDir, 0o755); err != nil {
		die("Failed: mkdir %s: %v", groupDir, err)
	}
	if err := os.WriteFile(path, []byte(encoded.String()), 0o644); err != nil {
		die("Failed: write %s: %v", path, err)
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
	lock := lockPackageOperation(dataDir)
	// Closing the lock releases this operation's exclusive ownership.
	defer lock.Close() //nolint:errcheck
	applyProductMixLocked(dataDir, rdb, folder, nil, nil)
}

func applyProductMixLocked(dataDir string, rdb *routd.DB, folder string,
	replacement []byte, prepared *packageCandidate) {
	if err := container.CheckPendingPackageAdoption(packageStore(rdb), nil); err != nil {
		die("Failed: %v", err)
	}
	if folder == "." || !groupfolder.IsValidFolder(folder) {
		die("Failed: invalid folder %q", folder)
	}
	cfg, err := core.LoadConfigFrom(dataDir)
	if err != nil {
		die("Failed: load config: %v", err)
	}
	groupDir := filepath.Join(cfg.GroupsDir, folder)
	mix, exists := readProductMix(groupDir)
	if replacement != nil {
		mix = container.ProductManifest{}
		if _, err := toml.Decode(string(replacement), &mix); err != nil {
			die("Failed: parse replacement selection: %v", err)
		}
		exists = true
	}
	records, err := rdb.InstalledPackages()
	if err != nil {
		die("Failed: list installed products: %v", err)
	}
	if len(mix.Product) == 0 {
		for _, record := range records {
			if record.Folder == folder && len(record.Manifest[manifestArtifact]) == 0 &&
				!container.HasPendingSettings(record.AssetHashes) {
				die("Failed: %s has installed products; declare their selections explicitly in %s before apply or sync",
					folder, filepath.Join(groupDir, container.ManifestFile))
			}
		}
	}
	if !exists {
		die("Failed: %s has no %s — write the mix first", groupDir, container.ManifestFile)
	}
	var candidates []packageCandidate
	var products []container.Product
	prior := map[string]map[string]string{}
	for _, record := range records {
		if record.Folder == folder {
			prior[record.Name] = record.AssetHashes
		}
	}
	var existing []packageCandidate
	if prepared != nil {
		selected, err := container.ReadPackageSelection(dataDir, folder)
		if err != nil {
			die("Failed: read active packages: %v", err)
		}
		for _, snapshot := range selected {
			if snapshot.Name == prepared.snapshot.Name {
				continue
			}
			record, found, err := rdb.InstalledPackage(folder, snapshot.Name)
			if err != nil || !found || record.AssetHashes[manifestArtifact] != snapshot.Digest ||
				container.HasPendingSettings(record.AssetHashes) {
				die("Failed: incomplete package operation: repair the receipt for %s first", snapshot.Name)
			}
			dir, err := container.ResolvePackage(dataDir, snapshot)
			if err != nil {
				die("Failed: resolve %s: %v", snapshot.Name, err)
			}
			existing = append(existing, packageCandidate{snapshot, dir, record.Source, record.Revision})
		}
	}
	for _, entry := range mix.Product {
		if entry.Source == "" {
			die("Failed: %s/%s has a [[product]] with no source", groupDir, container.ManifestFile)
		}
		var candidate packageCandidate
		if prepared != nil && entry.Source == prepared.source {
			candidate = *prepared
		} else if index := slices.IndexFunc(existing, func(candidate packageCandidate) bool {
			return candidate.source == entry.Source
		}); index >= 0 {
			candidate = existing[index]
		} else {
			dir, revision, cleanup := resolveProductSource(cfg, entry.Source)
			defer cleanup()
			candidate, err = preparePackageCandidate(dataDir, dir, entry.Source, revision)
			if err != nil {
				die("Failed: %v", err)
			}
		}
		record, _, err := rdb.InstalledPackage(folder, candidate.snapshot.Name)
		if err != nil {
			die("Failed: read receipt: %v", err)
		}
		prior[candidate.snapshot.Name] = record.AssetHashes
		candidates = append(candidates, candidate)
		products = append(products, container.Product{Name: candidate.snapshot.Name,
			Dir: candidate.dir, Snapshot: &candidate.snapshot})
	}
	if err := checkPackageCandidates(dataDir, folder, rdb, candidates); err != nil {
		die("Failed: preflight packages: %v", err)
	}
	if err := checkPendingSettings(dataDir, folder, records, candidates); err != nil {
		die("Failed: %v", err)
	}
	blend, err := container.PrepareProductBlend(groupDir, products, prior)
	if err != nil {
		die("Failed: blend %s: %v", folder, err)
	}
	if err := prepareSettingsReceipts(folder, rdb, candidates, blend.Blended); err != nil {
		die("Failed: %v", err)
	}
	blended, err := blend.Apply()
	if err != nil {
		die("Failed: blend %s: %v", folder, err)
	}
	if replacement != nil {
		if err := writeFileAtomic(filepath.Join(groupDir, container.ManifestFile), replacement); err != nil {
			die("Failed: write group selection: %v", err)
		}
	}
	receipts := make([]routd.InstalledPackage, len(blended))
	for i, blended := range blended {
		receipts[i] = routd.InstalledPackage{Manifest: blended.Manifest, AssetHashes: blended.AssetHashes}
		for _, warning := range blended.Warnings {
			fmt.Fprintf(os.Stderr, "warning: %s\n", warning)
		}
	}
	if err := activatePackageCandidates(dataDir, folder, rdb, candidates, receipts); err != nil {
		die("Failed: %v", err)
	}
	for _, record := range records {
		if record.Folder != folder || (len(record.Manifest[manifestArtifact]) == 0 &&
			!container.HasPendingSettings(record.AssetHashes)) {
			continue
		}
		if !slices.ContainsFunc(candidates, func(candidate packageCandidate) bool {
			return candidate.snapshot.Name == record.Name
		}) {
			if _, err := rdb.DeleteInstalledPackage(folder, record.Name); err != nil {
				die("Failed: incomplete package operation: %s is inactive; receipt cleanup failed: %v", record.Name, err)
			}
		}
	}
	if len(candidates) == 0 {
		fmt.Printf("no products selected for %s\n", folder)
	} else {
		fmt.Printf("composed %s from %d product(s)\n", folder, len(candidates))
	}
}

func installGroupPackage(dataDir, folder string, candidate packageCandidate, rdb *routd.DB) {
	cfg, err := core.LoadConfigFrom(dataDir)
	if err != nil {
		die("Failed: load config: %v", err)
	}
	groupDir := filepath.Join(cfg.GroupsDir, folder)
	mix, _ := readProductMix(groupDir)
	name, source := candidate.snapshot.Name, candidate.source
	index := len(mix.Product)
	for i, entry := range mix.Product {
		if entry.Source == source {
			index = i
			break
		}
		record, ok, err := rdb.InstalledPackage(folder, name)
		if err != nil {
			die("Failed: read receipt: %v", err)
		}
		if ok && entry.Source == record.Source {
			index = i
			break
		}
	}
	path := filepath.Join(groupDir, container.ManifestFile)
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		die("Failed: read %s: %v", path, err)
	}
	updated, err := editProductSelection(data, index, source)
	if err != nil {
		die("Failed: edit %s: %v", path, err)
	}
	applyProductMixLocked(dataDir, rdb, folder, updated, &candidate)
}

// listProducts enumerates the bundled catalog under `<HOST_APP_DIR>/ant/examples`.
// A directory without a readable arizuko.toml is reported rather than skipped: the
// manifest is what makes a directory a product, so a silent omission would read
// as "not shipped" when the truth is "malformed".
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
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m, manifestErr := container.ReadProductManifest(filepath.Join(root, e.Name()))
		line := ""
		if manifestErr != nil {
			line = fmt.Sprintf("%-14s (no readable %s: %v)\n", e.Name(), container.ManifestFile, manifestErr)
		} else if m.Name != e.Name() {
			// The directory is what `create --product` accepts, so the directory
			// IS the identity. A manifest declaring a different name advertises
			// a product no command takes, and product-mix state keys the third
			// spelling (BUGS J12). Refuse instead of printing the unusable one.
			return fmt.Errorf("product %s: %s declares name %q — the directory name is the identity `create --product` takes",
				e.Name(), container.ManifestFile, m.Name)
		} else {
			line = fmt.Sprintf("%-14s %-10s %s\n", e.Name(), m.Brand, m.Tagline)
		}
		// A dropped write means the operator sees a SHORTER catalog and cannot
		// tell — the same silent-omission failure the malformed-manifest line
		// exists to prevent (BUGS J13).
		if _, err := io.WriteString(w, line); err != nil {
			return fmt.Errorf("write catalog: %w", err)
		}
	}
	return nil
}

var errNoHostAppDir = errors.New("HOST_APP_DIR is unset in .env — it names the bundled catalog")

// cmdProducts lists the bundled catalog or applies a group's `arizuko.toml` mix.
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

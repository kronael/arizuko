package main

// products.go is spec 5/28-packages's composition reader: a group's `arizuko.toml` is
// the ORDERED MIX of products blended into it. It is the group-scoped half of
// the same lifecycle `packages install` runs instance-wide — same lock
// (installed_packages, keyed `(folder, name)`), same source resolution, same
// dirty-refusal — so `folder` is the only thing that differs between them.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/routd"
)

// readProductMix parses a group's arizuko.toml. Absent file → ok=false, which
// is not an error: most groups have no mix.
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

// resolveProductSource turns one `source =` into a local dir + revision. A
// relative local path is joined to HostAppDir so `ant/examples/trip` names the
// bundled corpus; identity is configured (HOST_APP_DIR), never derived from
// where this process happens to run.
func resolveProductSource(cfg *core.Config, src string) (dir, revision string, cleanup func()) {
	if !isGitURL(src) && !filepath.IsAbs(src) {
		if cfg.HostAppDir == "" {
			die("Failed: relative product source %q needs HOST_APP_DIR set in .env", src)
		}
		src = filepath.Join(cfg.HostAppDir, src)
	}
	return fetchSource(src)
}

// productName reads the mix entry's identity from its arizuko.toml — the one
// manifest a product ships (spec 5/28-packages). No fallback to the directory name: the
// name is the record key and the CLAUDE.md region id, so guessing it would key
// the lock on where a clone happened to land.
func productName(dir, src string) string {
	m, err := container.ReadProductManifest(dir)
	if err != nil {
		die("Failed: read %s for %s: %v", container.ManifestFile, src, err)
	}
	if m.Name == "" {
		die("Failed: %s for %s has no `name` — it is the record key", container.ManifestFile, src)
	}
	mustPkgName(m.Name)
	return m.Name
}

// applyProductMix blends one group's declared mix and records each product under
// `(folder, name)`. This is the ONLY writer of a non-empty installed_packages
// folder; `arizuko packages install` writes InstanceWide because compose
// fragments, proxyd routes and host files belong to no group.
func applyProductMix(dataDir string, rdb *routd.DB, folder string) {
	if !groupfolder.IsValidFolder(folder) {
		die("Failed: invalid folder %q", folder)
	}
	cfg, err := core.LoadConfigFrom(dataDir)
	if err != nil {
		die("Failed: load config: %v", err)
	}
	groupDir := filepath.Join(cfg.GroupsDir, folder)
	mix, ok := readProductMix(groupDir)
	if len(mix.Product) == 0 {
		records, err := rdb.InstalledPackages()
		if err != nil {
			die("Failed: list installed products: %v", err)
		}
		for _, record := range records {
			if record.Folder == folder {
				die("Failed: %s has installed products; declare their selections explicitly in %s before apply or sync",
					folder, filepath.Join(groupDir, container.ManifestFile))
			}
		}
		if !ok {
			die("Failed: %s has no %s — write the mix first (one [[product]] block per entry)",
				groupDir, container.ManifestFile)
		}
		fmt.Printf("no products selected for %s\n", folder)
		return
	}

	prior := map[string]map[string]string{}
	products := make([]container.Product, 0, len(mix.Product))
	sources := make([]string, 0, len(mix.Product))
	revisions := make([]string, 0, len(mix.Product))
	for _, entry := range mix.Product {
		if entry.Source == "" {
			die("Failed: %s/%s has a [[product]] with no source", groupDir, container.ManifestFile)
		}
		dir, revision, cleanup := resolveProductSource(cfg, entry.Source)
		defer cleanup()
		name := productName(dir, entry.Source)
		rec, found, rerr := rdb.InstalledPackage(folder, name)
		if rerr != nil {
			die("Failed: read record for %s: %v", name, rerr)
		}
		if found {
			prior[name] = rec.AssetHashes
		}
		products = append(products, container.Product{Name: name, Dir: dir})
		sources = append(sources, entry.Source)
		revisions = append(revisions, revision)
	}

	blended, err := container.ComposeGroup(cfg, folder, products, prior)
	if err != nil {
		die("Failed: blend %s: %v", folder, err)
	}
	for i, b := range blended {
		for _, w := range b.Warnings {
			fmt.Fprintf(os.Stderr, "warning: %s\n", w)
		}
		if len(b.Dirty) > 0 {
			fmt.Printf("%-14s SKIPPED managed asset(s) edited locally: %s\n", b.Name, strings.Join(b.Dirty, ", "))
		}
		if err := rdb.PutInstalledPackage(routd.InstalledPackage{
			Folder:      folder,
			Name:        b.Name,
			Source:      sources[i],
			Revision:    revisions[i],
			Manifest:    b.Manifest,
			AssetHashes: b.AssetHashes,
			InstalledAt: time.Now().UTC().Format(time.RFC3339),
		}); err != nil {
			die("Failed: record %s in %s: %v", b.Name, folder, err)
		}
		state := "unchanged"
		if b.Changed {
			state = "applied"
		}
		fmt.Printf("%-14s %s (%s@%s)\n", b.Name, state, sources[i], revisions[i])
	}
	fmt.Printf("composed %s from %d product(s)\n", folder, len(blended))
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

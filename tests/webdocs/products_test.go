package webdocs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const examplesDir = "../../ant/examples"

// shapeMarker is how products/shapes.html declares that its products are
// assembled from the primitives and ship no tree.
const shapeMarker = "This is a shape, not a template"

// TestProductCatalogMatchesCorpus is BUGS L38: `ant/examples/` shipped 11
// products, the catalog documented 9, and only 5 were in both. A shipped
// product needs a page pair, because `products list` prints its name. A
// product with no tree is a section of shapes.html, which says so, and never
// a page pair, because a page pair reads as a template.
func TestProductCatalogMatchesCorpus(t *testing.T) {
	productsDir := filepath.Join(pubDir, siteRoot, "products")
	shipped := map[string]bool{}
	entries, err := os.ReadDir(examplesDir)
	if err != nil {
		t.Fatalf("read %s: %v", examplesDir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			shipped[e.Name()] = true
		}
	}
	documented := map[string]bool{}
	pages, err := os.ReadDir(productsDir)
	if err != nil {
		t.Fatalf("read %s: %v", productsDir, err)
	}
	for _, e := range pages {
		if e.IsDir() {
			documented[e.Name()] = true
		}
	}
	if len(shipped) < 5 || len(documented) < 5 {
		t.Fatalf("%d shipped, %d documented — a path is wrong and this would pass vacuously",
			len(shipped), len(documented))
	}

	for name := range shipped {
		for _, page := range []string{"index.html", "setup.html"} {
			if _, err := os.Stat(filepath.Join(productsDir, name, page)); err != nil {
				t.Errorf("product %q ships but has no catalog %s — `products list` names it and the site does not "+
					"(template/web/CLAUDE.md §\"Adding a new product\")", name, page)
			}
		}
	}
	for name := range documented {
		if !shipped[name] {
			t.Errorf("products/%s/ has no tree in ant/examples/ — a shape is a section of products/shapes.html", name)
		}
	}

	shapes, err := os.ReadFile(filepath.Join(productsDir, "shapes.html"))
	if err != nil {
		t.Fatalf("read shapes.html: %v", err)
	}
	if !strings.Contains(string(shapes), shapeMarker) {
		t.Errorf("shapes.html does not carry the %q note", shapeMarker)
	}
	for _, m := range idRe.FindAllStringSubmatch(string(shapes), -1) {
		if shipped[m[1]] {
			t.Errorf("shapes.html has a section %q, and that product ships a tree", m[1])
		}
	}
}

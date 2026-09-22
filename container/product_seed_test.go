package container

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kronael/arizuko/core"
)

// examplesDir is the shipped product corpus (spec 5/21) that
// `arizuko create --product` seeds from.
const examplesDir = "../ant/examples"

func hashFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// walkFiles returns every regular file under root, keyed by its path relative
// to root, mapped to its content hash.
func walkFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		out[rel] = hashFile(t, path)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}

// TestProductSeedIsVerbatim locks today's seed: SetupGroup copies a product tree into
// the group folder byte-for-byte, every file, no filter.
//
// This is the regression bar for spec 5/28-packages's composition blend. The blend table names
// eight payloads, but the shipped corpus also ships PRODUCT.md (11/11), SOUL.md (6/11)
// and BRANDING.md (3/11). A table-strict blend treating the table as a whitelist would
// silently DROP those.
//
// Verbatim is about BYTES, not the author's directory layout: a skill relocates under
// .claude/ here exactly as in the blend, because that is the one place a skill is read
// (BUGS L37).
func TestProductSeedIsVerbatim(t *testing.T) {
	products, err := os.ReadDir(examplesDir)
	if err != nil {
		t.Fatalf("read %s: %v", examplesDir, err)
	}
	seen := 0
	for _, p := range products {
		if !p.IsDir() {
			continue
		}
		seen++
		t.Run(p.Name(), func(t *testing.T) {
			seedDir := filepath.Join(examplesDir, p.Name())
			want := walkFiles(t, seedDir)
			if len(want) == 0 {
				t.Fatalf("product %s has no files — corpus fixture is empty", p.Name())
			}

			tmp := t.TempDir()
			cfg := &core.Config{
				GroupsDir: filepath.Join(tmp, "groups"),
				IpcDir:    filepath.Join(tmp, "ipc"),
			}
			if err := os.MkdirAll(cfg.GroupsDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(cfg.IpcDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := SetupGroup(cfg, "seeded", seedDir); err != nil {
				t.Fatalf("SetupGroup: %v", err)
			}

			got := walkFiles(t, filepath.Join(cfg.GroupsDir, "seeded"))
			for rel, wantHash := range want {
				dst := filepath.FromSlash(productDest(filepath.ToSlash(rel)))
				gotHash, ok := got[dst]
				if !ok {
					t.Errorf("seed DROPPED %s (expected at %s)", rel, dst)
					continue
				}
				if gotHash != wantHash {
					t.Errorf("seed altered %s: %s != %s", rel, gotHash, wantHash)
				}
				if dst != rel && got[rel] != "" {
					t.Errorf("seed ALSO left %s at the group root — one placement rule, not two", rel)
				}
			}
		})
	}
	if seen == 0 {
		t.Fatal("no products found — corpus path wrong, test would pass vacuously")
	}
}

// TestProductMixSeedsCorpusVerbatim is the same bar applied to the composition engine:
// every corpus file must survive a mix of ONE byte-identical. CLAUDE.md is the single
// exception the blend table mandates — it concatenates, so it lands inside its product's
// marked region. Without the table's catch-all row this drops PRODUCT.md for 10/10.
func TestProductMixSeedsCorpusVerbatim(t *testing.T) {
	products, err := os.ReadDir(examplesDir)
	if err != nil {
		t.Fatalf("read %s: %v", examplesDir, err)
	}
	seen := 0
	for _, p := range products {
		if !p.IsDir() {
			continue
		}
		seen++
		t.Run(p.Name(), func(t *testing.T) {
			seedDir := filepath.Join(examplesDir, p.Name())
			want := walkFiles(t, seedDir)
			if len(want) == 0 {
				t.Fatalf("product %s has no files — corpus fixture is empty", p.Name())
			}
			group := filepath.Join(t.TempDir(), "group")
			if _, err := BlendProducts(group, []Product{{Name: p.Name(), Dir: seedDir}}, nil); err != nil {
				t.Fatalf("BlendProducts: %v", err)
			}
			got := walkFiles(t, group)
			for rel, wantHash := range want {
				if rel == "CLAUDE.md" {
					body, rerr := os.ReadFile(filepath.Join(seedDir, "CLAUDE.md"))
					if rerr != nil {
						t.Fatal(rerr)
					}
					region := regionBegin(p.Name()) + "\n" +
						strings.TrimRight(string(body), "\n") + "\n" + regionEnd(p.Name())
					blended, rerr := os.ReadFile(filepath.Join(group, "CLAUDE.md"))
					if rerr != nil {
						t.Fatalf("blend DROPPED CLAUDE.md: %v", rerr)
					}
					if !strings.Contains(string(blended), region) {
						t.Errorf("CLAUDE.md region altered the product's bytes")
					}
					continue
				}
				// Two payload kinds transform BY DESIGN, so "same path, same bytes" is the wrong
				// assertion for them: a skill is relocated under .claude/, and settings.json is a
				// key union that is re-serialised. Assert the transformed result instead of
				// exempting them, or the reference product would silently lose either payload.
				if strings.HasPrefix(rel, "skills/") {
					moved := productDest(rel)
					if got[moved] != wantHash {
						t.Errorf("skill %s did not land verbatim at %s", rel, moved)
					}
					continue
				}
				if rel == settingsRel {
					var seedKeys, blendedKeys map[string]any
					readJSON(t, filepath.Join(seedDir, rel), &seedKeys)
					readJSON(t, filepath.Join(group, rel), &blendedKeys)
					for k, v := range seedKeys {
						if fmt.Sprint(blendedKeys[k]) != fmt.Sprint(v) {
							t.Errorf("settings key %q: got %v, want %v", k, blendedKeys[k], v)
						}
					}
					continue
				}
				gotHash, ok := got[rel]
				if !ok {
					t.Errorf("blend DROPPED %s", rel)
					continue
				}
				if gotHash != wantHash {
					t.Errorf("blend altered %s: %s != %s", rel, gotHash, wantHash)
				}
			}
		})
	}
	if seen == 0 {
		t.Fatal("no products found — corpus path wrong, test would pass vacuously")
	}
}

// TestBothSeedersPlaceAlike is BUGS L37's regression bar: the verbatim seed and
// the blend must put a product's payload in the SAME place, because a product
// that means one thing under `group add --product` and another under `products
// apply` is two products. The skill is the file that broke it — the verbatim
// path left it at the group root, where nothing reads it.
func TestBothSeedersPlaceAlike(t *testing.T) {
	prod := t.TempDir()
	for rel, body := range map[string]string{
		"PRODUCT.md":             "name = \"pp\"\n",
		"skills/triage/SKILL.md": "---\nname: triage\n---\nBody.\n",
		"facts/sla.md":           "Four hours.\n",
	} {
		path := filepath.Join(prod, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	tmp := t.TempDir()
	cfg := &core.Config{GroupsDir: filepath.Join(tmp, "groups"), IpcDir: filepath.Join(tmp, "ipc")}
	if err := SetupGroup(cfg, "verbatim", prod); err != nil {
		t.Fatalf("SetupGroup: %v", err)
	}
	seeded := walkFiles(t, filepath.Join(cfg.GroupsDir, "verbatim"))

	blended := filepath.Join(t.TempDir(), "group")
	if _, err := BlendProducts(blended, []Product{{Name: "pp", Dir: prod}}, nil); err != nil {
		t.Fatalf("BlendProducts: %v", err)
	}
	mixed := walkFiles(t, blended)

	// The skill is the whole point: assert its destination explicitly rather
	// than only that the two agree, so both agreeing on the WRONG place fails.
	const skill = ".claude/skills/triage/SKILL.md"
	for path, set := range map[string]map[string]string{"verbatim seed": seeded, "blend": mixed} {
		if set[skill] == "" {
			t.Errorf("%s: skill did not land at %s", path, skill)
		}
		if set["skills/triage/SKILL.md"] != "" {
			t.Errorf("%s: skill left at the group root, where nothing reads it", path)
		}
	}
	for rel, h := range mixed {
		if rel == "CLAUDE.md" {
			continue // the blend's one concatenating payload; this product ships none
		}
		if seeded[rel] != h {
			t.Errorf("blend put %s where the verbatim seed did not (seed has %q)", rel, seeded[rel])
		}
	}
}

// TestProductCorpusPayloadKinds pins the payload kinds the shipped corpus
// actually uses, so spec 5/28-packages's blend table can be checked against reality
// rather than against an earlier agent's recollection. Counts are asserted, not
// described: if a product adds SOUL.md the count moves and the spec must too.
func TestProductCorpusPayloadKinds(t *testing.T) {
	products, err := os.ReadDir(examplesDir)
	if err != nil {
		t.Fatalf("read %s: %v", examplesDir, err)
	}
	counts := map[string]int{}
	total := 0
	for _, p := range products {
		if !p.IsDir() {
			continue
		}
		total++
		for _, name := range []string{"PRODUCT.md", "SOUL.md", "PERSONA.md", "BRANDING.md", "CLAUDE.md"} {
			if _, err := os.Stat(filepath.Join(examplesDir, p.Name(), name)); err == nil {
				counts[name]++
			}
		}
	}
	want := map[string]int{
		"PRODUCT.md":  11, // every product
		"SOUL.md":     6,
		"PERSONA.md":  5,
		"BRANDING.md": 3,
		"CLAUDE.md":   3,
	}
	if total != 11 {
		t.Fatalf("corpus size moved: %d products, spec 5/28 says 11", total)
	}
	for name, wantN := range want {
		if counts[name] != wantN {
			t.Errorf("%s: corpus has %d, spec 5/28 says %d", name, counts[name], wantN)
		}
	}
	// `helpdesk` is the reference product: it exercises every payload kind that
	// has a consumer, so the blend table is no longer hypothetical for skills or
	// settings. Assert it keeps doing that — a reference that quietly loses a
	// payload stops being one.
	for _, rel := range []string{".claude/settings.json", "skills/triage/SKILL.md", "facts/sla.md", "eval/helpdesk.toml"} {
		if _, err := os.Stat(filepath.Join(examplesDir, "helpdesk", rel)); err != nil {
			t.Errorf("helpdesk is the reference product and no longer ships %s: %v", rel, err)
		}
	}
	// Kinds the blend table names that the corpus does not ship. `tasks.toml` and
	// `migrations/` are the two REFUSED payloads (BUGS L35), so a corpus product
	// shipping one would not apply at all. Paths are product-relative.
	for _, absent := range []string{"tasks.toml", "Dockerfile.ant", "migrations"} {
		for _, p := range products {
			if !p.IsDir() {
				continue
			}
			if _, err := os.Stat(filepath.Join(examplesDir, p.Name(), absent)); err == nil {
				t.Errorf("%s now ships %s — spec 5/28's corpus gap is stale", p.Name(), absent)
			}
		}
	}
}

// productsWebDir is the operator-facing product catalog (template/web/CLAUDE.md
// §"Adding a new product"). It is source-of-truth for the published site.
const productsWebDir = "../template/web/pub/arizuko/products"

// shapeMarker is how a catalog page declares it documents a shape assembled
// from the primitives rather than a tree that ships. A page without it is
// claiming `--product <id>` works.
const shapeMarker = "This is a shape, not a template"

// TestProductCatalogMatchesCorpus is BUGS L38: `ant/examples/` shipped 11 products, the
// catalog documented 9, and only 5 were in both. The ritual existed and was not
// followed, so the assertion is what keeps the two sets together.
//
// The rule is directional, because the two directions fail differently. A shipped
// product MUST have a page pair, since `products list` prints its name. A documented
// product with no tree is legal — the catalog carries shapes on purpose — but only if
// its page SAYS so, because silence there reads as a template.
func TestProductCatalogMatchesCorpus(t *testing.T) {
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
	pages, err := os.ReadDir(productsWebDir)
	if err != nil {
		t.Fatalf("read %s: %v", productsWebDir, err)
	}
	for _, e := range pages {
		if e.IsDir() {
			documented[e.Name()] = true
		}
	}
	if len(shipped) == 0 || len(documented) == 0 {
		t.Fatal("one of the two sets is empty — a path is wrong and this would pass vacuously")
	}

	for name := range shipped {
		for _, page := range []string{"index.html", "setup.html"} {
			if _, err := os.Stat(filepath.Join(productsWebDir, name, page)); err != nil {
				t.Errorf("product %q ships but has no catalog %s — `products list` names it and the site does not "+
					"(template/web/CLAUDE.md §\"Adding a new product\")", name, page)
			}
		}
	}
	for name := range documented {
		if shipped[name] {
			continue
		}
		b, rerr := os.ReadFile(filepath.Join(productsWebDir, name, "index.html"))
		if rerr != nil {
			t.Errorf("catalog entry %q has no index.html: %v", name, rerr)
			continue
		}
		if !strings.Contains(string(b), shapeMarker) {
			t.Errorf("%q is documented but ships no tree, and its page does not say so — "+
				"add the %q note or drop the page", name, shapeMarker)
		}
	}
}

// TestEveryProductShipsEvalCases locks the other half of spec 5/9-agent-capability-eval, "a product carries
// its own cases": the harness takes `--cases <dir>`, and for a while exactly one of eleven
// used it. A product with no cases regresses invisibly — every daemon healthy, all 34
// platform cases green, and the persona quietly answering the way it must not.
//
// Only `[[case]]` count is asserted, not what a case says: what to assert is a judgement
// per product and a test cannot make it. That a suite EXISTS is mechanical.
func TestEveryProductShipsEvalCases(t *testing.T) {
	entries, err := os.ReadDir(examplesDir)
	if err != nil {
		t.Fatalf("read %s: %v", examplesDir, err)
	}
	seen := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		seen++
		files, err := filepath.Glob(filepath.Join(examplesDir, e.Name(), "eval", "*.toml"))
		if err != nil {
			t.Fatalf("glob %s: %v", e.Name(), err)
		}
		if len(files) == 0 {
			t.Errorf("product %q ships no eval/*.toml — a product proves its own behaviour "+
				"or it regresses silently (spec 5/9)", e.Name())
			continue
		}
		cases := 0
		for _, f := range files {
			b, rerr := os.ReadFile(f)
			if rerr != nil {
				t.Fatalf("read %s: %v", f, rerr)
			}
			cases += strings.Count(string(b), "[[case]]")
		}
		if cases == 0 {
			t.Errorf("product %q has eval/*.toml but no [[case]] in it", e.Name())
		}
	}
	if seen == 0 {
		t.Fatal("no products found — examplesDir is wrong and this would pass vacuously")
	}
}

// readJSON decodes a JSON file into v, failing the test on any error — used by
// the settings-union assertion, where comparing hashes would be wrong.
func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
}

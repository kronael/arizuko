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

func TestProductManifestOwnership(t *testing.T) {
	for _, mode := range []string{"seed", "blend"} {
		for _, existing := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/existing=%v", mode, existing), func(t *testing.T) {
				root := t.TempDir()
				product := mkProduct(t, root, "source", map[string]string{
					"arizuko.yml": "name: source\npackages: []\n",
					"PERSONA.md":  "source persona", "assets/arizuko.yml": "ordinary asset",
				})
				group := filepath.Join(root, "group")
				if err := os.MkdirAll(group, 0o755); err != nil {
					t.Fatal(err)
				}
				const caller = "# caller-owned\nname: my-group\n"
				path := filepath.Join(group, "arizuko.yml")
				if existing {
					if err := os.WriteFile(path, []byte(caller), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "seed" {
					if err := SeedProduct(group, product.Dir); err != nil {
						t.Fatal(err)
					}
				} else {
					out := blendInto(t, group, []Product{product}, nil)
					for _, rel := range out[0].Manifest["group_seed"] {
						if rel == "arizuko.yml" {
							t.Error("source owns the caller manifest")
						}
					}
					if _, ok := out[0].AssetHashes["file:arizuko.yml"]; ok {
						t.Error("source hashes the caller manifest")
					}
				}
				got, err := os.ReadFile(path)
				if existing && (err != nil || string(got) != caller) {
					t.Errorf("caller manifest = %q, err=%v", got, err)
				} else if !existing && !os.IsNotExist(err) {
					t.Errorf("source manifest copied: %q, err=%v", got, err)
				}
				if read(t, group, "PERSONA.md") != "source persona" ||
					read(t, group, "assets/arizuko.yml") != "ordinary asset" {
					t.Error("ordinary payload lost")
				}
			})
		}
	}
}

func TestBlendProtectsGroupManifestFromPriorOwnership(t *testing.T) {
	root := t.TempDir()
	product := mkProduct(t, root, "source", map[string]string{"PERSONA.md": "persona"})
	group := filepath.Join(root, "group")
	if err := os.MkdirAll(group, 0o755); err != nil {
		t.Fatal(err)
	}
	const caller = "name: caller\n"
	if err := os.WriteFile(filepath.Join(group, "arizuko.yml"), []byte(caller), 0o644); err != nil {
		t.Fatal(err)
	}
	prior := map[string]map[string]string{"source": {"file:arizuko.yml": hashBytes([]byte(caller))}}
	out := blendInto(t, group, []Product{product}, prior)
	if got := read(t, group, "arizuko.yml"); got != caller {
		t.Errorf("caller manifest changed: %q", got)
	}
	if _, ok := out[0].AssetHashes["file:arizuko.yml"]; ok {
		t.Error("obsolete ownership retained")
	}
}

func TestProductManifestSourceRefusal(t *testing.T) {
	for _, body := range []string{"name = [\n", "name: source\npackages:\n    - source: nested\n"} {
		for _, mode := range []string{"check", "seed", "blend"} {
			t.Run(mode+"/"+body, func(t *testing.T) {
				root := t.TempDir()
				product := mkProduct(t, root, "source", map[string]string{
					"arizuko.yml": body, "PERSONA.md": "must not land",
				})
				group := filepath.Join(root, "group")
				var err error
				switch mode {
				case "check":
					err = CheckProductAssets(product.Name, product.Dir)
				case "seed":
					err = SeedProduct(group, product.Dir)
				case "blend":
					_, err = BlendProducts(group, []Product{product}, nil)
				}
				if err == nil || !strings.Contains(err.Error(), "arizuko.yml") {
					t.Errorf("invalid source accepted: %v", err)
				}
				if _, err := os.Stat(filepath.Join(group, "PERSONA.md")); !os.IsNotExist(err) {
					t.Errorf("refusal wrote payload: %v", err)
				}
			})
		}
	}
}

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

// TestProductSeedIsVerbatim checks every payload file after excluding source metadata.
//
// This is the regression bar for spec 5/28-packages's composition blend. The blend table names
// eight payloads, but the shipped corpus also ships SOUL.md (6/11)
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
				if rel == "arizuko.yml" || rel == "CLAUDE.md" {
					if got[rel] != "" {
						t.Error("source manifest reached the group")
					}
					continue
				}
				if strings.HasPrefix(rel, "skills/") {
					if got[rel] != "" {
						t.Error("managed skill was copied")
					}
					continue
				}
				dst := filepath.FromSlash(rel)
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
// every corpus payload must survive a mix of ONE byte-identical. CLAUDE.md is the single
// exception the blend table mandates — it concatenates, so it lands inside its product's
// marked region. Source metadata is excluded from payloads.
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
				if rel == "arizuko.yml" || rel == "CLAUDE.md" {
					if got[rel] != "" {
						t.Error("source manifest reached the group")
					}
					continue
				}
				// Two payload kinds transform BY DESIGN, so "same path, same bytes" is the wrong
				// assertion for them: a skill is relocated under .claude/, and settings.json is a
				// key union that is re-serialised. Assert the transformed result instead of
				// exempting them, or the reference product would silently lose either payload.
				if strings.HasPrefix(rel, "skills/") {
					if got[rel] != "" {
						t.Errorf("managed skill %s was copied", rel)
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
		"arizuko.yml":            "name: pp\n",
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
		if set[skill] != "" {
			t.Errorf("%s: managed skill was copied to %s", path, skill)
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

func TestProductCorpusPayloadKinds(t *testing.T) {
	products, err := os.ReadDir(examplesDir)
	if err != nil {
		t.Fatalf("read %s: %v", examplesDir, err)
	}
	total := 0
	for _, p := range products {
		if !p.IsDir() {
			continue
		}
		total++
		if _, err := os.Stat(filepath.Join(examplesDir, p.Name(), "arizuko.yml")); err != nil {
			t.Errorf("%s: missing arizuko.yml: %v", p.Name(), err)
		}
	}
	if total == 0 {
		t.Fatal("no products found")
	}
	for _, rel := range []string{".claude/settings.json", "skills/triage/SKILL.md", "facts/sla.md", "eval/helpdesk.toml"} {
		if _, err := os.Stat(filepath.Join(examplesDir, "helpdesk", rel)); err != nil {
			t.Errorf("helpdesk is the reference product and no longer ships %s: %v", rel, err)
		}
	}
	for _, absent := range []string{"tasks.toml", "migrations"} {
		for _, p := range products {
			if !p.IsDir() {
				continue
			}
			if _, err := os.Stat(filepath.Join(examplesDir, p.Name(), absent)); err == nil {
				t.Errorf("%s ships unsupported payload %s", p.Name(), absent)
			}
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

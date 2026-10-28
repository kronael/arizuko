package container

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// examplesDir is the shipped product corpus.
const examplesDir = "../ant/examples"

func TestCoreHasNoGroupPayload(t *testing.T) {
	group := t.TempDir()
	out := blendInto(t, group, []Product{{Name: "core", Dir: "../ant"}}, nil)
	if len(out[0].Manifest[ManifestGroupFiles]) != 0 {
		t.Fatalf("core group files: %v", out[0].Manifest)
	}
	entries, err := os.ReadDir(group)
	if err != nil || len(entries) != 0 {
		t.Fatalf("core copied source files: %v, %v", entries, err)
	}
}

func TestGroupPayloadExcludesSourceReadme(t *testing.T) {
	root := t.TempDir()
	product := mkProduct(t, root, "source", map[string]string{"PERSONA.md": "persona"})
	writeFile(t, filepath.Join(product.Dir, "README.md"), "source documentation")
	group := t.TempDir()
	blendInto(t, group, []Product{product}, nil)
	if got := read(t, group, "PERSONA.md"); got != "persona" {
		t.Fatalf("persona: %q", got)
	}
	if _, err := os.Stat(filepath.Join(group, "README.md")); !os.IsNotExist(err) {
		t.Fatalf("source README copied: %v", err)
	}
}

// A group's own arizuko.yml is not a package file: the one at the package root
// is the manifest, and group/arizuko.yml is never written or removed.
func TestProductManifestOwnership(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing=%v", existing), func(t *testing.T) {
			root := t.TempDir()
			product := mkProduct(t, root, "source", map[string]string{
				"arizuko.yml": "name: source\n",
				"PERSONA.md":  "source persona", "assets/arizuko.yml": "ordinary asset",
			})
			group := filepath.Join(root, "group")
			const caller = "# caller-owned\nname: my-group\n"
			path := filepath.Join(group, "arizuko.yml")
			if existing {
				writeFile(t, path, caller)
			}
			out := blendInto(t, group, []Product{product}, nil)
			if slices.Contains(out[0].Manifest[ManifestGroupFiles], "arizuko.yml") {
				t.Error("source owns the caller manifest")
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

func TestProductManifestSourceRefusal(t *testing.T) {
	for _, body := range []string{"name = [\n", "name: source\npackages:\n    - source: nested\n"} {
		t.Run(body, func(t *testing.T) {
			root := t.TempDir()
			product := mkProduct(t, root, "source", map[string]string{
				"arizuko.yml": body, "PERSONA.md": "must not land",
			})
			group := filepath.Join(root, "group")
			_, _, err := BlendProducts(group, []Product{product}, nil)
			if err == nil || !strings.Contains(err.Error(), "arizuko.yml") {
				t.Errorf("invalid source accepted: %v", err)
			}
			if _, err := os.Stat(filepath.Join(group, "PERSONA.md")); !os.IsNotExist(err) {
				t.Errorf("refusal wrote payload: %v", err)
			}
		})
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
			if path == root && os.IsNotExist(err) {
				return nil
			}
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

// TestProductMixWritesCorpusVerbatim: every corpus payload survives a mix of
// one byte-identical. settings.json is the one transformed payload, a key union
// that is re-serialised, so its keys are compared instead of its bytes.
func TestProductMixWritesCorpusVerbatim(t *testing.T) {
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
			want := walkFiles(t, filepath.Join(seedDir, "group"))
			if (len(want) == 0) != (p.Name() == "cuzk") {
				t.Fatalf("unexpected group payload size: %d", len(want))
			}
			group := filepath.Join(t.TempDir(), "group")
			out := blendInto(t, group, []Product{{Name: p.Name(), Dir: seedDir}}, nil)
			got := walkFiles(t, group)
			for rel, wantHash := range want {
				if rel == "arizuko.yml" {
					if got[rel] != "" {
						t.Error("source manifest reached the group")
					}
					continue
				}
				if rel == settingsRel {
					var seedKeys, blendedKeys map[string]any
					readJSON(t, filepath.Join(seedDir, "group", rel), &seedKeys)
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
				if !slices.Contains(out[0].Manifest[ManifestGroupFiles], rel) {
					t.Errorf("receipt does not list %s", rel)
				}
			}
		})
	}
	if seen == 0 {
		t.Fatal("no products found — corpus path wrong, test would pass vacuously")
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
	for _, rel := range []string{"group/.claude/settings.json", "skills/triage/SKILL.md", "facts/sla.md", "eval/helpdesk.toml"} {
		if _, err := os.Stat(filepath.Join(examplesDir, "helpdesk", rel)); err != nil {
			t.Errorf("helpdesk is the reference product and must ship %s: %v", rel, err)
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

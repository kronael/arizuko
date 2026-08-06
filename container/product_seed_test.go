package container

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
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

// TestProductSeedIsVerbatim locks today's seed: SetupGroup copies a product
// tree into the group folder byte-for-byte, every file, no filter.
//
// This is the regression bar for spec 5/28's composition blend. The blend table
// names `skills/`, PERSONA.md, CLAUDE.md, facts/, tasks.toml, settings.json,
// Dockerfile.ant and migrations/ — but the shipped corpus also ships
// PRODUCT.md (10/10 products), SOUL.md (6/10) and BRANDING.md (2/10). A
// table-strict blend that treats the table as a whitelist would silently DROP
// those, which is a regression and not a simplification. If a future blend
// engine replaces CopyDirNoSymlinks, this test must still pass.
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
				gotHash, ok := got[rel]
				if !ok {
					t.Errorf("seed DROPPED %s", rel)
					continue
				}
				if gotHash != wantHash {
					t.Errorf("seed altered %s: %s != %s", rel, gotHash, wantHash)
				}
			}
		})
	}
	if seen == 0 {
		t.Fatal("no products found — corpus path wrong, test would pass vacuously")
	}
}

// TestProductCorpusPayloadKinds pins the payload kinds the shipped corpus
// actually uses, so spec 5/28's blend table can be checked against reality
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
		"PRODUCT.md":  10, // every product
		"SOUL.md":     6,
		"PERSONA.md":  3,
		"BRANDING.md": 2,
		"CLAUDE.md":   3,
	}
	if total != 10 {
		t.Fatalf("corpus size moved: %d products, spec 5/28 says 10", total)
	}
	for name, wantN := range want {
		if counts[name] != wantN {
			t.Errorf("%s: corpus has %d, spec 5/28 says %d", name, counts[name], wantN)
		}
	}
	// Kinds the blend table names that the corpus does not ship — the spec's
	// "payload kinds do not match the corpus" gap. If one appears, the blend
	// table stops being hypothetical for it and the spec must say so.
	for _, absent := range []string{"tasks.toml", "settings.json", "Dockerfile.ant"} {
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

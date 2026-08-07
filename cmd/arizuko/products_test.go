package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kronael/arizuko/routd"
)

// mixInstance seeds a data dir whose group `main` declares an ordered mix of the
// named products, each a local source tree under <base>/src/<name>.
func mixInstance(t *testing.T, files map[string]map[string]string, order []string) (dataDir, base string) {
	t.Helper()
	base = t.TempDir()
	t.Setenv("ARIZUKO_DATA_DIR", base)
	dataDir = filepath.Join(base, "arizuko_t")
	t.Setenv("DATA_DIR", dataDir)
	groupDir := filepath.Join(dataDir, "groups", "main")
	for _, d := range []string{filepath.Join(dataDir, "store"), groupDir, filepath.Join(dataDir, "ipc")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mix := ""
	for _, name := range order {
		for rel, body := range files[name] {
			p := filepath.Join(base, "src", name, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		mix += "[[product]]\nsource = \"" + filepath.Join(base, "src", name) + "\"\n\n"
	}
	if err := os.WriteFile(filepath.Join(groupDir, "products.toml"), []byte(mix), 0o644); err != nil {
		t.Fatal(err)
	}
	return dataDir, base
}

// TestProductsApplyWritesGroupScopedRecord is spec 5/28's one unbuilt item: a
// reader for products.toml AND the only writer of a non-empty
// installed_packages.folder. A record under InstanceWide would mean composition
// still shares the instance's key and `packages remove` could reach a group's
// products.
func TestProductsApplyWritesGroupScopedRecord(t *testing.T) {
	dataDir, _ := mixInstance(t, map[string]map[string]string{
		"one": {"PRODUCT.md": "name = \"one\"\n", "PERSONA.md": "p1"},
		"two": {"PRODUCT.md": "name = \"two\"\n", "facts/t.md": "f2"},
	}, []string{"one", "two"})

	cmdProducts([]string{"t", "apply", "main"})

	groupDir := filepath.Join(dataDir, "groups", "main")
	if b, err := os.ReadFile(filepath.Join(groupDir, "PERSONA.md")); err != nil || string(b) != "p1" {
		t.Fatalf("PERSONA.md = %q err=%v", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(groupDir, "facts", "t.md")); err != nil || string(b) != "f2" {
		t.Fatalf("facts/t.md = %q err=%v", b, err)
	}

	rdb, err := routd.Open(filepath.Join(dataDir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	defer rdb.Close()
	for _, name := range []string{"one", "two"} {
		rec, ok, err := rdb.InstalledPackage("main", name)
		if err != nil || !ok {
			t.Fatalf("record (main,%s) missing: ok=%v err=%v", name, ok, err)
		}
		if rec.Folder != "main" {
			t.Errorf("%s recorded folder %q, want main", name, rec.Folder)
		}
		if len(rec.Manifest["group_seed"]) == 0 {
			t.Errorf("%s manifest names no identities: %+v", name, rec.Manifest)
		}
		if len(rec.AssetHashes) == 0 {
			t.Errorf("%s recorded no asset hashes", name)
		}
		if _, instanceWide, _ := rdb.InstalledPackage(routd.InstanceWide, name); instanceWide {
			t.Errorf("%s also landed under InstanceWide — composition must not share the instance key", name)
		}
	}
}

// TestProductsApplyIsIdempotent proves a second apply writes NOTHING. It
// compares modification times, not just bytes: re-writing a file with identical
// content leaves every hash equal, so a content-only check passes on a blend
// that rewrites the whole group tree every run. Never the second-granular
// installed_at, which matches even when the record is rewritten.
func TestProductsApplyIsIdempotent(t *testing.T) {
	dataDir, _ := mixInstance(t, map[string]map[string]string{
		"one": {"PRODUCT.md": "name = \"one\"\n", "CLAUDE.md": "runbook", "skills/s/SKILL.md": "v1"},
	}, []string{"one"})

	cmdProducts([]string{"t", "apply", "main"})
	groupDir := filepath.Join(dataDir, "groups", "main")
	before := treeHashes(t, groupDir)
	beforeMtime := treeMtimes(t, groupDir)

	rdb, err := routd.Open(filepath.Join(dataDir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	first, _, _ := rdb.InstalledPackage("main", "one")
	rdb.Close()

	cmdProducts([]string{"t", "apply", "main"})

	after := treeHashes(t, groupDir)
	if len(before) != len(after) {
		t.Fatalf("file set moved on re-apply: %d -> %d", len(before), len(after))
	}
	for rel, h := range before {
		if after[rel] != h {
			t.Errorf("%s content moved on a no-op re-apply", rel)
		}
	}
	for rel, mt := range treeMtimes(t, groupDir) {
		if was, ok := beforeMtime[rel]; ok && !mt.Equal(was) {
			t.Errorf("%s was re-written on a no-op re-apply (mtime %v -> %v)", rel, was, mt)
		}
	}
	rdb2, err := routd.Open(filepath.Join(dataDir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	defer rdb2.Close()
	second, ok, _ := rdb2.InstalledPackage("main", "one")
	if !ok {
		t.Fatal("record vanished on re-apply")
	}
	for k, v := range first.AssetHashes {
		if second.AssetHashes[k] != v {
			t.Errorf("recorded hash for %s moved: %s -> %s", k, v, second.AssetHashes[k])
		}
	}
	if len(first.AssetHashes) != len(second.AssetHashes) {
		t.Errorf("recorded asset set moved: %d -> %d", len(first.AssetHashes), len(second.AssetHashes))
	}
}

// TestPackagesSyncCoversGroupMix locks the seam between the two halves of the
// lock: `sync` walks every record, and a group-scoped one is a product, not a
// compose fragment. Routing it through reapplyPackage dies on "no compose
// fragment"; routing it through the mix re-applies it.
func TestPackagesSyncCoversGroupMix(t *testing.T) {
	dataDir, base := mixInstance(t, map[string]map[string]string{
		"one": {"PRODUCT.md": "name = \"one\"\n", "skills/s/SKILL.md": "v1"},
	}, []string{"one"})

	cmdProducts([]string{"t", "apply", "main"})

	skill := filepath.Join(dataDir, "groups", "main", ".claude", "skills", "s", "SKILL.md")
	if b, _ := os.ReadFile(skill); string(b) != "v1" {
		t.Fatalf("skill not seeded: %q", b)
	}
	if err := os.WriteFile(filepath.Join(base, "src", "one", "skills", "s", "SKILL.md"), []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmdPackages([]string{"t", "sync"})

	if b, _ := os.ReadFile(skill); string(b) != "v2" {
		t.Errorf("skill = %q — sync must re-apply the group mix's managed payload", b)
	}
}

// TestReadProductMixKeepsOrder locks the reader's only contract beyond parsing:
// the mix is ORDERED, and every blend rule (first-wins, last-wins, mix order)
// reads that order.
func TestReadProductMixKeepsOrder(t *testing.T) {
	dir := t.TempDir()
	body := "[[product]]\nsource = \"z\"\n\n[[product]]\nsource = \"a\"\n\n[[product]]\nsource = \"m\"\n"
	if err := os.WriteFile(filepath.Join(dir, "products.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	mix, ok := readProductMix(dir)
	if !ok {
		t.Fatal("declared mix not found")
	}
	want := []string{"z", "a", "m"}
	if len(mix.Product) != len(want) {
		t.Fatalf("got %d entries, want %d", len(mix.Product), len(want))
	}
	for i, w := range want {
		if mix.Product[i].Source != w {
			t.Errorf("entry %d = %q, want %q — declaration order is the mix order", i, mix.Product[i].Source, w)
		}
	}
	if _, ok := readProductMix(t.TempDir()); ok {
		t.Error("a group with no products.toml reported a mix")
	}
}

// treeMtimes maps every regular file under root to its modification time — the
// only evidence that distinguishes "not written" from "written identically".
func treeMtimes(t *testing.T, root string) map[string]time.Time {
	t.Helper()
	out := map[string]time.Time{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Type()&os.ModeSymlink != 0 {
			return err
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		fi, rerr := d.Info()
		if rerr != nil {
			return rerr
		}
		out[rel] = fi.ModTime()
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}

// treeHashes maps every regular file under root to its content hash.
func treeHashes(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		out[rel] = sha256hex(b)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}

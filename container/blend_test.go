package container

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// mkProduct writes a product tree under root/name and returns its mix entry.
// Keys are slash-separated product-relative paths.
func mkProduct(t *testing.T, root, name string, files map[string]string) Product {
	t.Helper()
	dir := filepath.Join(root, name)
	if _, found := files[ManifestFile]; !found {
		files[ManifestFile] = "name: " + name + "\n"
	}
	for rel, body := range files {
		base := dir
		if rel != ManifestFile && rel != "CLAUDE.md" && !strings.HasPrefix(rel, "skills/") && rel != ".git" && !strings.HasPrefix(rel, ".git/") {
			base = filepath.Join(dir, "group")
		}
		p := filepath.Join(base, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return Product{Name: name, Dir: dir}
}

func read(t *testing.T, parts ...string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(parts...))
	if err != nil {
		t.Fatalf("read %v: %v", parts, err)
	}
	return string(b)
}

// blendInto runs a mix into groupDir with the files the previous blend wrote
// and fails on refusal.
func blendInto(t *testing.T, groupDir string, mix []Product, managed []string) []Blended {
	t.Helper()
	out, _, err := BlendProducts(groupDir, mix, managed)
	if err != nil {
		t.Fatalf("BlendProducts: %v", err)
	}
	return out
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestProductPayloadSkipsRootGit(t *testing.T) {
	for _, gitPath := range []string{".git/config", ".git"} {
		t.Run(gitPath, func(t *testing.T) {
			root := t.TempDir()
			product := mkProduct(t, root, "product", map[string]string{
				gitPath:              "gitdir: /example/repository\n",
				"PRODUCT.md":         "name = \"product\"\n",
				"assets/.git/config": "product asset",
			})
			group := filepath.Join(root, "group")
			blendInto(t, group, []Product{product}, nil)
			if _, err := os.Lstat(filepath.Join(group, ".git")); !os.IsNotExist(err) {
				t.Errorf("group .git must be absent, got %v", err)
			}
			if got := read(t, group, "PRODUCT.md"); got != "name = \"product\"\n" {
				t.Errorf("PRODUCT.md = %q", got)
			}
			if got := read(t, group, "assets/.git/config"); got != "product asset" {
				t.Errorf("nested .git asset = %q", got)
			}
		})
	}
}

// The last package in the mix wins a path, whatever the file is called, and the
// override line names both packages. A path only one package ships still lands.
func TestBlendLastProviderWins(t *testing.T) {
	root := t.TempDir()
	a := mkProduct(t, root, "a", map[string]string{
		"PERSONA.md": "persona A", "facts/x.md": "fact A", "BRANDING.md": "brand A",
	})
	b := mkProduct(t, root, "b", map[string]string{
		"PERSONA.md": "persona B", "facts/x.md": "fact B", "README.md": "b readme",
	})
	group := filepath.Join(root, "group")
	out, overrides, err := BlendProducts(group, []Product{a, b}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]string{
		"PERSONA.md": "persona B", "facts/x.md": "fact B", "BRANDING.md": "brand A", "README.md": "b readme",
	} {
		if got := read(t, group, rel); got != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}
	if want := []string{"b overrides a on group/PERSONA.md", "b overrides a on group/facts/x.md"}; !slices.Equal(overrides, want) {
		t.Errorf("overrides = %q, want %q", overrides, want)
	}
	if got := out[0].Manifest[ManifestGroupFiles]; !slices.Equal(got, []string{"BRANDING.md"}) {
		t.Errorf("a wrote %v, want only BRANDING.md", got)
	}
	if got := out[1].Manifest[ManifestGroupFiles]; !slices.Equal(got, []string{"PERSONA.md", "README.md", "facts/x.md"}) {
		t.Errorf("b wrote %v", got)
	}
}

// Every blend rewrites the managed files, so an agent edit does not survive.
func TestBlendReplacesAgentEdits(t *testing.T) {
	root := t.TempDir()
	a := mkProduct(t, root, "a", map[string]string{"PERSONA.md": "v1", "facts/f.md": "f1"})
	group := filepath.Join(root, "group")
	first := blendInto(t, group, []Product{a}, nil)

	writeFile(t, filepath.Join(group, "PERSONA.md"), "mine")
	mkProduct(t, root, "a", map[string]string{"PERSONA.md": "v2", "facts/f.md": "f2"})
	blendInto(t, group, []Product{a}, first[0].Manifest[ManifestGroupFiles])
	if got := read(t, group, "PERSONA.md"); got != "v2" {
		t.Errorf("PERSONA.md = %q, want the package's bytes", got)
	}
	if got := read(t, group, "facts/f.md"); got != "f2" {
		t.Errorf("facts/f.md = %q, want the package's bytes", got)
	}

	writeFile(t, filepath.Join(group, "facts/f.md"), "edited")
	blendInto(t, group, []Product{a}, first[0].Manifest[ManifestGroupFiles])
	if got := read(t, group, "facts/f.md"); got != "f2" {
		t.Errorf("facts/f.md = %q, want the package's bytes after a second edit", got)
	}
}

// A managed file no package ships is removed; a file no package ever shipped is
// the group's and stays.
func TestBlendRemovesUnshippedManagedFiles(t *testing.T) {
	root := t.TempDir()
	a := mkProduct(t, root, "a", map[string]string{"keep.md": "k", "gone.md": "g", "dir/gone.md": "d"})
	group := filepath.Join(root, "group")
	first := blendInto(t, group, []Product{a}, nil)
	writeFile(t, filepath.Join(group, "mine.md"), "the group's own")

	for _, rel := range []string{"gone.md", "dir/gone.md"} {
		if err := os.Remove(filepath.Join(a.Dir, "group", filepath.FromSlash(rel))); err != nil {
			t.Fatal(err)
		}
	}
	second := blendInto(t, group, []Product{a}, first[0].Manifest[ManifestGroupFiles])
	for _, rel := range []string{"gone.md", "dir/gone.md"} {
		if _, err := os.Stat(filepath.Join(group, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Errorf("%s no longer shipped but still in the group: %v", rel, err)
		}
	}
	if read(t, group, "keep.md") != "k" || read(t, group, "mine.md") != "the group's own" {
		t.Error("a shipped file or the group's own file was lost")
	}
	if got := second[0].Manifest[ManifestGroupFiles]; !slices.Equal(got, []string{"keep.md"}) {
		t.Errorf("receipt lists %v, want only keep.md", got)
	}

	blendInto(t, group, nil, second[0].Manifest[ManifestGroupFiles])
	if _, err := os.Stat(filepath.Join(group, "keep.md")); !os.IsNotExist(err) {
		t.Errorf("withdrawing the last package left keep.md: %v", err)
	}
	if read(t, group, "mine.md") != "the group's own" {
		t.Error("withdrawal removed the group's own file")
	}
}

// A file that exists and that no earlier blend wrote is unmanaged, whatever its
// bytes; a file the previous blend wrote is managed.
func TestUnmanagedPaths(t *testing.T) {
	root := t.TempDir()
	a := mkProduct(t, root, "a", map[string]string{"PERSONA.md": "pkg", "other.md": "o", "new.md": "n"})
	group := filepath.Join(root, "group")
	writeFile(t, filepath.Join(group, "PERSONA.md"), "pkg")
	writeFile(t, filepath.Join(group, "other.md"), "mine")

	got, err := UnmanagedPaths(group, []Product{a}, []string{"other.md"})
	if err != nil || !slices.Equal(got, []string{"PERSONA.md"}) {
		t.Fatalf("UnmanagedPaths = %v, %v; want [PERSONA.md]", got, err)
	}
	if _, err := os.Stat(filepath.Join(group, "new.md")); !os.IsNotExist(err) {
		t.Error("UnmanagedPaths wrote a file")
	}
}

// TestBlendRefusesProductMigrations locks the refusal of a product's
// migrations/": no runner exists, so writing the payload would report success
// for an asset that never executes.
func TestBlendRefusesProductMigrations(t *testing.T) {
	root := t.TempDir()
	a := mkProduct(t, root, "a", map[string]string{
		"migrations/001-x.md": "do the thing", "PRODUCT.md": "a",
	})
	group := filepath.Join(root, "group")

	_, _, err := BlendProducts(group, []Product{a}, nil)
	if err == nil {
		t.Fatal("a product shipping migrations/ applied cleanly and ran nothing")
	}
	for _, want := range []string{"migrations/001-x.md", "CLAUDE.md"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must name %q so the author knows the working path; got %v", want, err)
		}
	}
	if _, sErr := os.Stat(filepath.Join(group, "PRODUCT.md")); !os.IsNotExist(sErr) {
		t.Error("a refused mix still wrote files")
	}
}

// TestBlendRefusesProductTasks locks BUGS L35's exit: `tasks.toml` is refused,
// not written. Nothing reads it, so the file would land in the group and no
// task would exist.
func TestBlendRefusesProductTasks(t *testing.T) {
	root := t.TempDir()
	a := mkProduct(t, root, "a", map[string]string{
		"tasks.toml": "[[task]]\ncron = \"0 9 * * *\"\n", "PRODUCT.md": "a",
	})
	group := filepath.Join(root, "group")

	_, _, err := BlendProducts(group, []Product{a}, nil)
	if err == nil {
		t.Fatal("a product shipping tasks.toml applied cleanly and scheduled nothing")
	}
	for _, want := range []string{"tasks.toml", "schedule_task"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must name %q so the author knows the working path; got %v", want, err)
		}
	}
	if _, sErr := os.Stat(filepath.Join(group, "PRODUCT.md")); !os.IsNotExist(sErr) {
		t.Error("a refused mix still wrote files")
	}
}

// TestRefusedPayloadsNameARoute: every refusal must tell the author where the
// intent DOES work.
func TestRefusedPayloadsNameARoute(t *testing.T) {
	for kind, reason := range refusedPayloads {
		if !strings.Contains(reason, "—") || !strings.Contains(reason, "container/README.md") {
			t.Errorf("refusal for payload %v must name the working route and its documentation: %q", kind, reason)
		}
	}
}

// TestBlendSettingsRefusesMcpServers — the settings.json `mcpServers` key
// reaches no consumer. arizuko is the only server on the agent's map after
// BUGS J1/J14, and Claude Code has no top-level `mcpServers` setting either, so
// a product shipping one applied cleanly and did NOTHING: a silent no-op on an
// operator-facing path, which the fail-loud rule forbids (BUGS J15). The apply
// now stops and names the path that works.
func TestBlendSettingsRefusesMcpServers(t *testing.T) {
	root := t.TempDir()
	a := mkProduct(t, root, "a", map[string]string{
		".claude/settings.json": `{"mcpServers":{"alpha":{"command":"a"}},"outputStyle":"telegram"}`,
	})

	_, _, err := BlendProducts(filepath.Join(root, "group"), []Product{a}, nil)
	if err == nil {
		t.Fatal("a product declaring mcpServers applied cleanly and did nothing")
	}
	for _, want := range []string{"mcpServers", "connectors.toml"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must name %q so the operator knows the working path; got %v", want, err)
		}
	}
}

// A settings.json WITHOUT mcpServers still blends: the refusal is one key, not
// the whole asset kind.
func TestBlendSettingsUnionsOtherKeys(t *testing.T) {
	root := t.TempDir()
	a := mkProduct(t, root, "a", map[string]string{
		".claude/settings.json": `{"outputStyle":"telegram"}`,
	})
	b := mkProduct(t, root, "b", map[string]string{
		".claude/settings.json": `{"cleanupPeriodDays":1}`,
	})
	group := filepath.Join(root, "group")
	blendInto(t, group, []Product{a, b}, nil)

	var s map[string]any
	if err := json.Unmarshal([]byte(read(t, group, ".claude/settings.json")), &s); err != nil {
		t.Fatal(err)
	}
	if s["outputStyle"] != "telegram" {
		t.Errorf("outputStyle = %v, want telegram", s["outputStyle"])
	}
	if s["cleanupPeriodDays"] == nil {
		t.Errorf("the second provider's key was dropped: %v", s)
	}

	dup := mkProduct(t, root, "dup", map[string]string{
		".claude/settings.json": `{"outputStyle":"slack"}`,
	})
	if _, _, err := BlendProducts(filepath.Join(root, "g2"), []Product{a, dup}, nil); err == nil {
		t.Fatal("a settings.json key collision was accepted")
	}
}

// TestBlendIsIdempotent proves re-applying an unchanged mix moves no byte.
func TestBlendIsIdempotent(t *testing.T) {
	root := t.TempDir()
	a := mkProduct(t, root, "a", map[string]string{
		"PRODUCT.md": "name = \"a\"\n", "PERSONA.md": "p", "CLAUDE.md": "c", "facts/f.md": "f",
	})
	group := filepath.Join(root, "group")
	first := blendInto(t, group, []Product{a}, nil)
	before := walkFiles(t, group)

	blendInto(t, group, []Product{a}, first[0].Manifest[ManifestGroupFiles])
	after := walkFiles(t, group)
	if len(before) != len(after) {
		t.Fatalf("file set moved: %d -> %d", len(before), len(after))
	}
	for rel, h := range before {
		if after[rel] != h {
			t.Errorf("%s content moved on a no-op re-apply", rel)
		}
	}
}

// TestClassifyCoversTheBlendTable pins each path to the rule it selects, so a
// future edit that reroutes a payload fails here rather than in a live group.
func TestClassifyCoversTheBlendTable(t *testing.T) {
	want := map[string]payload{
		"tasks.toml":            payTasks,
		"migrations/001-x.md":   payMigrations,
		".claude/settings.json": paySettings,
		"PERSONA.md":            payVerbatim,
		"SOUL.md":               payVerbatim,
		"CLAUDE.md":             payVerbatim,
		"facts/x.md":            payVerbatim,
		"skills/foo/SKILL.md":   payVerbatim,
		"Dockerfile.ant":        payVerbatim,
		"BRANDING.md":           payVerbatim,
		"settings.json":         payVerbatim,
		"migrations":            payVerbatim,
	}
	for rel, k := range want {
		if got := classify(rel); got != k {
			t.Errorf("classify(%q) = %v, want %v", rel, got, k)
		}
	}
}

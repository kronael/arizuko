package container

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mkProduct writes a product tree under root/name and returns its mix entry.
// Keys are slash-separated product-relative paths.
func mkProduct(t *testing.T, root, name string, files map[string]string) Product {
	t.Helper()
	dir := filepath.Join(root, name)
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
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

// blendInto runs a mix into a fresh group dir and fails on refusal.
func blendInto(t *testing.T, groupDir string, mix []Product, prior map[string]map[string]string) []Blended {
	t.Helper()
	out, err := BlendProducts(groupDir, mix, prior)
	if err != nil {
		t.Fatalf("BlendProducts: %v", err)
	}
	return out
}

// TestBlendCatchAllCopiesUnnamedPayload locks spec 5/28's last table row: a
// payload no rule names is copied whole, FIRST provider wins. Without it a
// table-strict blend drops PRODUCT.md (10/10 of the corpus) and BRANDING.md.
func TestBlendCatchAllCopiesUnnamedPayload(t *testing.T) {
	root := t.TempDir()
	a := mkProduct(t, root, "a", map[string]string{
		"PRODUCT.md": "name = \"a\"\n", "BRANDING.md": "brand A",
	})
	b := mkProduct(t, root, "b", map[string]string{
		"PRODUCT.md": "name = \"b\"\n", "README.md": "b readme",
	})
	group := filepath.Join(root, "group")
	out := blendInto(t, group, []Product{a, b}, nil)

	if got := read(t, group, "PRODUCT.md"); got != "name = \"a\"\n" {
		t.Errorf("PRODUCT.md = %q, want the FIRST provider's bytes", got)
	}
	if got := read(t, group, "BRANDING.md"); got != "brand A" {
		t.Errorf("BRANDING.md = %q", got)
	}
	if got := read(t, group, "README.md"); got != "b readme" {
		t.Errorf("README.md = %q — a payload only the later product ships must still land", got)
	}
	if got := out[0].Manifest["group_seed"]; len(got) != 2 {
		t.Errorf("a owns %v, want PRODUCT.md + BRANDING.md", got)
	}
	if got := out[1].Manifest["group_seed"]; len(got) != 1 || got[0] != "README.md" {
		t.Errorf("b owns %v, want only README.md (a won PRODUCT.md)", got)
	}
}

// TestBlendSkillsLastWinsWholesale locks "skills/: union by name; LAST product
// wins wholesale" — the winner supplies the whole dir, so a file only the loser
// shipped must not survive as a half-merged leftover.
func TestBlendSkillsLastWinsWholesale(t *testing.T) {
	root := t.TempDir()
	a := mkProduct(t, root, "a", map[string]string{
		"skills/shared/SKILL.md":  "from a",
		"skills/shared/only-a.md": "a extra",
		"skills/onlya/SKILL.md":   "a own",
	})
	b := mkProduct(t, root, "b", map[string]string{
		"skills/shared/SKILL.md": "from b",
	})
	group := filepath.Join(root, "group")
	out := blendInto(t, group, []Product{a, b}, nil)

	if got := read(t, group, ".claude/skills/shared/SKILL.md"); got != "from b" {
		t.Errorf("shared/SKILL.md = %q, want the LAST provider's bytes", got)
	}
	if _, err := os.Stat(filepath.Join(group, ".claude/skills/shared/only-a.md")); !os.IsNotExist(err) {
		t.Error("loser's extra file survived — LAST wins WHOLESALE, not file-by-file")
	}
	if got := read(t, group, ".claude/skills/onlya/SKILL.md"); got != "a own" {
		t.Errorf("onlya/SKILL.md = %q — union by name must keep an uncontested skill", got)
	}
	if got := out[0].Manifest["skill"]; len(got) != 1 || got[0] != "onlya" {
		t.Errorf("a owns skills %v, want only onlya", got)
	}
	if got := out[1].Manifest["skill"]; len(got) != 1 || got[0] != "shared" {
		t.Errorf("b owns skills %v, want shared", got)
	}
}

// TestBlendPersonaFirstWins locks "PERSONA.md / SOUL.md: FIRST provider wins;
// later warned".
func TestBlendPersonaFirstWins(t *testing.T) {
	root := t.TempDir()
	a := mkProduct(t, root, "a", map[string]string{"PERSONA.md": "persona A"})
	b := mkProduct(t, root, "b", map[string]string{"PERSONA.md": "persona B"})
	group := filepath.Join(root, "group")
	out := blendInto(t, group, []Product{a, b}, nil)

	if got := read(t, group, "PERSONA.md"); got != "persona A" {
		t.Errorf("PERSONA.md = %q, want the FIRST provider's", got)
	}
	if len(out[0].Warnings) != 1 || !strings.Contains(out[0].Warnings[0], "b also ships PERSONA.md") {
		t.Errorf("warnings = %v, want the later provider named", out[0].Warnings)
	}
}

// TestBlendPersonaSoulCollisionWarns locks the spec's named trap: SOUL.md is
// renamed to PERSONA.md only at READ time and only when PERSONA.md is absent, so
// a mix carrying both strands one as a dead file. Silence there looks applied.
func TestBlendPersonaSoulCollisionWarns(t *testing.T) {
	root := t.TempDir()
	a := mkProduct(t, root, "a", map[string]string{"PERSONA.md": "persona A"})
	b := mkProduct(t, root, "b", map[string]string{"SOUL.md": "soul B"})
	group := filepath.Join(root, "group")
	out := blendInto(t, group, []Product{a, b}, nil)

	if _, err := os.Stat(filepath.Join(group, "SOUL.md")); !os.IsNotExist(err) {
		t.Error("SOUL.md was written — the PERSONA slot has one winner")
	}
	if len(out[0].Warnings) != 1 {
		t.Fatalf("warnings = %v, want exactly one", out[0].Warnings)
	}
	w := out[0].Warnings[0]
	if !strings.Contains(w, "SOUL.md") || !strings.Contains(w, "PERSONA.md") || !strings.Contains(w, "dead file") {
		t.Errorf("warning %q does not name both files and the consequence", w)
	}
}

// TestBlendClaudeMarkedRegions locks the CLAUDE.md row: each provider gets its
// own named region, regions are emitted in MIX ORDER, and operator text outside
// every region is never read, rewritten, or reordered.
func TestBlendClaudeMarkedRegions(t *testing.T) {
	root := t.TempDir()
	a := mkProduct(t, root, "a", map[string]string{"CLAUDE.md": "runbook A"})
	b := mkProduct(t, root, "b", map[string]string{"CLAUDE.md": "runbook B"})
	group := filepath.Join(root, "group")
	if err := os.MkdirAll(group, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(group, "CLAUDE.md"), []byte("operator note\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	blendInto(t, group, []Product{a, b}, nil)

	got := read(t, group, "CLAUDE.md")
	want := "operator note\n\n" +
		"<!-- arizuko:package:a BEGIN -->\nrunbook A\n<!-- arizuko:package:a END -->\n\n" +
		"<!-- arizuko:package:b BEGIN -->\nrunbook B\n<!-- arizuko:package:b END -->\n"
	if got != want {
		t.Errorf("CLAUDE.md =\n%q\nwant\n%q", got, want)
	}
}

// TestBlendClaudeRegionsAreSeedOnce locks the row's update column: a region that
// already exists is the group's own state and is not rewritten, while a product
// joining the mix later appends its own region without disturbing it.
func TestBlendClaudeRegionsAreSeedOnce(t *testing.T) {
	root := t.TempDir()
	a := mkProduct(t, root, "a", map[string]string{"CLAUDE.md": "runbook A"})
	b := mkProduct(t, root, "b", map[string]string{"CLAUDE.md": "runbook B"})
	group := filepath.Join(root, "group")
	blendInto(t, group, []Product{a}, nil)

	// The agent edits inside a's region — its state now, not the product's.
	edited := strings.Replace(read(t, group, "CLAUDE.md"), "runbook A", "runbook A, amended", 1)
	if err := os.WriteFile(filepath.Join(group, "CLAUDE.md"), []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	blendInto(t, group, []Product{a, b}, nil)

	got := read(t, group, "CLAUDE.md")
	if !strings.Contains(got, "runbook A, amended") {
		t.Error("an existing region was rewritten — CLAUDE.md is seed-once")
	}
	if !strings.Contains(got, "<!-- arizuko:package:b BEGIN -->") {
		t.Error("the newly added product got no region")
	}
	if strings.Count(got, "arizuko:package:a BEGIN") != 1 {
		t.Error("a's region was duplicated")
	}
}

// TestClaudeRegionsMalformedRefuses is BUGS C6's rule restated for markdown: an
// unbalanced or duplicated marker is corruption, so the blend refuses to rewrite
// the file rather than best-effort truncating everything after it.
func TestClaudeRegionsMalformedRefuses(t *testing.T) {
	cases := map[string]string{
		"begin with no end":  "<!-- arizuko:package:a BEGIN -->\nbody\n",
		"end before begin":   "<!-- arizuko:package:a END -->\n",
		"duplicate region":   "<!-- arizuko:package:a BEGIN -->\nx\n<!-- arizuko:package:a END -->\n<!-- arizuko:package:a BEGIN -->\ny\n<!-- arizuko:package:a END -->\n",
		"nested region":      "<!-- arizuko:package:a BEGIN -->\n<!-- arizuko:package:b BEGIN -->\n",
		"mismatched closing": "<!-- arizuko:package:a BEGIN -->\n<!-- arizuko:package:b END -->\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := claudeRegions(body); err == nil {
				t.Fatal("malformed markers accepted")
			}
		})
	}
	if _, err := claudeRegions("head\n<!-- arizuko:package:a BEGIN -->\nx\n<!-- arizuko:package:a END -->\ntail\n"); err != nil {
		t.Fatalf("well-formed file rejected: %v", err)
	}
}

// TestBlendClaudeMalformedLeavesFileAlone proves the refusal is a refusal: the
// existing bytes survive intact and no product is recorded as applied.
func TestBlendClaudeMalformedLeavesFileAlone(t *testing.T) {
	root := t.TempDir()
	a := mkProduct(t, root, "a", map[string]string{"CLAUDE.md": "runbook A"})
	group := filepath.Join(root, "group")
	if err := os.MkdirAll(group, 0o755); err != nil {
		t.Fatal(err)
	}
	corrupt := "keep me\n<!-- arizuko:package:x BEGIN -->\nhalf\n"
	if err := os.WriteFile(filepath.Join(group, "CLAUDE.md"), []byte(corrupt), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := BlendProducts(group, []Product{a}, nil); err == nil {
		t.Fatal("blend accepted a malformed CLAUDE.md")
	}
	if got := read(t, group, "CLAUDE.md"); got != corrupt {
		t.Errorf("CLAUDE.md was rewritten to %q despite the refusal", got)
	}
}

// TestBlendUnionCollisionRefuses locks "facts/, tasks.toml: union; filename
// collision = refuse" — and that the refusal happens before any byte is written,
// so a mix never lands half-composed.
func TestBlendUnionCollisionRefuses(t *testing.T) {
	root := t.TempDir()
	a := mkProduct(t, root, "a", map[string]string{"facts/x.md": "a", "PRODUCT.md": "a"})
	b := mkProduct(t, root, "b", map[string]string{"facts/x.md": "b"})
	group := filepath.Join(root, "group")
	err := func() error { _, e := BlendProducts(group, []Product{a, b}, nil); return e }()
	if err == nil {
		t.Fatal("colliding facts/ filename accepted")
	}
	if !strings.Contains(err.Error(), "facts/x.md") {
		t.Errorf("error %q does not name the colliding path", err)
	}
	if _, sErr := os.Stat(filepath.Join(group, "PRODUCT.md")); !os.IsNotExist(sErr) {
		t.Error("a refused mix still wrote files")
	}
}

// TestBlendUnionKindsMerge locks the other half of the union row: distinct
// filenames from different products all land.
func TestBlendUnionKindsMerge(t *testing.T) {
	root := t.TempDir()
	a := mkProduct(t, root, "a", map[string]string{"facts/a.md": "fa", "tasks.toml": "ta"})
	b := mkProduct(t, root, "b", map[string]string{"facts/b.md": "fb", "migrations/001-x.md": "m"})
	group := filepath.Join(root, "group")
	blendInto(t, group, []Product{a, b}, nil)

	for rel, want := range map[string]string{
		"facts/a.md": "fa", "facts/b.md": "fb", "tasks.toml": "ta", "migrations/001-x.md": "m",
	} {
		if got := read(t, group, filepath.FromSlash(rel)); got != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}
}

// TestBlendDockerfileAtMostOne locks "Dockerfile.ant: at most one in the mix".
func TestBlendDockerfileAtMostOne(t *testing.T) {
	root := t.TempDir()
	a := mkProduct(t, root, "a", map[string]string{"Dockerfile.ant": "FROM arizuko-ant\n"})
	b := mkProduct(t, root, "b", map[string]string{"Dockerfile.ant": "FROM arizuko-ant\nRUN x\n"})
	group := filepath.Join(root, "group")

	out := blendInto(t, group, []Product{a}, nil)
	if got := read(t, group, "Dockerfile.ant"); got != "FROM arizuko-ant\n" {
		t.Errorf("Dockerfile.ant = %q", got)
	}
	if got := out[0].Manifest["image_extension"]; len(got) != 1 {
		t.Errorf("image_extension manifest = %v", got)
	}
	if _, err := BlendProducts(filepath.Join(root, "g2"), []Product{a, b}, nil); err == nil {
		t.Fatal("two Dockerfile.ant in one mix accepted")
	}
}

// TestBlendSettingsMcpServersUnion locks "settings.json mcpServers: map union;
// name collision = refuse", written where arizuko actually reads it.
func TestBlendSettingsMcpServersUnion(t *testing.T) {
	root := t.TempDir()
	a := mkProduct(t, root, "a", map[string]string{
		".claude/settings.json": `{"mcpServers":{"alpha":{"command":"a"}},"outputStyle":"telegram"}`,
	})
	b := mkProduct(t, root, "b", map[string]string{
		".claude/settings.json": `{"mcpServers":{"beta":{"command":"b"}}}`,
	})
	group := filepath.Join(root, "group")
	out := blendInto(t, group, []Product{a, b}, nil)

	var s map[string]any
	if err := json.Unmarshal([]byte(read(t, group, ".claude/settings.json")), &s); err != nil {
		t.Fatal(err)
	}
	servers, _ := s["mcpServers"].(map[string]any)
	if servers["alpha"] == nil || servers["beta"] == nil {
		t.Errorf("mcpServers = %v, want both providers unioned", servers)
	}
	if s["outputStyle"] != "telegram" {
		t.Errorf("outputStyle = %v — a non-mcpServers key must survive the union", s["outputStyle"])
	}
	if got := out[0].Manifest["mcp_server"]; len(got) != 1 || got[0] != "alpha" {
		t.Errorf("a owns mcp servers %v", got)
	}

	dup := mkProduct(t, root, "dup", map[string]string{
		".claude/settings.json": `{"mcpServers":{"alpha":{"command":"c"}}}`,
	})
	if _, err := BlendProducts(filepath.Join(root, "g2"), []Product{a, dup}, nil); err == nil {
		t.Fatal("mcpServers name collision accepted")
	}
}

// TestBlendIsIdempotent proves re-applying an unchanged mix writes nothing.
// Content hashes, not timestamps: a second-granular RFC3339 comparison passes
// even when every byte is rewritten.
func TestBlendIsIdempotent(t *testing.T) {
	root := t.TempDir()
	a := mkProduct(t, root, "a", map[string]string{
		"PRODUCT.md": "name = \"a\"\n", "PERSONA.md": "p", "CLAUDE.md": "c",
		"facts/f.md": "f", "skills/s/SKILL.md": "sk",
	})
	group := filepath.Join(root, "group")
	first := blendInto(t, group, []Product{a}, nil)
	if !first[0].Changed {
		t.Fatal("first apply reported no change")
	}
	before := walkFiles(t, group)

	prior := map[string]map[string]string{"a": first[0].AssetHashes}
	second := blendInto(t, group, []Product{a}, prior)
	if second[0].Changed {
		t.Error("re-applying an unchanged mix reported a change")
	}
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

// TestBlendSeedOnceKeepsLocalEdits locks the seed/managed split: persona and
// facts become the group's own state at seed time and are never re-written, even
// when the product's bytes change upstream.
func TestBlendSeedOnceKeepsLocalEdits(t *testing.T) {
	root := t.TempDir()
	a := mkProduct(t, root, "a", map[string]string{"PERSONA.md": "v1", "facts/f.md": "f1"})
	group := filepath.Join(root, "group")
	first := blendInto(t, group, []Product{a}, nil)

	if err := os.WriteFile(filepath.Join(group, "PERSONA.md"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	mkProduct(t, root, "a", map[string]string{"PERSONA.md": "v2", "facts/f.md": "f2"})
	blendInto(t, group, []Product{a}, map[string]map[string]string{"a": first[0].AssetHashes})

	if got := read(t, group, "PERSONA.md"); got != "mine" {
		t.Errorf("PERSONA.md = %q — a seed-once payload must never be re-written", got)
	}
	if got := read(t, group, "facts/f.md"); got != "f1" {
		t.Errorf("facts/f.md = %q — seed-once, so the upstream change must not land", got)
	}
}

// TestBlendManagedRefusesDirty locks the no-blind-overwrite rule for the managed
// half: a skill file edited after install stops that skill being replaced and is
// reported, instead of the operator's edit vanishing under a new revision.
func TestBlendManagedRefusesDirty(t *testing.T) {
	root := t.TempDir()
	a := mkProduct(t, root, "a", map[string]string{"skills/s/SKILL.md": "v1"})
	group := filepath.Join(root, "group")
	first := blendInto(t, group, []Product{a}, nil)
	if got := read(t, group, ".claude/skills/s/SKILL.md"); got != "v1" {
		t.Fatalf("seeded skill = %q", got)
	}

	if err := os.WriteFile(filepath.Join(group, ".claude/skills/s/SKILL.md"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	mkProduct(t, root, "a", map[string]string{"skills/s/SKILL.md": "v2"})
	out := blendInto(t, group, []Product{a}, map[string]map[string]string{"a": first[0].AssetHashes})

	if got := read(t, group, ".claude/skills/s/SKILL.md"); got != "mine" {
		t.Errorf("skill = %q — a dirty managed asset must not be overwritten", got)
	}
	if len(out[0].Dirty) != 1 || !strings.Contains(out[0].Dirty[0], "skills/s/SKILL.md") {
		t.Errorf("Dirty = %v, want the edited skill file named", out[0].Dirty)
	}
}

// TestBlendDirtyRefusalSurvivesTheNextApply: reporting a dirty asset once is not
// protection. The record that same apply writes must still carry the hash
// dirty-detection compares against — drop it and the NEXT apply sees a
// never-recorded file, calls the edit clean, and overwrites exactly what the
// refusal protected.
func TestBlendDirtyRefusalSurvivesTheNextApply(t *testing.T) {
	root := t.TempDir()
	a := mkProduct(t, root, "a", map[string]string{"skills/s/SKILL.md": "v1"})
	group := filepath.Join(root, "group")
	first := blendInto(t, group, []Product{a}, nil)

	if err := os.WriteFile(filepath.Join(group, ".claude/skills/s/SKILL.md"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	mkProduct(t, root, "a", map[string]string{"skills/s/SKILL.md": "v2"})

	second := blendInto(t, group, []Product{a}, map[string]map[string]string{"a": first[0].AssetHashes})
	if len(second[0].Dirty) != 1 {
		t.Fatalf("second apply Dirty = %v, want the edited skill", second[0].Dirty)
	}
	third := blendInto(t, group, []Product{a}, map[string]map[string]string{"a": second[0].AssetHashes})
	if len(third[0].Dirty) != 1 {
		t.Errorf("third apply Dirty = %v — the refusal did not survive the record it wrote", third[0].Dirty)
	}
	if got := read(t, group, ".claude/skills/s/SKILL.md"); got != "mine" {
		t.Errorf("skill = %q — the local edit was overwritten on the third apply", got)
	}
	if got := second[0].Manifest["skill"]; len(got) != 1 || got[0] != "s" {
		t.Errorf("a skipped skill dropped out of the manifest: %v", got)
	}
}

// TestBlendManagedReplacesClean is the other side of the same rule: an untouched
// managed asset DOES take the new revision, so dirty-refusal is not a synonym
// for never updating.
func TestBlendManagedReplacesClean(t *testing.T) {
	root := t.TempDir()
	a := mkProduct(t, root, "a", map[string]string{"skills/s/SKILL.md": "v1"})
	group := filepath.Join(root, "group")
	first := blendInto(t, group, []Product{a}, nil)

	mkProduct(t, root, "a", map[string]string{"skills/s/SKILL.md": "v2"})
	out := blendInto(t, group, []Product{a}, map[string]map[string]string{"a": first[0].AssetHashes})

	if got := read(t, group, ".claude/skills/s/SKILL.md"); got != "v2" {
		t.Errorf("skill = %q, want the new revision on a clean asset", got)
	}
	if !out[0].Changed || len(out[0].Dirty) != 0 {
		t.Errorf("Changed=%v Dirty=%v", out[0].Changed, out[0].Dirty)
	}
}

// TestBlendDropsStaleAsset locks the record's other job: a file a product owned
// and no longer ships is removed, unless it was edited locally — deleting that
// loses exactly as much as overwriting it would.
func TestBlendDropsStaleAsset(t *testing.T) {
	root := t.TempDir()
	a := mkProduct(t, root, "a", map[string]string{
		"skills/s/SKILL.md": "v1", "skills/s/gone.md": "g", "skills/s/kept.md": "k",
	})
	group := filepath.Join(root, "group")
	first := blendInto(t, group, []Product{a}, nil)
	if err := os.WriteFile(filepath.Join(group, ".claude/skills/s/kept.md"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(filepath.Join(root, "a", "skills", "s", "gone.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "a", "skills", "s", "kept.md")); err != nil {
		t.Fatal(err)
	}
	out := blendInto(t, group, []Product{a}, map[string]map[string]string{"a": first[0].AssetHashes})

	if _, err := os.Stat(filepath.Join(group, ".claude/skills/s/gone.md")); !os.IsNotExist(err) {
		t.Error("an asset the product stopped shipping was not dropped")
	}
	if got := read(t, group, ".claude/skills/s/kept.md"); got != "mine" {
		t.Errorf("kept.md = %q — a locally edited stale asset must survive", got)
	}
	if len(out[0].Warnings) != 1 || !strings.Contains(out[0].Warnings[0], "kept.md") {
		t.Errorf("warnings = %v, want the kept stale asset reported", out[0].Warnings)
	}
}

// TestClassifyCoversTheBlendTable pins each table row to the rule it selects, so
// a future edit that quietly reroutes a payload (dropping PRODUCT.md into a
// named rule, say) fails here rather than in a live group.
func TestClassifyCoversTheBlendTable(t *testing.T) {
	want := map[string]payload{
		"skills/foo/SKILL.md":   paySkill,
		"PERSONA.md":            payPersona,
		"SOUL.md":               payPersona,
		"CLAUDE.md":             payClaude,
		"facts/x.md":            payUnion,
		"tasks.toml":            payUnion,
		"migrations/001-x.md":   payUnion,
		".claude/settings.json": paySettings,
		"Dockerfile.ant":        payDockerfile,
		"PRODUCT.md":            payVerbatim,
		"BRANDING.md":           payVerbatim,
		"settings.json":         payVerbatim,
		"skills":                payVerbatim,
	}
	for rel, k := range want {
		if got := classify(rel); got != k {
			t.Errorf("classify(%q) = %v, want %v", rel, got, k)
		}
	}
}

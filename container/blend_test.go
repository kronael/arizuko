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
	if _, found := files[ManifestFile]; !found {
		files[ManifestFile] = "name: " + name + "\n"
	}
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

func TestProductSeedSkipsRootGit(t *testing.T) {
	for _, mode := range []string{"blend", "seed"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			product := mkProduct(t, root, "product", map[string]string{
				".git/config":        "clone metadata",
				"PRODUCT.md":         "name = \"product\"\n",
				"assets/.git/config": "product asset",
			})
			group := filepath.Join(root, "group")
			if mode == "blend" {
				blendInto(t, group, []Product{product}, nil)
			} else if err := SeedProduct(group, product.Dir); err != nil {
				t.Fatal(err)
			}
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

// TestBlendCatchAllCopiesUnnamedPayload locks spec 5/28-packages's last table row: a
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

// TestBlendClaudeRegionsAreSeedOnce locks the row's update column: a region that
// already exists is the group's own state and is not rewritten, while a product
// joining the mix later appends its own region without disturbing it.

// TestClaudeRegionsMalformedRefuses is BUGS C6's rule restated for markdown: an
// unbalanced or duplicated marker is corruption, so the blend refuses to rewrite
// the file rather than best-effort truncating everything after it.

// TestBlendClaudeMalformedLeavesFileAlone proves the refusal is a refusal: the
// existing bytes survive intact and no product is recorded as applied.

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
	a := mkProduct(t, root, "a", map[string]string{"facts/a.md": "fa", "facts/c.md": "fc"})
	b := mkProduct(t, root, "b", map[string]string{"facts/b.md": "fb"})
	group := filepath.Join(root, "group")
	blendInto(t, group, []Product{a, b}, nil)

	for rel, want := range map[string]string{
		"facts/a.md": "fa", "facts/b.md": "fb", "facts/c.md": "fc",
	} {
		if got := read(t, group, filepath.FromSlash(rel)); got != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}
}

// TestBlendRefusesProductMigrations locks spec 5/28-packages §"Running a product's
// migrations/": no runner exists, so seeding the payload would report success
// for an asset that never executes — the settings.json mcpServers no-op again.
func TestBlendRefusesProductMigrations(t *testing.T) {
	root := t.TempDir()
	a := mkProduct(t, root, "a", map[string]string{
		"migrations/001-x.md": "do the thing", "PRODUCT.md": "a",
	})
	group := filepath.Join(root, "group")

	_, err := BlendProducts(group, []Product{a}, nil)
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
// not seeded. Nothing ever read it — no parser, no schema, no seeder — while
// 5/21 told authors it seeded scheduled tasks, so the file landed in the group
// and no task was created. Third payload of the same class, third refusal
// (mcpServers J15, migrations/ 5/28-packages).
func TestBlendRefusesProductTasks(t *testing.T) {
	root := t.TempDir()
	a := mkProduct(t, root, "a", map[string]string{
		"tasks.toml": "[[task]]\ncron = \"0 9 * * *\"\n", "PRODUCT.md": "a",
	})
	group := filepath.Join(root, "group")

	_, err := BlendProducts(group, []Product{a}, nil)
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

// The verbatim single-product seeder refuses the same payloads as the blend:
// one map, both seeders, so neither accepts what the other rejects.
func TestCheckProductAssetsRefusesWhatTheBlendDoes(t *testing.T) {
	root := t.TempDir()
	ok := mkProduct(t, root, "ok", map[string]string{"PERSONA.md": "p", "facts/a.md": "f"})
	if err := CheckProductAssets(ok.Name, ok.Dir); err != nil {
		t.Fatalf("clean product refused: %v", err)
	}
	for name, rel := range map[string]string{
		"migrations": "migrations/002-y.md",
		"tasks":      "tasks.toml",
	} {
		bad := mkProduct(t, root, "bad-"+name, map[string]string{rel: "x"})
		if err := CheckProductAssets(bad.Name, bad.Dir); err == nil {
			t.Errorf("verbatim seed accepted %s, which the blend refuses", rel)
		}
	}
}

// TestRefusedPayloadsNameARoute: every refusal must tell the author where the
// intent DOES work. A bare "not supported" turns a fixable authoring error into
// a dead end, which is the failure the refusals exist to avoid.
func TestRefusedPayloadsNameARoute(t *testing.T) {
	for kind, reason := range refusedPayloads {
		if !strings.Contains(reason, "—") || !strings.Contains(reason, "spec 5/28") {
			t.Errorf("refusal for payload %v must name the working route and its spec: %q", kind, reason)
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

	_, err := BlendProducts(filepath.Join(root, "group"), []Product{a}, nil)
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
	if _, err := BlendProducts(filepath.Join(root, "g2"), []Product{a, dup}, nil); err == nil {
		t.Fatal("a settings.json key collision was accepted")
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

// TestBlendDirtyRefusalSurvivesTheNextApply: reporting a dirty asset once is not
// protection. The record that same apply writes must still carry the hash
// dirty-detection compares against — drop it and the NEXT apply sees a
// never-recorded file, calls the edit clean, and overwrites exactly what the
// refusal protected.

// TestBlendManagedReplacesClean is the other side of the same rule: an untouched
// managed asset DOES take the new revision, so dirty-refusal is not a synonym
// for never updating.

// TestBlendDropsStaleAsset locks the record's other job: a file a product owned
// and no longer ships is removed, unless it was edited locally — deleting that
// loses exactly as much as overwriting it would.

// TestBlendHandoffKeepsTheAsset is BUGS L34: `a` drops an asset, `b` picks it up
// with IDENTICAL bytes, both stay in the mix. The identical bytes are the whole
// trap — the on-disk hash still matches `a`'s record, so against `a`'s own
// hashes alone the file reads as clean-and-no-longer-mine and `a` deleted what
// `b` had just written, while the apply reported `b unchanged`.

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
		"tasks.toml":            payTasks,
		"migrations/001-x.md":   payMigrations,
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

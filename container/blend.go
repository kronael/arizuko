package container

// blend.go is spec 5/28-packages's composition layer: a group blends an ORDERED MIX of
// products. Two providers share no merge base, so blending is per PAYLOAD KIND and
// never a content merge — each kind gets one rule from the spec's table, whose last
// row is a catch-all: a payload no rule names is copied whole, first-provider-wins.
//
// Seed vs managed is per KIND, not per product. Identity and knowledge seed once
// and become the group's own state; skills stay upstream-managed and are
// re-applied — but never over a locally edited asset, which is reported dirty.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/kronael/arizuko/chanlib"
)

// Product is one entry of a group's ordered mix. Name is arizuko.toml's `name` —
// the installed-package record key and the CLAUDE.md region id; Dir is the
// resolved local tree (a local path or a fetched clone).
type Product struct {
	Name     string
	Dir      string
	Snapshot *PackageSnapshot
}

// Blended reports one product's contribution: the identities its
// installed-package record owns (asset kind -> ids), the content hash of every
// file it provides, whether anything actually moved on disk, the assets skipped
// because they were edited after the last apply, and non-fatal collisions.
type Blended struct {
	Name        string
	Manifest    map[string][]string
	AssetHashes map[string]string
	Changed     bool
	Dirty       []string
	Warnings    []string
}

// settingsRel is the ONLY settings.json arizuko reads (seedSettings,
// runner.go). A product's mix payload therefore mirrors the group home at that
// path; a root-level settings.json is an unnamed payload and falls to the
// verbatim catch-all like any other file.
const settingsRel = ".claude/settings.json"

// Pending settings are prospective value hashes, bound to an immutable package
// digest and the SHA-256 of its configured source. They are not applied assets.
const (
	PendingSettingPrefix    = "pending:setting:"
	PendingSettingsArtifact = "pending:settings:package_source"
	PendingSettingsSource   = "pending:settings:source"
)

// HasPendingSettings also reports incomplete binding metadata as pending.
func HasPendingSettings(hashes map[string]string) bool {
	for key := range hashes {
		if key == PendingSettingsArtifact || key == PendingSettingsSource ||
			strings.HasPrefix(key, PendingSettingPrefix) {
			return true
		}
	}
	return false
}

// ProductBlend holds one preflight and its rendered payloads until receipt preparation completes.
type ProductBlend struct {
	Blended  []Blended
	groupDir string
	mix      []Product
	prior    map[string]map[string]string
	plan     *plan
	settings *renderedSettings
	claude   []byte
}

type renderedSettings struct {
	data     []byte
	previous string
}

// skillsRel is where a product's bundled skills land: the group's own skill dir,
// which seedSkills layers stock skills into afterwards (cpDirFresh only writes
// missing files, so a product's skill survives a same-named stock one).
const skillsRel = ".claude/skills"

// payload is the blend rule one file falls under (spec 5/28-packages's table).
type payload int

const (
	payVerbatim   payload = iota // anything else — verbatim copy, FIRST wins
	paySkill                     // skills/<name>/ — union by name, LAST wins wholesale
	payPersona                   // PERSONA.md / SOUL.md — FIRST wins, later warned
	payClaude                    // CLAUDE.md — marked sections, in mix order
	payUnion                     // facts/ — union, collision refuses
	paySettings                  // .claude/settings.json — key union; mcpServers refused
	payDockerfile                // Dockerfile.ant — at most one in the mix
	payMigrations                // migrations/ — refused: nothing runs a product's migrations
	payTasks                     // tasks.toml — refused: nothing reads a product's tasks.toml
)

// productDest maps a product-relative path to where it lands in the group home.
// ONE placement rule, shared by the blend and the verbatim single-product seed, so
// a product means the same thing however it is applied. Only skills relocate, and
// they must: seedSkills and the agent both read .claude/skills, so a skills/<n>/
// left at the group root reaches no consumer (BUGS L37).
func productDest(rel string) string {
	if classify(rel) == paySkill {
		return skillsRel + strings.TrimPrefix(rel, "skills")
	}
	return rel
}

// classify maps a product-relative path (slash-separated) to its blend rule.
func classify(rel string) payload {
	parts := strings.Split(rel, "/")
	switch {
	case parts[0] == "skills" && len(parts) > 1:
		return paySkill
	case rel == "PERSONA.md" || rel == "SOUL.md":
		return payPersona
	case rel == "CLAUDE.md":
		return payClaude
	case parts[0] == "facts" && len(parts) > 1:
		return payUnion
	case parts[0] == "migrations" && len(parts) > 1:
		return payMigrations
	case rel == "tasks.toml":
		return payTasks
	case rel == settingsRel:
		return paySettings
	case rel == "Dockerfile.ant":
		return payDockerfile
	}
	return payVerbatim
}

// refusedPayloads names the product payloads arizuko will not seed, each with the
// route that does work. A payload that applies cleanly and reaches no consumer is
// an authoring error: it reports success and then does nothing (BUGS J15). ONE map,
// so the two seeders cannot drift and adding a refusal is a row.
//
// tasks.toml — a scheduled task is a scheduled_tasks ROW, created through the
// resource's own handler. No parser, no schema type, no seeder ever existed; 5/21
// claimed otherwise for the whole life of the payload (BUGS L35).
var refusedPayloads = map[payload]string{
	payMigrations: "nothing runs a product's migrations/ — " +
		"put the instructions in the product's CLAUDE.md or a skill, which the agent reads (spec 5/28)",
	payTasks: "nothing reads a product's tasks.toml — " +
		"a scheduled task is a scheduled_tasks row, so create it with schedule_task, the dashboard " +
		"or POST /v1/tasks, or have the product's CLAUDE.md tell the agent to (spec 5/28)",
}

func refusePayload(product, rel string, kind payload) error {
	return fmt.Errorf("%s: %s ships it, and %s", rel, product, refusedPayloads[kind])
}

// CheckProductAssets refuses a product tree carrying a payload no consumer reads.
// Both seeders call it — the blend through buildPlan, the verbatim single-product
// seed through SetupGroup — so neither path can quietly accept what the other
// rejects.
func CheckProductAssets(product, dir string) error {
	rels, err := productFiles(dir)
	if err != nil {
		return err
	}
	for _, rel := range rels {
		if kind := classify(rel); refusedPayloads[kind] != "" {
			return refusePayload(product, rel, kind)
		}
	}
	return nil
}

// SeedProduct copies product payloads into groupDir, excluding the source manifest.
// It is the single-product seed
// behind `arizuko create --product` and `group add --product`. It is
// productDest over a symlink-skipping walk: same bytes, but each file lands
// where its payload kind is read rather than where the author happened to put it.
func SeedProduct(groupDir, dir string) error {
	rels, err := productFiles(dir)
	if err != nil {
		return err
	}
	for _, rel := range rels {
		dst := filepath.Join(groupDir, filepath.FromSlash(productDest(rel)))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := chanlib.CopyFile(filepath.Join(dir, filepath.FromSlash(rel)), dst); err != nil {
			return err
		}
	}
	return nil
}

// productFiles lists a product's regular files by slash-separated relative path.
// Symlinks are skipped: copying their targets would leak arbitrary host files.
func productFiles(dir string) ([]string, error) {
	if _, err := ReadProductManifest(dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	var rels []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && p == filepath.Join(dir, ".git") {
			return fs.SkipDir
		}
		if d.IsDir() || d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil {
			return rerr
		}
		if rel != ManifestFile {
			rels = append(rels, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(rels)
	return rels, err
}

func hashBytes(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// plan is the resolved mix: who owns each contested slot, decided before a
// single byte is written so a collision refuses the whole blend rather than
// leaving a half-composed group.
type plan struct {
	skill      map[string]int   // skill dir name -> product index (LAST wins)
	persona    int              // product index, -1 = none
	personaRel string           // the winner's own filename (PERSONA.md or SOUL.md)
	claude     []int            // product indices shipping CLAUDE.md, in mix order
	union      map[string]int   // rel -> product index (collision refuses)
	settings   []int            // product indices shipping .claude/settings.json
	dockerfile int              // product index, -1 = none
	verbatim   map[string]int   // rel -> product index (FIRST wins)
	files      map[int][]string // product index -> its relative paths
	warnings   []string
}

// buildPlan applies the blend table to the mix. Errors are the table's "refuse"
// cells: a payload that cannot be blended without silently losing one provider's
// bytes stops the apply and names both products.
func buildPlan(mix []Product) (*plan, error) {
	p := &plan{
		skill: map[string]int{}, persona: -1, union: map[string]int{},
		dockerfile: -1, verbatim: map[string]int{}, files: map[int][]string{},
	}
	for i, prod := range mix {
		rels, err := productFiles(prod.Dir)
		if err != nil {
			return nil, fmt.Errorf("read product %s: %w", prod.Name, err)
		}
		p.files[i] = rels
		for _, rel := range rels {
			kind := classify(rel)
			switch kind {
			case paySkill:
				if prod.Snapshot == nil {
					p.skill[strings.Split(rel, "/")[1]] = i
				}
			case payPersona:
				if p.persona == -1 {
					p.persona, p.personaRel = i, rel
					continue
				}
				if p.personaRel == rel {
					p.warnings = append(p.warnings, fmt.Sprintf(
						"%s also ships %s — %s wins, later providers are ignored",
						prod.Name, rel, mix[p.persona].Name))
					continue
				}
				// SOUL.md is the legacy PERSONA.md name, renamed only at READ
				// time and only when PERSONA.md is absent (runner.go). A mix
				// carrying both keeps the PERSONA.md one and strands the other
				// as a dead file, so say so rather than let it look applied.
				p.warnings = append(p.warnings, fmt.Sprintf(
					"%s ships %s and %s ships %s — SOUL.md is the legacy PERSONA.md name, "+
						"so only %s is ever read; the other is a dead file",
					mix[p.persona].Name, p.personaRel, prod.Name, rel, p.personaRel))
			case payClaude:
				p.claude = append(p.claude, i)
			case payUnion:
				if owner, dup := p.union[rel]; dup {
					return nil, fmt.Errorf("%s: both %s and %s ship it — union payloads refuse a filename collision",
						rel, mix[owner].Name, prod.Name)
				}
				p.union[rel] = i
			case paySettings:
				p.settings = append(p.settings, i)
			case payMigrations, payTasks:
				return nil, refusePayload(prod.Name, rel, kind)
			case payDockerfile:
				if p.dockerfile != -1 {
					return nil, fmt.Errorf("both %s and %s ship a Dockerfile.ant — at most one image extension per mix",
						mix[p.dockerfile].Name, prod.Name)
				}
				p.dockerfile = i
			default:
				if _, taken := p.verbatim[rel]; !taken {
					p.verbatim[rel] = i
				}
			}
		}
	}
	return p, nil
}

// BlendProducts writes an ordered product mix into groupDir per spec 5/28-packages's
// blend table and reports what each product owns. prior maps a product name to
// the AssetHashes its last apply recorded; a managed asset whose bytes moved
// since then is reported dirty and left alone (no blind overwrite).
func BlendProducts(groupDir string, mix []Product, prior map[string]map[string]string) ([]Blended, error) {
	blend, err := PrepareProductBlend(groupDir, mix, prior)
	if err != nil {
		return nil, err
	}
	return blend.Apply()
}

// PrepareProductBlend renders without writing. Prospective settings use PendingSettingPrefix until Apply succeeds.
func PrepareProductBlend(groupDir string, mix []Product,
	prior map[string]map[string]string) (*ProductBlend, error) {
	p, err := buildPlan(mix)
	if err != nil {
		return nil, err
	}
	out := make([]Blended, len(mix))
	for i := range mix {
		out[i] = Blended{Name: mix[i].Name, Manifest: map[string][]string{}, AssetHashes: map[string]string{}}
	}
	if len(p.warnings) > 0 {
		out[0].Warnings = append(out[0].Warnings, p.warnings...)
	}
	settings, err := renderBlendSettings(groupDir, mix, p, prior, out, true)
	if err != nil {
		return nil, err
	}
	claude, err := renderBlendClaude(groupDir, mix, p, out)
	if err != nil {
		return nil, err
	}
	return &ProductBlend{out, groupDir, mix, prior, p, settings, claude}, nil
}

// Apply confirms prospective settings only after their payload write succeeds.
func (blend *ProductBlend) Apply() ([]Blended, error) {
	groupDir, mix, prior := blend.groupDir, blend.mix, blend.prior
	p, out := blend.plan, blend.Blended

	if err := blendSkills(groupDir, mix, p, prior, out); err != nil {
		return nil, err
	}
	if err := blendSeedOnce(groupDir, mix, p, prior, out); err != nil {
		return nil, err
	}
	if blend.claude != nil {
		if _, err := writeIfDifferent(groupDir, "CLAUDE.md", blend.claude); err != nil {
			return nil, err
		}
	}
	if blend.settings != nil {
		wrote, err := writeBlendSettings(groupDir, blend.settings)
		if err != nil {
			return nil, err
		}
		if wrote {
			for _, i := range p.settings {
				out[i].Changed = true
			}
		}
	}
	for i := range out {
		for key, hash := range out[i].AssetHashes {
			if name, pending := strings.CutPrefix(key, PendingSettingPrefix); pending {
				out[i].AssetHashes["setting:"+name] = hash
				delete(out[i].AssetHashes, key)
				if !slices.Contains(out[i].Manifest["setting"], name) {
					out[i].Manifest["setting"] = append(out[i].Manifest["setting"], name)
				}
			}
		}
		sort.Strings(out[i].Manifest["setting"])
	}
	// Drop what a product owned last time and no longer ships. "No longer
	// shipped" is a property of the WHOLE mix, not of one product: handing an
	// asset from A to B is a supported move, and against A's own hashes alone it
	// reads as A dropping a clean file, so A deleted what B had just written
	// (BUGS L34). The union is the shipped set; ownership moved, the file did
	// not.
	shipped := map[string]string{}
	for i := range out {
		for k, h := range out[i].AssetHashes {
			shipped[k] = h
		}
	}
	for i := range mix {
		dropped, warns, err := dropStale(groupDir, prior[mix[i].Name], shipped)
		if err != nil {
			return nil, err
		}
		out[i].Changed = out[i].Changed || dropped
		out[i].Warnings = append(out[i].Warnings, warns...)
	}
	return out, nil
}

// blendSkills applies "union by name; LAST product wins wholesale". Skills are a
// managed payload, so a locally edited skill file is reported dirty and the
// whole skill is left untouched.
func blendSkills(groupDir string, mix []Product, p *plan, prior map[string]map[string]string, out []Blended) error {
	names := make([]string, 0, len(p.skill))
	for n := range p.skill {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		i := p.skill[name]
		recorded := prior[mix[i].Name]
		var files [][2]string // dst rel, src path
		for _, rel := range p.files[i] {
			if classify(rel) == paySkill && strings.Split(rel, "/")[1] == name {
				files = append(files, [2]string{
					productDest(rel),
					filepath.Join(mix[i].Dir, filepath.FromSlash(rel)),
				})
			}
		}
		dirty := false
		for _, f := range files {
			if isDirty(groupDir, f[0], recorded["file:"+f[0]]) {
				out[i].Dirty = append(out[i].Dirty, f[0])
				dirty = true
			}
		}
		if dirty {
			// Carry the recorded hashes and the ownership forward. Dropping
			// them would leave the NEXT apply with no prior hash for these
			// files, so it would call the operator's edit clean and overwrite
			// exactly what this refusal just protected — and dropStale would
			// see them as no longer owned. The refusal has to survive its own
			// report.
			for _, f := range files {
				if h := recorded["file:"+f[0]]; h != "" {
					out[i].AssetHashes["file:"+f[0]] = h
				}
			}
			out[i].Manifest["skill"] = append(out[i].Manifest["skill"], name)
			continue
		}
		for _, f := range files {
			b, err := os.ReadFile(f[1])
			if err != nil {
				return err
			}
			wrote, err := writeIfDifferent(groupDir, f[0], b)
			if err != nil {
				return err
			}
			out[i].Changed = out[i].Changed || wrote
			out[i].AssetHashes["file:"+f[0]] = hashBytes(b)
		}
		out[i].Manifest["skill"] = append(out[i].Manifest["skill"], name)
	}
	return nil
}

func blendSeedOnce(groupDir string, mix []Product, p *plan, prior map[string]map[string]string, out []Blended) error {
	type item struct {
		owner int
		src   string // product-relative
		dst   string // group-relative
		kind  string // manifest key
	}
	var items []item
	if p.persona != -1 {
		items = append(items, item{p.persona, p.personaRel, p.personaRel, "group_seed"})
	}
	if p.dockerfile != -1 {
		items = append(items, item{p.dockerfile, "Dockerfile.ant", "Dockerfile.ant", "image_extension"})
	}
	for _, m := range []map[string]int{p.union, p.verbatim} {
		rels := make([]string, 0, len(m))
		for rel := range m {
			rels = append(rels, rel)
		}
		sort.Strings(rels)
		for _, rel := range rels {
			items = append(items, item{m[rel], rel, rel, "group_seed"})
		}
	}
	// PERSONA.md/SOUL.md and Dockerfile.ant are also matched by the loop above
	// only if they fell to another rule; they do not, so no de-dup is needed.
	for _, it := range items {
		b, err := os.ReadFile(filepath.Join(mix[it.owner].Dir, filepath.FromSlash(it.src)))
		if err != nil {
			return err
		}
		wrote, err := seedOnce(groupDir, it.dst, b)
		if err != nil {
			return err
		}
		o := &out[it.owner]
		o.Changed = o.Changed || wrote
		hash := prior[mix[it.owner].Name]["file:"+it.dst]
		if wrote {
			hash = hashBytes(b)
		}
		if hash == "" {
			continue
		}
		o.AssetHashes["file:"+it.dst] = hash
		o.Manifest[it.kind] = append(o.Manifest[it.kind], it.dst)
	}
	for i := range out {
		sort.Strings(out[i].Manifest["group_seed"])
	}
	return nil
}

func renderBlendClaude(groupDir string, mix []Product, p *plan, out []Blended) ([]byte, error) {
	if len(p.claude) == 0 {
		return nil, nil
	}
	path := filepath.Join(groupDir, "CLAUDE.md")
	prev, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	present, err := claudeRegions(string(prev))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	body := strings.TrimRight(string(prev), "\n")
	added := false
	for _, i := range p.claude {
		name := mix[i].Name
		b, rerr := os.ReadFile(filepath.Join(mix[i].Dir, "CLAUDE.md"))
		if rerr != nil {
			return nil, rerr
		}
		region := regionBegin(name) + "\n" + strings.TrimRight(string(b), "\n") + "\n" + regionEnd(name)
		out[i].Manifest["claude_region"] = []string{name}
		if body, found := present[name]; found {
			out[i].AssetHashes["region:CLAUDE.md"] = hashBytes([]byte(body))
			continue
		}
		out[i].AssetHashes["region:CLAUDE.md"] = hashBytes([]byte(region))
		if body != "" {
			body += "\n\n"
		}
		body += region
		added = true
		out[i].Changed = true
	}
	if !added {
		return nil, nil
	}
	return []byte(body + "\n"), nil
}

func regionBegin(name string) string { return "<!-- arizuko:package:" + name + " BEGIN -->" }
func regionEnd(name string) string   { return "<!-- arizuko:package:" + name + " END -->" }

var regionRE = regexp.MustCompile(`^<!--\s*arizuko:package:(\S+)\s+(BEGIN|END)\s*-->$`)

// claudeRegions returns the product names with a well-formed region in s. Any
// malformation — a BEGIN with no END, a duplicate, an END before its BEGIN, a
// mismatched pair, or nesting — is corruption: refuse to rewrite rather than
// best-effort truncate (the fix BUGS C6 paid for in writeManagedEnv).
func claudeRegions(s string) (map[string]string, error) {
	present := map[string]string{}
	open := ""
	start := 0
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		m := regionRE.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		name, marker := m[1], m[2]
		if marker == "BEGIN" {
			if open != "" {
				return nil, fmt.Errorf("region %q begins inside region %q — resolve manually", name, open)
			}
			if _, found := present[name]; found {
				return nil, fmt.Errorf("duplicate region %q — resolve manually", name)
			}
			open = name
			start = i
			continue
		}
		if open == "" {
			return nil, fmt.Errorf("region %q ends with no preceding BEGIN — resolve manually", name)
		}
		if open != name {
			return nil, fmt.Errorf("region %q ends inside region %q — resolve manually", name, open)
		}
		present[name] = strings.Join(lines[start:i+1], "\n")
		open = ""
	}
	if open != "" {
		return nil, fmt.Errorf("region %q has no matching END — refusing to rewrite; resolve manually", open)
	}
	return present, nil
}

func renderBlendSettings(groupDir string, mix []Product, p *plan,
	prior map[string]map[string]string, out []Blended, insert bool) (*renderedSettings, error) {
	merged := map[string]any{}
	owner := map[string]int{}
	for _, i := range p.settings {
		b, err := os.ReadFile(filepath.Join(mix[i].Dir, filepath.FromSlash(settingsRel)))
		if err != nil {
			return nil, err
		}
		var s map[string]any
		if err := json.Unmarshal(b, &s); err != nil {
			return nil, fmt.Errorf("%s %s: %w", mix[i].Name, settingsRel, err)
		}
		var names []string
		for k := range s {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			if k == "mcpServers" {
				return nil, fmt.Errorf("%s %s: mcpServers reaches nothing — a product's MCP server must register as an arizuko connector in connectors.toml (spec 5/13); remove the key", mix[i].Name, settingsRel)
			}
			if prev, dup := owner[k]; dup {
				return nil, fmt.Errorf("settings.json %q: both %s and %s set it — a key collision refuses the mix", k, mix[prev].Name, mix[i].Name)
			}
			owner[k] = i
			merged[k] = s[k]
		}
	}
	recorded := map[string]string{}
	pending := map[string]string{}
	for _, hashes := range prior {
		for key, hash := range hashes {
			if name, setting := strings.CutPrefix(key, "setting:"); setting {
				recorded[name] = hash
			}
			if name, setting := strings.CutPrefix(key, PendingSettingPrefix); setting {
				pending[name] = hash
			}
		}
	}
	if len(merged) == 0 && len(recorded) == 0 && len(pending) == 0 {
		return nil, nil
	}
	path := filepath.Join(groupDir, filepath.FromSlash(settingsRel))
	cur := map[string]any{}
	previous := ""
	if b, err := os.ReadFile(path); err == nil {
		previous = hashBytes(b)
		if err := json.Unmarshal(b, &cur); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	changed := false
	for k, hash := range pending {
		value, exists := cur[k]
		b, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		if exists && hashBytes(b) == hash {
			if i, retained := owner[k]; retained {
				out[i].AssetHashes[PendingSettingPrefix+k] = hash
			} else {
				delete(cur, k)
				changed = true
			}
		}
	}
	for k, hash := range recorded {
		if _, retained := merged[k]; retained {
			continue
		}
		value, exists := cur[k]
		b, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		if exists && hashBytes(b) == hash {
			delete(cur, k)
			changed = true
		}
	}
	for k, v := range merged {
		hash := recorded[k]
		if _, taken := cur[k]; !taken && insert {
			cur[k] = v
			b, err := json.Marshal(v)
			if err != nil {
				return nil, err
			}
			out[owner[k]].AssetHashes[PendingSettingPrefix+k] = hashBytes(b)
			changed = true
		}
		if hash != "" {
			i := owner[k]
			out[i].Manifest["setting"] = append(out[i].Manifest["setting"], k)
			out[i].AssetHashes["setting:"+k] = hash
		}
	}
	for i := range out {
		sort.Strings(out[i].Manifest["setting"])
	}
	if !changed {
		return nil, nil
	}
	b, err := json.MarshalIndent(cur, "", "  ")
	if err != nil {
		return nil, err
	}
	return &renderedSettings{append(b, '\n'), previous}, nil
}

func writeBlendSettings(groupDir string, settings *renderedSettings) (bool, error) {
	b, err := os.ReadFile(filepath.Join(groupDir, filepath.FromSlash(settingsRel)))
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	current := ""
	if err == nil {
		current = hashBytes(b)
	}
	if current != settings.previous {
		return false, fmt.Errorf("incomplete package operation: %s changed after preparation; retry", settingsRel)
	}
	return writeIfDifferent(groupDir, settingsRel, settings.data)
}

// BlendProductSettings reconciles settings without changing group seeds or runbook regions.
func BlendProductSettings(groupDir string, mix []Product,
	prior map[string]map[string]string) ([]Blended, error) {
	p, err := buildPlan(mix)
	if err != nil {
		return nil, err
	}
	out := make([]Blended, len(mix))
	for i := range mix {
		out[i] = Blended{Name: mix[i].Name, Manifest: map[string][]string{}, AssetHashes: map[string]string{}}
	}
	settings, err := renderBlendSettings(groupDir, mix, p, prior, out, false)
	if err != nil {
		return nil, err
	}
	if settings != nil {
		if _, err := writeBlendSettings(groupDir, settings); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// isDirty reports whether the file at groupDir/rel was edited after the apply
// that recorded hash `recorded`. A never-recorded or absent file is not dirty.
func isDirty(groupDir, rel, recorded string) bool {
	if recorded == "" {
		return false
	}
	b, err := os.ReadFile(filepath.Join(groupDir, filepath.FromSlash(rel)))
	if err != nil {
		return false
	}
	return hashBytes(b) != recorded
}

// seedOnce writes b at groupDir/rel only when nothing is there yet.
func seedOnce(groupDir, rel string, b []byte) (bool, error) {
	path := filepath.Join(groupDir, filepath.FromSlash(rel))
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	return true, os.WriteFile(path, b, 0o644)
}

// writeIfDifferent writes b at groupDir/rel unless those bytes are already
// there, so re-applying an unchanged mix touches nothing.
func writeIfDifferent(groupDir, rel string, b []byte) (bool, error) {
	path := filepath.Join(groupDir, filepath.FromSlash(rel))
	if cur, err := os.ReadFile(path); err == nil && hashBytes(cur) == hashBytes(b) {
		return false, nil
	} else if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	return true, os.WriteFile(path, b, 0o644)
}

// dropStale removes the files a product owned at its last apply that NO product
// in the current mix ships — `shipped` is the mix's union, so a path another
// provider took over is still shipped and is left alone. A file whose bytes
// moved since then is the operator's: it is kept and reported, because deleting
// it loses exactly as much as overwriting it would.
func dropStale(groupDir string, recorded, shipped map[string]string) (bool, []string, error) {
	var stale []string
	for k := range recorded {
		rel, isFile := strings.CutPrefix(k, "file:")
		if !isFile || rel == ManifestFile {
			continue
		}
		if _, still := shipped[k]; !still {
			stale = append(stale, rel)
		}
	}
	sort.Strings(stale)
	var warns []string
	dropped := false
	for _, rel := range stale {
		path := filepath.Join(groupDir, filepath.FromSlash(rel))
		b, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return false, nil, err
		}
		if hashBytes(b) != recorded["file:"+rel] {
			warns = append(warns, fmt.Sprintf("%s is no longer shipped but was edited locally — left in place", rel))
			continue
		}
		if err := os.Remove(path); err != nil {
			return false, nil, err
		}
		dropped = true
	}
	return dropped, warns, nil
}

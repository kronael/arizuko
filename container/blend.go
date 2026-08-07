package container

// blend.go is spec 5/28's composition layer: a group blends an ORDERED MIX of
// products. Two providers share no merge base, so blending is per PAYLOAD KIND
// and never a content merge — each kind gets exactly one rule from the spec's
// table, and the table's last row is a catch-all: a payload no rule names is
// copied whole, first-provider-wins, exactly as the single-product verbatim seed
// copies it (container.SetupGroup, locked by product_seed_test.go).
//
// Seed vs managed is per KIND, not per product. Identity and knowledge (persona,
// facts, CLAUDE.md regions) seed once and become the group's own state; skills
// and mcpServers stay upstream-managed and are re-applied — but never over a
// locally edited asset, which is reported dirty and skipped.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Product is one entry of a group's ordered mix. Name is PRODUCT.md's `name` —
// the installed-package record key and the CLAUDE.md region id; Dir is the
// resolved local tree (a local path or a fetched clone).
type Product struct {
	Name string
	Dir  string
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

// skillsRel is where a product's bundled skills land: the group's own skill dir,
// which seedSkills layers stock skills into afterwards (cpDirFresh only writes
// missing files, so a product's skill survives a same-named stock one).
const skillsRel = ".claude/skills"

// payload is the blend rule one file falls under (spec 5/28's table).
type payload int

const (
	payVerbatim   payload = iota // anything else — verbatim copy, FIRST wins
	paySkill                     // skills/<name>/ — union by name, LAST wins wholesale
	payPersona                   // PERSONA.md / SOUL.md — FIRST wins, later warned
	payClaude                    // CLAUDE.md — marked sections, in mix order
	payUnion                     // facts/, tasks.toml, migrations/ — union, collision refuses
	paySettings                  // .claude/settings.json — mcpServers map union
	payDockerfile                // Dockerfile.ant — at most one in the mix
)

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
	case rel == "tasks.toml",
		parts[0] == "facts" && len(parts) > 1,
		parts[0] == "migrations" && len(parts) > 1:
		return payUnion
	case rel == settingsRel:
		return paySettings
	case rel == "Dockerfile.ant":
		return payDockerfile
	}
	return payVerbatim
}

// productFiles lists a product's regular files by slash-separated relative path.
// Symlinks are skipped: copying their targets would leak arbitrary host files
// (same discipline as chanlib.CopyDirNoSymlinks).
func productFiles(dir string) ([]string, error) {
	var rels []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil {
			return rerr
		}
		rels = append(rels, filepath.ToSlash(rel))
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
			switch classify(rel) {
			case paySkill:
				p.skill[strings.Split(rel, "/")[1]] = i
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
			case payDockerfile:
				if p.dockerfile != -1 {
					return nil, fmt.Errorf("Dockerfile.ant: both %s and %s ship one — at most one image extension per mix",
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

// BlendProducts writes an ordered product mix into groupDir per spec 5/28's
// blend table and reports what each product owns. prior maps a product name to
// the AssetHashes its last apply recorded; a managed asset whose bytes moved
// since then is reported dirty and left alone (no blind overwrite).
func BlendProducts(groupDir string, mix []Product, prior map[string]map[string]string) ([]Blended, error) {
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

	if err := blendSkills(groupDir, mix, p, prior, out); err != nil {
		return nil, err
	}
	if err := blendSeedOnce(groupDir, mix, p, out); err != nil {
		return nil, err
	}
	if err := blendClaude(groupDir, mix, p, out); err != nil {
		return nil, err
	}
	if err := blendSettings(groupDir, mix, p, out); err != nil {
		return nil, err
	}
	// Drop what a product owned last time and no longer ships. Only when the
	// file still matches the recorded hash: an asset the operator edited is
	// theirs, and deleting it is the same data loss as overwriting it.
	for i := range mix {
		dropped, warns, err := dropStale(groupDir, prior[mix[i].Name], out[i].AssetHashes)
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
					skillsRel + strings.TrimPrefix(rel, "skills"),
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

// blendSeedOnce writes the payloads that become the group's own state at seed
// time — persona, facts/tasks/migrations, the image extension, and every
// unnamed payload the catch-all row copies verbatim. Each is written only when
// nothing is there yet; none is ever re-written.
func blendSeedOnce(groupDir string, mix []Product, p *plan, out []Blended) error {
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
		o.AssetHashes["file:"+it.dst] = hashBytes(b)
		o.Manifest[it.kind] = append(o.Manifest[it.kind], it.dst)
	}
	for i := range out {
		sort.Strings(out[i].Manifest["group_seed"])
	}
	return nil
}

// blendClaude concatenates each provider's CLAUDE.md as its own marked region,
// in mix order. A region's identity is its product name, so adding a product
// later cannot disturb an existing one; existing regions are seed-once and left
// byte-for-byte. A malformed marker refuses to rewrite the file (BUGS C6's rule,
// inherited from writeManagedEnv).
func blendClaude(groupDir string, mix []Product, p *plan, out []Blended) error {
	if len(p.claude) == 0 {
		return nil
	}
	path := filepath.Join(groupDir, "CLAUDE.md")
	prev, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	present, err := claudeRegions(string(prev))
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	body := strings.TrimRight(string(prev), "\n")
	added := false
	for _, i := range p.claude {
		name := mix[i].Name
		b, rerr := os.ReadFile(filepath.Join(mix[i].Dir, "CLAUDE.md"))
		if rerr != nil {
			return rerr
		}
		region := regionBegin(name) + "\n" + strings.TrimRight(string(b), "\n") + "\n" + regionEnd(name)
		out[i].AssetHashes["region:CLAUDE.md"] = hashBytes([]byte(region))
		out[i].Manifest["claude_region"] = []string{name}
		if present[name] {
			continue
		}
		if body != "" {
			body += "\n\n"
		}
		body += region
		added = true
		out[i].Changed = true
	}
	if !added {
		return nil
	}
	if err := os.MkdirAll(groupDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body+"\n"), 0o644)
}

func regionBegin(name string) string { return "<!-- arizuko:package:" + name + " BEGIN -->" }
func regionEnd(name string) string   { return "<!-- arizuko:package:" + name + " END -->" }

var regionRE = regexp.MustCompile(`^<!--\s*arizuko:package:(\S+)\s+(BEGIN|END)\s*-->$`)

// claudeRegions returns the product names with a well-formed region in s. Any
// malformation — a BEGIN with no END, a duplicate, an END before its BEGIN, a
// mismatched pair, or nesting — is corruption: refuse to rewrite rather than
// best-effort truncate (the fix BUGS C6 paid for in writeManagedEnv).
func claudeRegions(s string) (map[string]bool, error) {
	present := map[string]bool{}
	open := ""
	for _, line := range strings.Split(s, "\n") {
		m := regionRE.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		name, marker := m[1], m[2]
		if marker == "BEGIN" {
			if open != "" {
				return nil, fmt.Errorf("region %q begins inside region %q — resolve manually", name, open)
			}
			if present[name] {
				return nil, fmt.Errorf("duplicate region %q — resolve manually", name)
			}
			open = name
			continue
		}
		if open == "" {
			return nil, fmt.Errorf("region %q ends with no preceding BEGIN — resolve manually", name)
		}
		if open != name {
			return nil, fmt.Errorf("region %q ends inside region %q — resolve manually", name, open)
		}
		present[name] = true
		open = ""
	}
	if open != "" {
		return nil, fmt.Errorf("region %q has no matching END — refusing to rewrite; resolve manually", open)
	}
	return present, nil
}

// blendSettings applies "map union; name collision = refuse" to
// .claude/settings.json. mcpServers is unioned entry by entry; the file's other
// top-level keys are unioned the same way, because the alternative — one
// provider silently winning a key the other set — is the loss this row exists to
// prevent. Keys already in the group's own settings.json are the agent's and are
// left alone: seedSettings rewrites that file every spawn.
func blendSettings(groupDir string, mix []Product, p *plan, out []Blended) error {
	if len(p.settings) == 0 {
		return nil
	}
	merged := map[string]any{}
	servers := map[string]any{}
	owner := map[string]string{}
	for _, i := range p.settings {
		b, err := os.ReadFile(filepath.Join(mix[i].Dir, filepath.FromSlash(settingsRel)))
		if err != nil {
			return err
		}
		var s map[string]any
		if err := json.Unmarshal(b, &s); err != nil {
			return fmt.Errorf("%s %s: %w", mix[i].Name, settingsRel, err)
		}
		var names []string
		for k := range s {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			if k == "mcpServers" {
				sub, ok := s[k].(map[string]any)
				if !ok {
					return fmt.Errorf("%s %s: mcpServers is not an object", mix[i].Name, settingsRel)
				}
				var subNames []string
				for n := range sub {
					subNames = append(subNames, n)
				}
				sort.Strings(subNames)
				for _, n := range subNames {
					if prev, dup := owner["mcpServers."+n]; dup {
						return fmt.Errorf("mcpServers.%s: both %s and %s declare it — a name collision refuses the mix", n, prev, mix[i].Name)
					}
					owner["mcpServers."+n] = mix[i].Name
					servers[n] = sub[n]
					out[i].Manifest["mcp_server"] = append(out[i].Manifest["mcp_server"], n)
				}
				continue
			}
			if prev, dup := owner[k]; dup {
				return fmt.Errorf("settings.json %q: both %s and %s set it — a key collision refuses the mix", k, prev, mix[i].Name)
			}
			owner[k] = mix[i].Name
			merged[k] = s[k]
			out[i].Manifest["setting"] = append(out[i].Manifest["setting"], k)
		}
	}
	if len(servers) > 0 {
		merged["mcpServers"] = servers
	}

	path := filepath.Join(groupDir, filepath.FromSlash(settingsRel))
	cur := map[string]any{}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &cur); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	for k, v := range merged {
		if k != "mcpServers" {
			if _, taken := cur[k]; !taken {
				cur[k] = v
			}
			continue
		}
		existing, _ := cur["mcpServers"].(map[string]any)
		if existing == nil {
			existing = map[string]any{}
		}
		for n, sv := range servers {
			if _, taken := existing[n]; !taken {
				existing[n] = sv
			}
		}
		cur["mcpServers"] = existing
	}
	b, err := json.MarshalIndent(cur, "", "  ")
	if err != nil {
		return err
	}
	wrote, err := writeIfDifferent(groupDir, settingsRel, append(b, '\n'))
	if err != nil {
		return err
	}
	if wrote {
		for _, i := range p.settings {
			out[i].Changed = true
		}
	}
	return nil
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

// dropStale removes the files a product owned at its last apply and no longer
// ships. A file whose bytes moved since then is the operator's: it is kept and
// reported, because deleting it loses exactly as much as overwriting it would.
func dropStale(groupDir string, recorded, current map[string]string) (bool, []string, error) {
	var stale []string
	for k := range recorded {
		rel, isFile := strings.CutPrefix(k, "file:")
		if !isFile {
			continue
		}
		if _, still := current[k]; !still {
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

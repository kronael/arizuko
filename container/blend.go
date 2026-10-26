package container

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kronael/arizuko/chanlib"
)

type Product struct {
	Name string
	Dir  string
}

type Blended struct {
	Name        string
	Manifest    map[string][]string
	AssetHashes map[string]string
	Changed     bool
	Dirty       []string
	Warnings    []string
}

const settingsRel = ".claude/settings.json"

type payload int

const (
	payVerbatim payload = iota
	paySkill
	payPersona
	payClaude
	payUnion
	paySettings
	payDockerfile
	payMigrations
	payTasks
)

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

func CheckProductAssets(product, dir string) error {
	if _, err := ReadProductManifest(dir); err != nil {
		return err
	}
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

func SeedProduct(groupDir, dir string) error {
	if _, err := ReadProductManifest(dir); err != nil {
		return err
	}
	rels, err := productFiles(dir)
	if err != nil {
		return err
	}
	for _, rel := range rels {
		dst := filepath.Join(groupDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := chanlib.CopyFile(filepath.Join(dir, filepath.FromSlash(rel)), dst); err != nil {
			return err
		}
	}
	return nil
}

func productFiles(dir string) ([]string, error) {
	var rels []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (p == filepath.Join(dir, ".git") ||
			p == filepath.Join(dir, "skills") || p == filepath.Join(dir, "hooks") ||
			p == filepath.Join(dir, "bin") || p == filepath.Join(dir, ".codex")) {
			return fs.SkipDir
		}
		if p == filepath.Join(dir, ManifestFile) || p == filepath.Join(dir, "CLAUDE.md") {
			return nil
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

type plan struct {
	persona    int
	personaRel string
	union      map[string]int
	settings   []int
	dockerfile int
	verbatim   map[string]int
	files      map[int][]string
	warnings   []string
}

func buildPlan(mix []Product) (*plan, error) {
	p := &plan{
		persona: -1, union: map[string]int{},
		dockerfile: -1, verbatim: map[string]int{}, files: map[int][]string{},
	}
	for i, prod := range mix {
		if _, err := ReadProductManifest(prod.Dir); err != nil {
			return nil, err
		}
		rels, err := productFiles(prod.Dir)
		if err != nil {
			return nil, fmt.Errorf("read product %s: %w", prod.Name, err)
		}
		p.files[i] = rels
		for _, rel := range rels {
			kind := classify(rel)
			switch kind {
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

				p.warnings = append(p.warnings, fmt.Sprintf(
					"%s ships %s and %s ships %s — SOUL.md is the legacy PERSONA.md name, "+
						"so only %s is ever read; the other is a dead file",
					mix[p.persona].Name, p.personaRel, prod.Name, rel, p.personaRel))
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

	if err := blendSeedOnce(groupDir, mix, p, out); err != nil {
		return nil, err
	}
	if err := blendSettings(groupDir, mix, p, out); err != nil {
		return nil, err
	}

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

func blendSeedOnce(groupDir string, mix []Product, p *plan, out []Blended) error {
	type item struct {
		owner int
		src   string
		dst   string
		kind  string
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

func blendSettings(groupDir string, mix []Product, p *plan, out []Blended) error {
	if len(p.settings) == 0 {
		return nil
	}
	merged := map[string]any{}
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
			if k == "hooks" {
				continue
			}

			if k == "mcpServers" {
				return fmt.Errorf("%s %s: mcpServers reaches nothing — a product's MCP server must register as an arizuko connector in connectors.toml (spec 5/13); remove the key", mix[i].Name, settingsRel)
			}
			if prev, dup := owner[k]; dup {
				return fmt.Errorf("settings.json %q: both %s and %s set it — a key collision refuses the mix", k, prev, mix[i].Name)
			}
			owner[k] = mix[i].Name
			merged[k] = s[k]
			out[i].Manifest["setting"] = append(out[i].Manifest["setting"], k)
		}
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
		if _, taken := cur[k]; !taken {
			cur[k] = v
		}
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

func dropStale(groupDir string, recorded, shipped map[string]string) (bool, []string, error) {
	var stale []string
	for k := range recorded {
		rel, isFile := strings.CutPrefix(k, "file:")
		if !isFile || rel == ManifestFile || rel == "CLAUDE.md" {
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

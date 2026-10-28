package container

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

type Product struct {
	Name string
	Dir  string
}

type Blended struct {
	Name     string
	Manifest map[string][]string
}

const settingsRel = ".claude/settings.json"

// ManifestGroupFiles is the receipt kind that lists the group/ files a
// package wrote into a group folder.
const ManifestGroupFiles = "group_seed"

type payload int

const (
	payVerbatim payload = iota
	paySettings
	payMigrations
	payTasks
)

func classify(rel string) payload {
	parts := strings.Split(rel, "/")
	switch {
	case parts[0] == "migrations" && len(parts) > 1:
		return payMigrations
	case rel == "tasks.toml":
		return payTasks
	case rel == settingsRel:
		return paySettings
	}
	return payVerbatim
}

var refusedPayloads = map[payload]string{
	payMigrations: "nothing runs a product's migrations/ — " +
		"put the instructions in the product's CLAUDE.md or a skill, which the agent reads (container/README.md)",
	payTasks: "nothing reads a product's tasks.toml — " +
		"a scheduled task is a scheduled_tasks row, so create it with schedule_task, the dashboard " +
		"or POST /v1/tasks, or have the product's CLAUDE.md tell the agent to (container/README.md)",
}

func refusePayload(product, rel string, kind payload) error {
	return fmt.Errorf("%s: %s ships it, and %s", rel, product, refusedPayloads[kind])
}

func productFiles(dir string) ([]string, error) {
	dir = filepath.Join(dir, "group")
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, nil
	}
	var rels []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == filepath.Join(dir, ManifestFile) {
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

type plan struct {
	settings  []int
	verbatim  map[string]int
	overrides []string
}

func buildPlan(mix []Product) (*plan, error) {
	p := &plan{verbatim: map[string]int{}}
	for i, prod := range mix {
		if _, err := ReadProductManifest(prod.Dir); err != nil {
			return nil, err
		}
		rels, err := productFiles(prod.Dir)
		if err != nil {
			return nil, fmt.Errorf("read product %s: %w", prod.Name, err)
		}
		for _, rel := range rels {
			kind := classify(rel)
			switch kind {
			case paySettings:
				p.settings = append(p.settings, i)
			case payMigrations, payTasks:
				return nil, refusePayload(prod.Name, rel, kind)
			default:
				if owner, taken := p.verbatim[rel]; taken {
					p.overrides = append(p.overrides, fmt.Sprintf("%s overrides %s on group/%s", prod.Name, mix[owner].Name, rel))
				}
				p.verbatim[rel] = i
			}
		}
	}
	return p, nil
}

// UnmanagedPaths lists the existing group files the mix would write that the
// previous blend did not: managed lists what that blend wrote.
func UnmanagedPaths(groupDir string, mix []Product, managed []string) ([]string, error) {
	p, err := buildPlan(mix)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, rel := range slices.Sorted(maps.Keys(p.verbatim)) {
		if _, err := os.Lstat(filepath.Join(groupDir, filepath.FromSlash(rel))); err == nil && !slices.Contains(managed, rel) {
			paths = append(paths, rel)
		}
	}
	return paths, nil
}

// BlendProducts writes a group's package payload: every group/ file of the
// mix, rewritten to the last provider's bytes on every call, and removes a
// managed file no package ships any more. Each Blended lists the files its
// package won under ManifestGroupFiles; the strings name each path a later
// package took from an earlier one.
func BlendProducts(groupDir string, mix []Product, managed []string) ([]Blended, []string, error) {
	p, err := buildPlan(mix)
	if err != nil {
		return nil, nil, err
	}
	out := make([]Blended, len(mix))
	for i := range mix {
		out[i] = Blended{Name: mix[i].Name, Manifest: map[string][]string{}}
	}
	for _, rel := range slices.Sorted(maps.Keys(p.verbatim)) {
		i := p.verbatim[rel]
		b, err := os.ReadFile(filepath.Join(mix[i].Dir, "group", filepath.FromSlash(rel)))
		if err != nil {
			return nil, nil, err
		}
		if _, err := writeIfDifferent(groupDir, rel, b); err != nil {
			return nil, nil, err
		}
		out[i].Manifest[ManifestGroupFiles] = append(out[i].Manifest[ManifestGroupFiles], rel)
	}
	for _, rel := range managed {
		if _, shipped := p.verbatim[rel]; !shipped {
			if err := os.Remove(filepath.Join(groupDir, filepath.FromSlash(rel))); err != nil && !os.IsNotExist(err) {
				return nil, nil, err
			}
		}
	}
	return out, p.overrides, blendSettings(groupDir, mix, p, out)
}

func blendSettings(groupDir string, mix []Product, p *plan, out []Blended) error {
	if len(p.settings) == 0 {
		return nil
	}
	merged := map[string]any{}
	owner := map[string]string{}
	for _, i := range p.settings {
		b, err := os.ReadFile(filepath.Join(mix[i].Dir, "group", filepath.FromSlash(settingsRel)))
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
	_, err = writeIfDifferent(groupDir, settingsRel, append(b, '\n'))
	return err
}

func writeIfDifferent(groupDir, rel string, b []byte) (bool, error) {
	path := filepath.Join(groupDir, filepath.FromSlash(rel))
	if cur, err := os.ReadFile(path); err == nil && bytes.Equal(cur, b) {
		return false, nil
	} else if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	return true, os.WriteFile(path, b, 0o644)
}

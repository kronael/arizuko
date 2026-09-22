package routd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/kronael/arizuko/ipc"
)

// connectorConfig is one [[connector]] block — the ONE declaration shape for
// an external tool source (spec 6/19). `transport` picks the dispatch path and
// nothing else: `mcp` discovers its own catalogue and reads Headers only;
// `rest` declares its tools and may use all three credential sinks.
type connectorConfig struct {
	Name      string `toml:"name"`
	Transport string `toml:"transport"`
	URL       string `toml:"url"`
	// Secrets names the keys the broker resolves for this connector.
	Secrets []string `toml:"secrets"`
	// Headers, Query and Body carry "{secret:KEY}" placeholders, rendered per
	// call. Three sinks, one vocabulary.
	Headers map[string]string `toml:"headers"`
	Query   map[string]string `toml:"query"`
	Body    map[string]string `toml:"body"`
	// Tools is the rest transport's catalogue; an mcp connector leaves it empty.
	Tools []extToolConfig `toml:"tool"`
}

type extToolConfig struct {
	Name        string           `toml:"name"`
	Description string           `toml:"description"`
	Scope       string           `toml:"scope"`
	Method      string           `toml:"method"`
	Path        string           `toml:"path"`
	Params      []extParamConfig `toml:"param"`
}

// extParamConfig is an optional [[ext.tool.param]] entry declaring a
// body/query argument; path placeholders ({zone_id}) are schema'd
// automatically.
type extParamConfig struct {
	Name        string `toml:"name"`
	Type        string `toml:"type"`
	Required    bool   `toml:"required"`
	Description string `toml:"description"`
}

var extPathParam = regexp.MustCompile(`\{([^}]+)\}`)

// extInputSchema builds the MCP input schema for one ext tool: path
// placeholders become required strings, declared params add typed fields, and
// additionalProperties stays true so CallExtTool can forward remaining args.
// Flat by design: nested tool schemas degrade LLM tool-calling.
func extInputSchema(path string, params []extParamConfig) json.RawMessage {
	props := map[string]any{}
	var required []string
	seen := map[string]bool{}
	add := func(name string, prop map[string]any, req bool) {
		props[name] = prop
		if req && !seen[name] {
			required = append(required, name)
			seen[name] = true
		}
	}
	for _, m := range extPathParam.FindAllStringSubmatch(path, -1) {
		name := strings.TrimSuffix(m[1], "...")
		add(name, map[string]any{"type": "string"}, true)
	}
	for _, p := range params {
		typ := p.Type
		if typ == "" {
			typ = "string"
		}
		prop := map[string]any{"type": typ}
		if p.Description != "" {
			prop["description"] = p.Description
		}
		add(p.Name, prop, p.Required)
	}
	schema := map[string]any{
		"type":                 "object",
		"properties":           props,
		"additionalProperties": true,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	b, _ := json.Marshal(schema)
	return b
}

type extFile struct {
	Connectors []connectorConfig `toml:"connector"`
}

// extSource is one entry in the loader's ordered source list (spec 6/19). The
// embedded set is ONE source: it ships as a unit and its file order is an
// accident of names. The operator file is the second source and shadows it.
type extSource struct {
	name       string
	connectors []connectorConfig
}

// extSlot records where a LocalName landed and which source put it there, so
// a shadow can name both sides and a repeat inside one source is
// distinguishable from an ordered override.
type extSlot struct {
	pos    int
	source string
}

// extSources reads the loader's sources in order:
//  1. Built-in providers embedded in extproviders/*.toml
//  2. Operator [[connector]] blocks from <dir>/connectors.toml (missing file ok)
func extSources(dir string) ([]extSource, error) {
	builtin := extSource{name: "embedded extproviders"}
	entries, err := fs.ReadDir(builtinProviders, "extproviders")
	if err != nil {
		return nil, fmt.Errorf("read embedded extproviders: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, rerr := builtinProviders.ReadFile("extproviders/" + e.Name())
		if rerr != nil {
			return nil, fmt.Errorf("read embedded %s: %w", e.Name(), rerr)
		}
		var f extFile
		if perr := decodeConnectors("embedded "+e.Name(), data, &f); perr != nil {
			return nil, perr
		}
		builtin.connectors = append(builtin.connectors, f.Connectors...)
	}
	sources := []extSource{builtin}

	opPath := filepath.Join(dir, "connectors.toml")
	data, err := os.ReadFile(opPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read %s: %w", opPath, err)
	}
	if err == nil {
		var f extFile
		if perr := decodeConnectors(opPath, data, &f); perr != nil {
			return nil, perr
		}
		sources = append(sources, extSource{name: opPath, connectors: f.Connectors})
	}
	return sources, nil
}

// LoadExtProviders builds the REST tool catalogue from the ordered sources.
// LocalName is the agent's wire identity, so it is the key: a later source
// replaces an earlier one and logs the shadow at WARN; two declarations of one
// name inside a single source are refused, because no ordering can pick
// between them. Returns nil, nil when no ext blocks exist.
func LoadExtProviders(_ context.Context, dir string) ([]ipc.ExtTool, error) {
	sources, err := extSources(dir)
	if err != nil {
		return nil, err
	}
	var out []ipc.ExtTool
	byName := map[string]extSlot{}
	for _, src := range sources {
		for _, p := range src.connectors {
			transport, terr := connectorTransport(p.Name, p.Transport)
			if terr != nil {
				return nil, terr
			}
			if transport == transportMCP {
				continue // discovers its own catalogue; LoadConnectors owns it
			}
			if len(p.Tools) == 0 {
				return nil, fmt.Errorf("connector %q: transport rest declares no [[connector.tool]]", p.Name)
			}
			for _, t := range p.Tools {
				tool := ipc.ExtTool{
					LocalName:   p.Name + "_" + t.Name,
					Description: t.Description,
					Scope:       t.Scope,
					InputSchema: extInputSchema(t.Path, t.Params),
					BaseURL:     p.URL,
					Method:      t.Method,
					Path:        t.Path,
					Secrets:     p.Secrets,
					Headers:     p.Headers,
					Query:       p.Query,
					Body:        p.Body,
				}
				held, taken := byName[tool.LocalName]
				switch {
				case !taken:
					byName[tool.LocalName] = extSlot{pos: len(out), source: src.name}
					out = append(out, tool)
				case held.source == src.name:
					return nil, fmt.Errorf("ext tool %q declared twice in %s: a tool name is the agent's wire identity and no ordering picks between two in one source (spec 6/19)",
						tool.LocalName, src.name)
				default:
					slog.Warn("ext tool shadowed", "tool", tool.LocalName,
						"shadowed", held.source, "by", src.name)
					out[held.pos] = tool
					byName[tool.LocalName] = extSlot{pos: held.pos, source: src.name}
				}
			}
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	slog.Info("ext providers loaded", "tools", len(out))
	return out, nil
}

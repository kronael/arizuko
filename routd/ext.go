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

// connectorConfig is one [[connector]] block — the ONE declaration shape for an
// external tool source (spec 6/19). `transport` picks the dispatch path and
// nothing else: `mcp` discovers its own catalogue and reads Headers only;
// `rest` declares its tools and may use all three credential sinks. A field is
// ABSENT for a transport, never the same field meaning two things.
type connectorConfig struct {
	Name      string `toml:"name"`
	Transport string `toml:"transport"`
	URL       string `toml:"url"`
	// Secrets names the keys the broker resolves for this connector.
	Secrets []string `toml:"secrets"`
	// Headers, Query and Body carry "{secret:KEY}" placeholders, rendered per
	// call. Three sinks, one vocabulary — which is what replaced the
	// method/secret/secret2/header/header2/param enum and its ceiling of two.
	Headers map[string]string `toml:"headers"`
	Query   map[string]string `toml:"query"`
	Body    map[string]string `toml:"body"`
	// Tools is the rest transport's catalogue; an mcp connector discovers its
	// own and leaves this empty.
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

// extParamConfig is an optional [[ext.tool.param]] entry declaring a body/query
// argument. Path placeholders ({zone_id}) are schema'd automatically; this is
// for the extra fields a descriptor wants to surface to the agent as typed.
type extParamConfig struct {
	Name        string `toml:"name"`
	Type        string `toml:"type"`
	Required    bool   `toml:"required"`
	Description string `toml:"description"`
}

var extPathParam = regexp.MustCompile(`\{([^}]+)\}`)

// extInputSchema builds the MCP input schema for one ext tool. Path
// placeholders become required string properties (the URL can't render without
// them); declared params add typed fields. additionalProperties stays true so
// the agent may still pass body fields the descriptor doesn't enumerate — which
// is how CallExtTool forwards remaining args. Flat by design: nested tool
// schemas degrade LLM tool-calling (see reference_tool_schema_bias).
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

// LoadExtProviders loads the `rest` [[connector]] blocks from:
//  1. Built-in providers embedded in extproviders/*.toml
//  2. Operator [[connector]] blocks from <dir>/connectors.toml (missing file ok)
//
// Returns nil, nil when no ext blocks exist.
func LoadExtProviders(_ context.Context, dir string) ([]ipc.ExtTool, error) {
	var providers []connectorConfig

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
		providers = append(providers, f.Connectors...)
	}

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
		providers = append(providers, f.Connectors...)
	}

	if len(providers) == 0 {
		return nil, nil
	}

	var out []ipc.ExtTool
	for _, p := range providers {
		transport, terr := connectorTransport(p.Name, p.Transport)
		if terr != nil {
			return nil, terr
		}
		if transport == transportMCP {
			continue // discovers its own catalogue; LoadConnectors owns it
		}
		for _, t := range p.Tools {
			out = append(out, ipc.ExtTool{
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
			})
		}
		if len(p.Tools) == 0 {
			return nil, fmt.Errorf("connector %q: transport rest declares no [[connector.tool]]", p.Name)
		}
	}
	slog.Info("ext providers loaded", "tools", len(out))
	return out, nil
}

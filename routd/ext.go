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

	"github.com/BurntSushi/toml"

	"github.com/kronael/arizuko/ipc"
)

type extProviderConfig struct {
	Name  string          `toml:"name"`
	Base  string          `toml:"base"`
	Auth  extAuthConfig   `toml:"auth"`
	Tools []extToolConfig `toml:"tool"`
}

type extAuthConfig struct {
	Method  string `toml:"method"`
	Secret  string `toml:"secret"`
	Secret2 string `toml:"secret2"`
	Header  string `toml:"header"`
	Header2 string `toml:"header2"`
	Param   string `toml:"param"`
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
	Ext []extProviderConfig `toml:"ext"`
}

// LoadExtProviders loads [[ext]] blocks from:
//  1. Built-in providers embedded in extproviders/*.toml
//  2. Operator [[ext]] blocks from <dir>/connectors.toml (missing file ok)
//
// Returns nil, nil when no ext blocks exist.
func LoadExtProviders(_ context.Context, dir string) ([]ipc.ExtTool, error) {
	var providers []extProviderConfig

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
		if perr := toml.Unmarshal(data, &f); perr != nil {
			return nil, fmt.Errorf("parse embedded %s: %w", e.Name(), perr)
		}
		providers = append(providers, f.Ext...)
	}

	opPath := filepath.Join(dir, "connectors.toml")
	data, err := os.ReadFile(opPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read %s: %w", opPath, err)
	}
	if err == nil {
		var f extFile
		if perr := toml.Unmarshal(data, &f); perr != nil {
			return nil, fmt.Errorf("parse %s ext blocks: %w", opPath, perr)
		}
		providers = append(providers, f.Ext...)
	}

	if len(providers) == 0 {
		return nil, nil
	}

	var out []ipc.ExtTool
	for _, p := range providers {
		before := len(out)
		for _, t := range p.Tools {
			out = append(out, ipc.ExtTool{
				LocalName:   p.Name + "_" + t.Name,
				Description: t.Description,
				Scope:       t.Scope,
				InputSchema: extInputSchema(t.Path, t.Params),
				BaseURL:     p.Base,
				Method:      t.Method,
				Path:        t.Path,
				AuthMethod:  p.Auth.Method,
				SecretKey:   p.Auth.Secret,
				SecretKey2:  p.Auth.Secret2,
				Header:      p.Auth.Header,
				Header2:     p.Auth.Header2,
				Param:       p.Auth.Param,
			})
		}
		if len(out) == before {
			slog.Warn("ext provider has no tools", "provider", p.Name)
		}
	}
	slog.Info("ext providers loaded", "tools", len(out))
	return out, nil
}

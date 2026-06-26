package routd

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

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
	Name        string `toml:"name"`
	Description string `toml:"description"`
	Scope       string `toml:"scope"`
	Method      string `toml:"method"`
	Path        string `toml:"path"`
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

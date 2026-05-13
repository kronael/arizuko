// Package config loads optional TOML config for the crackbox daemon.
//
// Lookup order (first present wins):
//  1. explicit path passed to Load
//  2. $XDG_CONFIG_HOME/crackbox/crackbox.toml
//  3. $HOME/.crackboxrc
//  4. /etc/crackbox.toml
//  5. defaults
//
// Missing file is not an error — the daemon runs on defaults. Invalid
// TOML or an unparseable listen string IS an error: silent
// misconfiguration is worse than refusing to start.
package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Defaults match the historical env-var defaults so existing deployments
// behave identically when no config file exists.
const (
	DefaultProxyListen       = ":3128"
	DefaultAdminListen       = ":3129"
	DefaultTransparentListen = ":3127"
	DefaultDNSListen         = ":53"
	DefaultDNSUpstream       = "1.1.1.1:53"
)

// Config is the parsed-and-defaulted shape. Empty TransparentListen
// means "don't bind the transparent listener" — that is the only way
// to disable it.
type Config struct {
	Proxy  ProxySection `toml:"proxy"`
	Admin  AdminSection `toml:"admin"`
	State  StateSection `toml:"state"`
	Source string       `toml:"-"` // path the config came from (empty = defaults only)
}

type ProxySection struct {
	Listen            string `toml:"listen"`
	AdminListen       string `toml:"admin_listen"`
	TransparentListen string `toml:"transparent_listen"`
	DNSListen         string `toml:"dns_listen"`
	DNSUpstream       string `toml:"dns_upstream"`
}

type AdminSection struct {
	Secret string `toml:"secret"`
}

type StateSection struct {
	Path string `toml:"path"`
}

// Defaults returns a Config with all default listen addresses set and
// no source file. Mutates nothing.
func Defaults() Config {
	return Config{
		Proxy: ProxySection{
			Listen:            DefaultProxyListen,
			AdminListen:       DefaultAdminListen,
			TransparentListen: DefaultTransparentListen,
			DNSListen:         DefaultDNSListen,
			DNSUpstream:       DefaultDNSUpstream,
		},
	}
}

// Load reads config from the first available location. explicitPath, when
// non-empty, must exist; missing → error. For implicit lookups, missing
// is fine.
//
// Defaults fill in any key NOT defined in the file. A key defined as the
// empty string overrides — this is the only way to disable the
// transparent listener via config (`transparent_listen = ""`).
func Load(explicitPath string) (Config, error) {
	cfg := Defaults()

	path, err := resolvePath(explicitPath)
	if err != nil {
		return cfg, err
	}
	if path == "" {
		return cfg, nil
	}

	b, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("read %s: %w", path, err)
	}
	var raw Config
	md, err := toml.Decode(string(b), &raw)
	if err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	mergeWithMeta(&cfg, raw, md)
	cfg.Source = path

	if err := validate(cfg); err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// resolvePath returns the first existing config path, or "" if none.
// An explicit path that does not exist is an error; implicit paths are
// quietly skipped.
func resolvePath(explicit string) (string, error) {
	if explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			return "", fmt.Errorf("config %s: %w", explicit, err)
		}
		return explicit, nil
	}
	for _, p := range implicitPaths() {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", nil
}

func implicitPaths() []string {
	var paths []string
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		paths = append(paths, filepath.Join(xdg, "crackbox", "crackbox.toml"))
	}
	if home := os.Getenv("HOME"); home != "" {
		paths = append(paths, filepath.Join(home, ".crackboxrc"))
	}
	paths = append(paths, "/etc/crackbox.toml")
	return paths
}

// mergeWithMeta overlays raw onto dst, using TOML metadata to distinguish
// "key absent in file" (keep default) from "key explicitly empty"
// (override default with ""). BurntSushi/toml exposes this via
// md.IsDefined which checks dotted key paths.
func mergeWithMeta(dst *Config, raw Config, md toml.MetaData) {
	if md.IsDefined("proxy", "listen") {
		dst.Proxy.Listen = raw.Proxy.Listen
	}
	if md.IsDefined("proxy", "admin_listen") {
		dst.Proxy.AdminListen = raw.Proxy.AdminListen
	}
	if md.IsDefined("proxy", "transparent_listen") {
		dst.Proxy.TransparentListen = raw.Proxy.TransparentListen
	}
	if md.IsDefined("proxy", "dns_listen") {
		dst.Proxy.DNSListen = raw.Proxy.DNSListen
	}
	if md.IsDefined("proxy", "dns_upstream") {
		dst.Proxy.DNSUpstream = raw.Proxy.DNSUpstream
	}
	if md.IsDefined("admin", "secret") {
		dst.Admin.Secret = raw.Admin.Secret
	}
	if md.IsDefined("state", "path") {
		dst.State.Path = raw.State.Path
	}
}

func validate(c Config) error {
	if err := checkAddr("proxy.listen", c.Proxy.Listen); err != nil {
		return err
	}
	if err := checkAddr("proxy.admin_listen", c.Proxy.AdminListen); err != nil {
		return err
	}
	if c.Proxy.TransparentListen != "" {
		if err := checkAddr("proxy.transparent_listen", c.Proxy.TransparentListen); err != nil {
			return err
		}
	}
	if c.Proxy.DNSListen != "" {
		if err := checkAddr("proxy.dns_listen", c.Proxy.DNSListen); err != nil {
			return err
		}
		if c.Proxy.DNSUpstream == "" {
			return fmt.Errorf("proxy.dns_upstream: empty (required when dns_listen is set)")
		}
		if err := checkAddr("proxy.dns_upstream", c.Proxy.DNSUpstream); err != nil {
			return err
		}
	}
	return nil
}

func checkAddr(name, addr string) error {
	if addr == "" {
		return fmt.Errorf("%s: empty", name)
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaults(t *testing.T) {
	d := Defaults()
	if d.Proxy.Listen != ":3128" {
		t.Errorf("proxy.listen default = %q want :3128", d.Proxy.Listen)
	}
	if d.Proxy.AdminListen != ":3129" {
		t.Errorf("proxy.admin_listen default = %q want :3129", d.Proxy.AdminListen)
	}
	if d.Proxy.TransparentListen != ":3127" {
		t.Errorf("proxy.transparent_listen default = %q want :3127", d.Proxy.TransparentListen)
	}
}

func TestLoadNoConfig(t *testing.T) {
	// Isolate: no HOME / XDG so implicit lookup can't pick anything up
	// and /etc/crackbox.toml is presumed absent in CI.
	t.Setenv("HOME", filepath.Join(t.TempDir(), "no-such-home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "no-such-xdg"))

	c, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Source != "" {
		t.Errorf("expected no source, got %q", c.Source)
	}
	if c.Proxy.TransparentListen != ":3127" {
		t.Errorf("default transparent = %q", c.Proxy.TransparentListen)
	}
}

func TestLoadExplicitMissing(t *testing.T) {
	if _, err := Load("/no/such/file.toml"); err == nil {
		t.Fatal("expected error for missing explicit path")
	}
}

func TestLoadOverrides(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "crackbox.toml")
	body := `
[proxy]
listen = ":4128"
admin_listen = ":4129"
transparent_listen = ":4127"

[admin]
secret = "shhh"

[state]
path = "/var/lib/crackbox/state.json"
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Proxy.Listen != ":4128" {
		t.Errorf("listen = %q", c.Proxy.Listen)
	}
	if c.Proxy.AdminListen != ":4129" {
		t.Errorf("admin = %q", c.Proxy.AdminListen)
	}
	if c.Proxy.TransparentListen != ":4127" {
		t.Errorf("transparent = %q", c.Proxy.TransparentListen)
	}
	if c.Admin.Secret != "shhh" {
		t.Errorf("secret = %q", c.Admin.Secret)
	}
	if c.State.Path != "/var/lib/crackbox/state.json" {
		t.Errorf("state path = %q", c.State.Path)
	}
}

func TestLoadDisableTransparent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "crackbox.toml")
	body := `
[proxy]
transparent_listen = ""
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Proxy.TransparentListen != "" {
		t.Errorf("transparent_listen = %q want empty", c.Proxy.TransparentListen)
	}
	// Other defaults preserved.
	if c.Proxy.Listen != ":3128" {
		t.Errorf("proxy.listen = %q want default", c.Proxy.Listen)
	}
}

func TestLoadInvalidTOML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "crackbox.toml")
	if err := os.WriteFile(path, []byte("not = valid = toml ="), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestLoadInvalidAddr(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "crackbox.toml")
	body := `
[proxy]
listen = "not-an-address"
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected validate error for unparseable listen")
	}
}

func TestImplicitLookupHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "no-such-xdg"))

	rc := filepath.Join(home, ".crackboxrc")
	body := `
[proxy]
listen = ":9128"
`
	if err := os.WriteFile(rc, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Source != rc {
		t.Errorf("source = %q want %q", c.Source, rc)
	}
	if c.Proxy.Listen != ":9128" {
		t.Errorf("listen = %q", c.Proxy.Listen)
	}
}

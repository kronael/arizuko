// Package surrogate runs the outbound "Connect <provider>" OAuth dance
// (spec 5/15): arizuko authenticates AS the user to a third-party API and the
// resulting access+refresh token is written into the `secrets` table the broker
// reads at call time. This is DISTINCT from identity OAuth (auth/oauth.go),
// which authenticates a user TO arizuko. It reuses auth's low-level primitives
// (PostForm, WritePKCE/ConsumePKCE, SignState/VerifyState) but drives them from
// a provider registry, never from identity's per-provider login exchange.
package surrogate

import (
	"embed"
	"fmt"
	"io/fs"
	"strings"

	"github.com/BurntSushi/toml"
)

//go:embed providers/*.toml
var builtinProviders embed.FS

// Provider is one registry entry: the endpoints, scopes, and the secrets.key an
// obtained token is written to. Loaded from providers/<name>.toml.
type Provider struct {
	AuthURL       string   `toml:"auth_url"`
	TokenURL      string   `toml:"token_url"`
	RevokeURL     string   `toml:"revoke_url"`
	Scopes        []string `toml:"scopes"`
	SecretKey     string   `toml:"secret_key"`
	AllowedDomain string   `toml:"allowed_domain"`
	AccessType    string   `toml:"access_type"`
}

// loadRegistry parses every embedded providers/*.toml into name→Provider, the
// file's basename (sans .toml) as the provider name.
func loadRegistry() (map[string]Provider, error) {
	entries, err := fs.ReadDir(builtinProviders, "providers")
	if err != nil {
		return nil, err
	}
	out := make(map[string]Provider, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		raw, err := builtinProviders.ReadFile("providers/" + e.Name())
		if err != nil {
			return nil, err
		}
		var p Provider
		if err := toml.Unmarshal(raw, &p); err != nil {
			return nil, fmt.Errorf("surrogate: parse %s: %w", e.Name(), err)
		}
		name := strings.TrimSuffix(e.Name(), ".toml")
		out[name] = p
	}
	return out, nil
}

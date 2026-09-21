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
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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

// loadRegistry parses every embedded providers/*.toml, then overlays operator
// files from <dataDir>/surrogate/*.toml (a same-named operator file replaces the
// embedded default — operator owns the datadir). File basename (sans .toml) is
// the provider name. dataDir "" loads embedded only. Every parsed provider is
// validated (required fields non-empty); a malformed or incomplete file is a
// hard error naming the file (fail loud — a half-defined provider yields a
// broken OAuth dance).
//
// The second result names the built-ins whose auth_url/token_url an operator
// file CHANGED. Client creds bind by provider NAME, so such a file keeps the
// built-in's operator client_secret and points it at its own endpoint; the
// caller warns once it knows which names carry creds.
func loadRegistry(dataDir string) (map[string]Provider, []string, error) {
	out := make(map[string]Provider)
	entries, err := fs.ReadDir(builtinProviders, "providers")
	if err != nil {
		return nil, nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		raw, err := builtinProviders.ReadFile("providers/" + e.Name())
		if err != nil {
			return nil, nil, err
		}
		if err := addProvider(out, "embedded:"+e.Name(), e.Name(), raw); err != nil {
			return nil, nil, err
		}
	}
	if dataDir == "" {
		return out, nil, nil
	}
	dir := filepath.Join(dataDir, "surrogate")
	opEntries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("surrogate: read %s: %w", dir, err)
	}
	var redirected []string
	for _, e := range opEntries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil, nil, fmt.Errorf("surrogate: read %s: %w", path, rerr)
		}
		name := providerName(e.Name())
		prev, replaced := out[name]
		if err := addProvider(out, path, e.Name(), raw); err != nil {
			return nil, nil, err
		}
		if replaced && (out[name].AuthURL != prev.AuthURL || out[name].TokenURL != prev.TokenURL) {
			redirected = append(redirected, name)
		}
	}
	if err := checkSecretKeys(out); err != nil {
		return nil, nil, err
	}
	return out, redirected, nil
}

// checkSecretKeys rejects two providers that write the same secrets key. The
// dance upserts by (user_sub, secret_key) alone, so a shared key means
// connecting the second provider destroys the first one's credential with no
// error anywhere and no second row for the connections page to show. That is a
// broken dance on the same terms as a missing required field, so it fails at
// boot rather than at the first user who clicks Connect.
func checkSecretKeys(reg map[string]Provider) error {
	owner := make(map[string]string, len(reg))
	for name, p := range reg {
		if first, dup := owner[p.SecretKey]; dup {
			return fmt.Errorf("surrogate: providers %q and %q both write secret_key %q; one would clobber the other's credential",
				first, name, p.SecretKey)
		}
		owner[p.SecretKey] = name
	}
	return nil
}

// providerName is the registry key a provider file lands under. ONE derivation:
// the loader reads a name out of the map before addProvider writes it, and a
// second spelling would make that read miss the entry the write then replaces.
func providerName(filename string) string {
	return strings.TrimSuffix(filename, ".toml")
}

// addProvider parses one provider TOML, validates it, and stores it under its
// basename. src labels the origin in error messages.
func addProvider(out map[string]Provider, src, filename string, raw []byte) error {
	var p Provider
	if err := toml.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("surrogate: parse %s: %w", src, err)
	}
	if p.AuthURL == "" || p.TokenURL == "" || p.SecretKey == "" {
		return fmt.Errorf("surrogate: %s: auth_url, token_url and secret_key are required", src)
	}
	out[providerName(filename)] = p
	return nil
}

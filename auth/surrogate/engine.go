package surrogate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/kronael/arizuko/auth"
)

// ErrReconnect signals a DEFINITIVE refresh rejection (revoked/expired
// refresh_token): the credential is dead and the user must reconnect. Distinct
// from a transient error (network, 5xx) — the caller nulls the row's oauth
// columns only on ErrReconnect, never on a transient failure. Match with
// errors.Is.
var ErrReconnect = errors.New("surrogate: refresh rejected, reconnect required")

// ClientCreds is one provider's operator-owned confidential-client id+secret,
// from .env (SURROGATE_<PROVIDER>_CLIENT_ID / _CLIENT_SECRET).
type ClientCreds struct {
	ID     string
	Secret string
}

// Tokens is the provider-agnostic result of an exchange or refresh. Refresh is
// the incoming refresh_token when the provider does not rotate it. ExpiresAt is
// zero when the provider returns no expiry (non-expiring token).
type Tokens struct {
	Access    string
	Refresh   string
	ExpiresAt time.Time
	Scope     string
}

// Engine is the provider-agnostic authorization-code driver: one registry, one
// creds map, three verbs (AuthorizeURL/Exchange/Refresh). Per-provider URLs +
// scopes + field quirks live in the registry, so a new provider is a TOML file,
// not new code.
type Engine struct {
	providers map[string]Provider
	creds     map[string]ClientCreds
}

// NewEngine loads the embedded provider registry and binds the operator creds
// (keyed by provider name). Providers without creds still load — the dance just
// errors when driven (no creds to present).
func NewEngine(creds map[string]ClientCreds) (*Engine, error) {
	reg, err := loadRegistry()
	if err != nil {
		return nil, err
	}
	return &Engine{providers: reg, creds: creds}, nil
}

// NewEngineWith builds an engine from an explicit registry — tests point a
// provider's TokenURL at an httptest server without touching the embedded TOML.
func NewEngineWith(providers map[string]Provider, creds map[string]ClientCreds) *Engine {
	return &Engine{providers: providers, creds: creds}
}

// Provider returns the registered provider config, or (zero, false).
func (e *Engine) Provider(name string) (Provider, bool) {
	p, ok := e.providers[name]
	return p, ok
}

// Names returns the registered provider names, sorted — the connections page
// iterates them.
func (e *Engine) Names() []string {
	out := make([]string, 0, len(e.providers))
	for name := range e.providers {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func (e *Engine) resolve(name string) (Provider, ClientCreds, error) {
	p, ok := e.providers[name]
	if !ok {
		return Provider{}, ClientCreds{}, fmt.Errorf("surrogate: unknown provider %q", name)
	}
	c, ok := e.creds[name]
	if !ok || c.ID == "" || c.Secret == "" {
		return Provider{}, ClientCreds{}, fmt.Errorf("surrogate: no client credentials for provider %q", name)
	}
	return p, c, nil
}

// AuthorizeURL builds the provider's authorize redirect: response_type=code, the
// registry scopes, the caller-minted CSRF state, and the S256 code_challenge
// (generate it with auth.WritePKCE, which stashes the verifier). redirect is the
// absolute callback URL; it must match the provider app's registered callback.
func (e *Engine) AuthorizeURL(provider, redirect, state, challenge string) (string, error) {
	p, c, err := e.resolve(provider)
	if err != nil {
		return "", err
	}
	q := url.Values{
		"client_id":             {c.ID},
		"redirect_uri":          {redirect},
		"response_type":         {"code"},
		"scope":                 {strings.Join(p.Scopes, " ")},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	if p.AccessType != "" {
		q.Set("access_type", p.AccessType)
	}
	return p.AuthURL + "?" + q.Encode(), nil
}

type tokenResp struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

// Exchange trades an authorization code for tokens (grant_type=authorization_code
// + the PKCE verifier). redirect must equal the AuthorizeURL redirect.
func (e *Engine) Exchange(ctx context.Context, provider, code, verifier, redirect string) (Tokens, error) {
	p, c, err := e.resolve(provider)
	if err != nil {
		return Tokens{}, err
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirect},
		"client_id":     {c.ID},
		"client_secret": {c.Secret},
	}
	if verifier != "" {
		form.Set("code_verifier", verifier)
	}
	tr, err := e.postToken(ctx, p.TokenURL, form)
	if err != nil {
		return Tokens{}, err
	}
	if tr.Error != "" || tr.AccessToken == "" {
		return Tokens{}, fmt.Errorf("surrogate: exchange failed: %s", firstNonEmpty(tr.ErrorDesc, tr.Error, "empty access token"))
	}
	return toTokens(tr, ""), nil
}

// Refresh trades a refresh_token for fresh tokens (grant_type=refresh_token). A
// definitive rejection (error body / 4xx) returns ErrReconnect; a transient
// failure (network / 5xx) returns a plain error so the caller keeps the row.
// When the provider does not rotate the refresh_token, the result carries the
// incoming one.
func (e *Engine) Refresh(ctx context.Context, provider, refresh string) (Tokens, error) {
	p, c, err := e.resolve(provider)
	if err != nil {
		return Tokens{}, err
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refresh},
		"client_id":     {c.ID},
		"client_secret": {c.Secret},
	}
	resp, err := auth.PostForm(ctx, p.TokenURL, form, "application/json")
	if err != nil {
		return Tokens{}, fmt.Errorf("surrogate refresh: %w", err) // transient
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 500 {
		return Tokens{}, fmt.Errorf("surrogate refresh: %s", resp.Status) // transient
	}
	var tr tokenResp
	_ = json.Unmarshal(body, &tr)
	if tr.Error != "" || tr.AccessToken == "" {
		return Tokens{}, fmt.Errorf("%w: %s", ErrReconnect, firstNonEmpty(tr.ErrorDesc, tr.Error, resp.Status))
	}
	return toTokens(tr, refresh), nil
}

// Revoke best-effort revokes a token at the provider's revoke_url ({client_id}
// substituted). Errors are swallowed by the caller — revocation is advisory.
func (e *Engine) Revoke(ctx context.Context, provider, token string) error {
	p, c, err := e.resolve(provider)
	if err != nil {
		return err
	}
	if p.RevokeURL == "" {
		return nil
	}
	endpoint := strings.ReplaceAll(p.RevokeURL, "{client_id}", c.ID)
	form := url.Values{"access_token": {token}, "token": {token}}
	resp, err := auth.PostForm(ctx, endpoint, form, "application/json")
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (e *Engine) postToken(ctx context.Context, endpoint string, form url.Values) (tokenResp, error) {
	resp, err := auth.PostForm(ctx, endpoint, form, "application/json")
	if err != nil {
		return tokenResp{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var tr tokenResp
	if err := json.Unmarshal(body, &tr); err != nil {
		return tokenResp{}, fmt.Errorf("surrogate: decode token response: %w", err)
	}
	return tr, nil
}

func toTokens(tr tokenResp, fallbackRefresh string) Tokens {
	t := Tokens{Access: tr.AccessToken, Refresh: tr.RefreshToken, Scope: tr.Scope}
	if t.Refresh == "" {
		t.Refresh = fallbackRefresh
	}
	if tr.ExpiresIn > 0 {
		t.ExpiresAt = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	}
	return t
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

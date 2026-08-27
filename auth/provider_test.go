package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/kronael/arizuko/core"
)

// The provider exchanges decide who you are. Every one of them was untested:
// a wrong redirect_uri, a dropped PKCE verifier, or a userinfo parse that
// accepts an empty subject is an identity bug, and an identity bug in a
// multitenant router is a cross-tenant bug.

type capturedReq struct {
	method, urlStr string
	header         http.Header
	form           url.Values
}

// stubProvider swaps the package http client for one that records the request
// and replays a canned response.
func stubProvider(t *testing.T, status int, body string) *capturedReq {
	t.Helper()
	got := &capturedReq{}
	prev := httpClient
	httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		got.method, got.urlStr, got.header = r.Method, r.URL.String(), r.Header.Clone()
		if r.Body != nil {
			raw, _ := io.ReadAll(r.Body)
			got.form, _ = url.ParseQuery(string(raw))
		}
		return &http.Response{
			StatusCode: status,
			Status:     http.StatusText(status),
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     http.Header{},
		}, nil
	})}
	t.Cleanup(func() { httpClient = prev })
	return got
}

func providerCfg() *core.Config {
	return &core.Config{
		AuthBaseURL:     "https://krons.fiu.wtf/",
		GoogleClientID:  "g-id",
		GoogleSecret:    "g-secret",
		GitHubClientID:  "gh-id",
		GitHubSecret:    "gh-secret",
		DiscordClientID: "d-id",
		DiscordSecret:   "d-secret",
	}
}

// The provider compares redirect_uri against what it has registered, byte for
// byte. A trailing slash left on AUTH_BASE_URL produces "…//auth/google/callback"
// and every login fails with redirect_uri_mismatch — which is why authBaseURL
// trims and why it is asserted rather than assumed.
func TestAuthBaseURLTrimsTrailingSlashes(t *testing.T) {
	cases := map[string]string{
		"https://host":    "https://host",
		"https://host/":   "https://host",
		"https://host///": "https://host",
		"":                "",
	}
	for in, want := range cases {
		if got := authBaseURL(&core.Config{AuthBaseURL: in}); got != want {
			t.Errorf("authBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExchangeGoogleSendsTheRegisteredCallbackAndThePKCEVerifier(t *testing.T) {
	got := stubProvider(t, 200, `{"access_token":"ya29.token"}`)

	tok, err := ExchangeGoogle(context.Background(), providerCfg(), "the-code", "the-verifier")
	if err != nil {
		t.Fatalf("ExchangeGoogle: %v", err)
	}
	if tok != "ya29.token" {
		t.Errorf("token = %q", tok)
	}
	if want := "https://krons.fiu.wtf/auth/google/callback"; got.form.Get("redirect_uri") != want {
		t.Errorf("redirect_uri = %q, want %q", got.form.Get("redirect_uri"), want)
	}
	if got.form.Get("code_verifier") != "the-verifier" {
		t.Error("the PKCE verifier was not forwarded; the provider rejects the exchange")
	}
	if got.form.Get("grant_type") != "authorization_code" {
		t.Errorf("grant_type = %q", got.form.Get("grant_type"))
	}
	if got.form.Get("client_secret") != "g-secret" || got.form.Get("code") != "the-code" {
		t.Errorf("secret or code missing from the exchange: %v", got.form)
	}
}

// An empty verifier means "this flow did not use PKCE". Sending
// code_verifier= empty is not the same thing and some providers reject it.
func TestExchangeOmitsCodeVerifierWhenThereIsNone(t *testing.T) {
	for _, p := range []struct {
		name string
		call func(*core.Config) error
	}{
		{"google", func(c *core.Config) error { _, e := ExchangeGoogle(context.Background(), c, "x", ""); return e }},
		{"github", func(c *core.Config) error { _, e := ExchangeGitHub(context.Background(), c, "x", ""); return e }},
		{"discord", func(c *core.Config) error { _, e := ExchangeDiscord(context.Background(), c, "x", ""); return e }},
	} {
		t.Run(p.name, func(t *testing.T) {
			got := stubProvider(t, 200, `{"access_token":"t"}`)
			if err := p.call(providerCfg()); err != nil {
				t.Fatal(err)
			}
			if _, present := got.form["code_verifier"]; present {
				t.Error("code_verifier was sent for a flow that has no verifier")
			}
		})
	}
}

// Every exchange must fail closed. A blank access token that flows on becomes
// an unauthenticated userinfo call, and a provider error must never read as a
// successful login.
func TestExchangesFailClosedOnABlankOrErroredToken(t *testing.T) {
	exchanges := map[string]func(*core.Config) (string, error){
		"google":  func(c *core.Config) (string, error) { return ExchangeGoogle(context.Background(), c, "x", "") },
		"github":  func(c *core.Config) (string, error) { return ExchangeGitHub(context.Background(), c, "x", "") },
		"discord": func(c *core.Config) (string, error) { return ExchangeDiscord(context.Background(), c, "x", "") },
	}
	bodies := map[string]string{
		"an empty json object":  `{}`,
		"an explicit empty tok": `{"access_token":""}`,
		"a null token":          `{"access_token":null}`,
	}
	for name, call := range exchanges {
		for what, body := range bodies {
			t.Run(name+"/"+what, func(t *testing.T) {
				stubProvider(t, 200, body)
				tok, err := call(providerCfg())
				if err == nil {
					t.Errorf("accepted %s and returned %q", what, tok)
				}
				if tok != "" {
					t.Errorf("returned a token %q alongside the error", tok)
				}
			})
		}
	}
}

func TestExchangeGitHubSurfacesTheProviderError(t *testing.T) {
	stubProvider(t, 200, `{"error":"bad_verification_code"}`)
	_, err := ExchangeGitHub(context.Background(), providerCfg(), "x", "")
	if err == nil {
		t.Fatal("a GitHub error response was treated as success")
	}
	if !strings.Contains(err.Error(), "bad_verification_code") {
		t.Errorf("the operator needs the provider's reason, got %q", err)
	}
}

func TestExchangeDiscordSurfacesANon200(t *testing.T) {
	stubProvider(t, 400, `{"error":"invalid_grant"}`)
	if _, err := ExchangeDiscord(context.Background(), providerCfg(), "x", ""); err == nil {
		t.Fatal("a 400 from the discord token endpoint was treated as success")
	}
}

// The client secret belongs in the token exchange and nowhere else. A secret
// leaking into a userinfo request sends it to a second endpoint and into that
// provider's request logs.
func TestUserinfoCallsCarryOnlyTheBearer(t *testing.T) {
	for _, p := range []struct {
		name string
		body string
		call func() error
	}{
		{"google", `{"sub":"1","email_verified":true}`, func() error {
			_, _, _, _, e := FetchGoogleUser(context.Background(), "the-token")
			return e
		}},
		{"github", `{"login":"octocat"}`, func() error {
			_, _, e := FetchGitHubUser(context.Background(), "the-token")
			return e
		}},
		{"discord", `{"id":"42"}`, func() error {
			_, _, e := FetchDiscordUser(context.Background(), "the-token")
			return e
		}},
	} {
		t.Run(p.name, func(t *testing.T) {
			got := stubProvider(t, 200, p.body)
			if err := p.call(); err != nil {
				t.Fatal(err)
			}
			if got.header.Get("Authorization") != "Bearer the-token" {
				t.Errorf("Authorization = %q", got.header.Get("Authorization"))
			}
			if len(got.form) != 0 {
				t.Errorf("a userinfo GET carried a form body: %v", got.form)
			}
			for _, secret := range []string{"g-secret", "gh-secret", "d-secret"} {
				if strings.Contains(got.urlStr, secret) {
					t.Errorf("a client secret reached the userinfo URL: %s", got.urlStr)
				}
			}
		})
	}
}

// An identity with no stable subject is not an identity. Accepting one would
// key every such login onto the same empty principal.
func TestUserinfoRejectsAnIdentityWithNoSubject(t *testing.T) {
	cases := []struct {
		name, body string
		call       func() error
	}{
		{"google without a sub", `{"name":"Alice","email":"a@b.c"}`, func() error {
			_, _, _, _, e := FetchGoogleUser(context.Background(), "t")
			return e
		}},
		{"github without a login", `{"name":"Alice"}`, func() error {
			_, _, e := FetchGitHubUser(context.Background(), "t")
			return e
		}},
		{"discord without an id", `{"username":"alice"}`, func() error {
			_, _, e := FetchDiscordUser(context.Background(), "t")
			return e
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stubProvider(t, 200, c.body)
			if err := c.call(); err == nil {
				t.Error("accepted an identity with no subject")
			}
		})
	}
}

func TestUserinfoRejectsANon200(t *testing.T) {
	calls := map[string]func() error{
		"google":  func() error { _, _, _, _, e := FetchGoogleUser(context.Background(), "t"); return e },
		"github":  func() error { _, _, e := FetchGitHubUser(context.Background(), "t"); return e },
		"discord": func() error { _, _, e := FetchDiscordUser(context.Background(), "t"); return e },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			stubProvider(t, 401, `{"sub":"1","login":"x","id":"1"}`)
			if err := call(); err == nil {
				t.Error("a 401 from userinfo was treated as a successful identification")
			}
		})
	}
}

// email_verified gates whether an address may be trusted against the operator
// allowlist. Defaulting it to true would let anyone claim any address.
func TestFetchGoogleUserPassesEmailVerifiedThrough(t *testing.T) {
	for _, verified := range []bool{true, false} {
		body := `{"sub":"1","name":"Alice","email":"a@b.c","email_verified":false}`
		if verified {
			body = `{"sub":"1","name":"Alice","email":"a@b.c","email_verified":true}`
		}
		stubProvider(t, 200, body)
		sub, name, email, got, err := FetchGoogleUser(context.Background(), "t")
		if err != nil {
			t.Fatal(err)
		}
		if got != verified {
			t.Errorf("email_verified = %v, want %v", got, verified)
		}
		if sub != "1" || name != "Alice" || email != "a@b.c" {
			t.Errorf("claims mangled: %q %q %q", sub, name, email)
		}
	}

	// Absent means not verified.
	stubProvider(t, 200, `{"sub":"1","email":"a@b.c"}`)
	if _, _, _, verified, err := FetchGoogleUser(context.Background(), "t"); err != nil || verified {
		t.Errorf("a missing email_verified must read as false, got %v (err %v)", verified, err)
	}
}

// A display name is optional at both providers; the account handle is the
// fallback so no user ends up nameless in a chat.
func TestUserinfoFallsBackToTheHandleWhenThereIsNoDisplayName(t *testing.T) {
	stubProvider(t, 200, `{"login":"octocat"}`)
	login, name, err := FetchGitHubUser(context.Background(), "t")
	if err != nil || login != "octocat" || name != "octocat" {
		t.Errorf("github: got (%q,%q,%v), want octocat/octocat", login, name, err)
	}

	stubProvider(t, 200, `{"id":"42","username":"alice"}`)
	id, name, err := FetchDiscordUser(context.Background(), "t")
	if err != nil || id != "42" || name != "alice" {
		t.Errorf("discord: got (%q,%q,%v), want 42/alice", id, name, err)
	}

	// global_name wins when present.
	stubProvider(t, 200, `{"id":"42","username":"alice","global_name":"Alice A"}`)
	if _, name, _ := FetchDiscordUser(context.Background(), "t"); name != "Alice A" {
		t.Errorf("discord display name = %q, want the global_name", name)
	}
}

// ---------------------------------------------------------------------------
// ParseECPrivateKeyPEM — the on-disk signing key format.
// ---------------------------------------------------------------------------

// authd's key load and the host-admin CLI's operator-bearer mint both read
// signing_keys.priv_pem through this one parser. It must round-trip what authd
// writes and refuse everything else, or a corrupted row becomes a panic or,
// worse, a silently different key.
func TestParseECPrivateKeyPEMRoundTripsAndRefusesTheRest(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	good := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))

	back, err := ParseECPrivateKeyPEM(good)
	if err != nil {
		t.Fatalf("ParseECPrivateKeyPEM rejected what authd writes: %v", err)
	}
	if !back.Equal(key) {
		t.Fatal("the parsed key is not the key that was written")
	}

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	rsaDER, err := x509.MarshalPKCS8PrivateKey(rsaKey)
	if err != nil {
		t.Fatal(err)
	}
	rsaPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: rsaDER}))

	bad := map[string]string{
		"an empty string": "",
		"not PEM at all":  "hunter2",
		// Composed, not written out: a literal PEM header trips the
		// detect-private-key pre-commit hook.
		"a PEM header with no body": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY"})),
		"a truncated body":          good[:len(good)/2],
		"an RSA key":                rsaPEM,
	}
	for name, in := range bad {
		t.Run(name, func(t *testing.T) {
			if k, err := ParseECPrivateKeyPEM(in); err == nil {
				t.Errorf("accepted %s and returned %v", name, k)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Exported wrappers.
// ---------------------------------------------------------------------------

// Each of these is a one-line delegation another package calls. A wrapper that
// stops matching its implementation is invisible until an authorization
// decision goes the wrong way.
func TestOAuthWrappersDelegate(t *testing.T) {
	if MatchEmailAllowlist("a@b.c", "*@b.c") != matchEmailAllowlist("a@b.c", "*@b.c") {
		t.Error("MatchEmailAllowlist does not match matchEmailAllowlist")
	}
	if got := AuthBaseURL(&core.Config{AuthBaseURL: "https://h/"}); got != "https://h" {
		t.Errorf("AuthBaseURL = %q", got)
	}

	stubProvider(t, 204, "")
	if !CheckGitHubOrgMember(context.Background(), "t", "acme", "octocat") {
		t.Error("CheckGitHubOrgMember does not match checkGitHubOrgMember")
	}

	for _, want := range []bool{true, false} {
		parent, scope := []string{"tasks:*"}, "tasks:read"
		if !want {
			scope = "secrets:read"
		}
		if got := HasScopeCoveredBy(parent, scope); got != want {
			t.Errorf("HasScopeCoveredBy(%v, %q) = %v, want %v", parent, scope, got, want)
		}
		if HasScopeCoveredBy(parent, scope) != scopeCoveredBy(parent, scope) {
			t.Error("HasScopeCoveredBy does not match scopeCoveredBy")
		}
	}
}

// PostForm is the exported primitive auth/surrogate uses for its token,
// refresh and revoke calls. Its one behaviour beyond the private postForm is
// the Accept header: surrogate passes "application/json" so GitHub answers
// JSON instead of a urlencoded body, and "" must leave the header unset.
func TestPostFormSetsAcceptOnlyWhenAsked(t *testing.T) {
	for _, accept := range []string{"application/json", ""} {
		got := stubProvider(t, 200, `{}`)
		resp, err := PostForm(context.Background(), "https://provider/token",
			url.Values{"grant_type": {"refresh_token"}}, accept)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()

		if got.header.Get("Accept") != accept {
			t.Errorf("Accept = %q, want %q", got.header.Get("Accept"), accept)
		}
		if ct := got.header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
			t.Errorf("Content-Type = %q; a form POST without it is rejected by every provider", ct)
		}
		if got.form.Get("grant_type") != "refresh_token" {
			t.Errorf("body not form-encoded: %v", got.form)
		}
	}
}

func TestConsumePKCEWrapperDelegates(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "https://host/auth/callback", nil)
	r.AddCookie(&http.Cookie{Name: "oauth_pkce", Value: "v"})
	if got := ConsumePKCE(w, r, true); got != "v" {
		t.Errorf("ConsumePKCE = %q, want the stored verifier", got)
	}
	if c := w.Result().Cookies()[0]; c.MaxAge >= 0 {
		t.Error("the wrapper must clear the verifier like consumePKCE does")
	}
}

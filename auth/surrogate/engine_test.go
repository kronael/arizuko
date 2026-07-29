package surrogate

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testEngine(tokenURL string) *Engine {
	return NewEngineWith(
		map[string]Provider{"github": {
			AuthURL:   "https://github.test/login/oauth/authorize",
			TokenURL:  tokenURL,
			SecretKey: "GITHUB_TOKEN",
			Scopes:    []string{"repo", "read:user"},
		}},
		map[string]ClientCreds{"github": {ID: "cid", Secret: "csecret"}},
	)
}

func TestAuthorizeURL(t *testing.T) {
	e := testEngine("")
	u, err := e.AuthorizeURL("github", "https://dash.test/cb", "state123", "chal456")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	q := parsed.Query()
	for k, want := range map[string]string{
		"client_id":             "cid",
		"redirect_uri":          "https://dash.test/cb",
		"response_type":         "code",
		"scope":                 "repo read:user",
		"state":                 "state123",
		"code_challenge":        "chal456",
		"code_challenge_method": "S256",
	} {
		if got := q.Get(k); got != want {
			t.Errorf("authorize %s = %q, want %q", k, got, want)
		}
	}
	if !strings.HasPrefix(u, "https://github.test/login/oauth/authorize?") {
		t.Errorf("authorize URL base wrong: %q", u)
	}
}

func TestAuthorizeURL_NoCreds(t *testing.T) {
	e := NewEngineWith(map[string]Provider{"github": {AuthURL: "x"}}, nil)
	if _, err := e.AuthorizeURL("github", "cb", "s", "c"); err == nil {
		t.Fatal("want error when creds missing")
	}
}

func TestExchange(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "authorization_code" {
			t.Errorf("grant_type = %q", r.Form.Get("grant_type"))
		}
		if r.Form.Get("code_verifier") != "verif" {
			t.Errorf("code_verifier = %q", r.Form.Get("code_verifier"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"at","refresh_token":"rt","expires_in":3600,"scope":"repo"}`))
	}))
	defer srv.Close()

	tok, err := testEngine(srv.URL).Exchange(context.Background(), "github", "code1", "verif", "cb")
	if err != nil {
		t.Fatal(err)
	}
	if tok.Access != "at" || tok.Refresh != "rt" || tok.Scope != "repo" {
		t.Errorf("tokens = %+v", tok)
	}
	if tok.ExpiresAt.IsZero() {
		t.Error("expires_at should be set from expires_in")
	}
}

func TestRefresh_RotatesAndKeeps(t *testing.T) {
	// Provider omits refresh_token → keep the incoming one.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "refresh_token" {
			t.Errorf("grant_type = %q", r.Form.Get("grant_type"))
		}
		_, _ = w.Write([]byte(`{"access_token":"fresh","expires_in":3600}`))
	}))
	defer srv.Close()

	tok, err := testEngine(srv.URL).Refresh(context.Background(), "github", "old-refresh")
	if err != nil {
		t.Fatal(err)
	}
	if tok.Access != "fresh" {
		t.Errorf("access = %q, want fresh", tok.Access)
	}
	if tok.Refresh != "old-refresh" {
		t.Errorf("refresh = %q, want old-refresh (not rotated → kept)", tok.Refresh)
	}
}

func TestRefresh_RevokedIsReconnect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad_refresh_token","error_description":"expired"}`))
	}))
	defer srv.Close()

	_, err := testEngine(srv.URL).Refresh(context.Background(), "github", "revoked")
	if !errors.Is(err, ErrReconnect) {
		t.Fatalf("err = %v, want ErrReconnect", err)
	}
}

func TestRefresh_TransientIsNotReconnect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := testEngine(srv.URL).Refresh(context.Background(), "github", "r")
	if err == nil {
		t.Fatal("want error on 5xx")
	}
	if errors.Is(err, ErrReconnect) {
		t.Error("5xx must be transient, not ErrReconnect (would null a live credential)")
	}
}

// TestRefresh_BodylessErrorIsNotReconnect: a 4xx with NO parseable OAuth error
// body (transient 429 / gateway hiccup) must be transient, not ErrReconnect —
// nulling a still-valid refresh token over a hiccup forces a needless reconnect.
func TestRefresh_BodylessErrorIsNotReconnect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests) // 429, empty body
	}))
	defer srv.Close()

	_, err := testEngine(srv.URL).Refresh(context.Background(), "github", "r")
	if err == nil {
		t.Fatal("want error on bodyless 4xx")
	}
	if errors.Is(err, ErrReconnect) {
		t.Error("bodyless 4xx must be transient, not ErrReconnect (would null a live credential)")
	}
}

func TestRegistry_EmbeddedGitHub(t *testing.T) {
	e, err := NewEngine("")
	if err != nil {
		t.Fatal(err)
	}
	p, ok := e.Provider("github")
	if !ok {
		t.Fatal("embedded github provider not loaded")
	}
	if p.SecretKey != "GITHUB_TOKEN" || p.TokenURL == "" || len(p.Scopes) == 0 {
		t.Errorf("github registry entry incomplete: %+v", p)
	}
	if names := e.Names(); len(names) == 0 || names[0] != "github" {
		t.Errorf("Names() = %v", names)
	}
}

// A datadir provider adds a new one AND overrides an embedded default by name;
// env creds bind per provider.
func TestRegistry_DatadirOverrideAndCreds(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "surrogate"), 0o755); err != nil {
		t.Fatal(err)
	}
	// New operator provider.
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, "surrogate", name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("acme.toml", `auth_url="https://acme.test/auth"
token_url="https://acme.test/token"
secret_key="ACME_TOKEN"
scopes=["read"]`)
	// Override embedded github's secret_key.
	write("github.toml", `auth_url="https://github.test/auth"
token_url="https://github.test/token"
secret_key="GH_OVERRIDDEN"`)

	t.Setenv("SURROGATE_ACME_CLIENT_ID", "acme-id")
	t.Setenv("SURROGATE_ACME_CLIENT_SECRET", "acme-secret")

	e, err := NewEngine(dir)
	if err != nil {
		t.Fatal(err)
	}
	acme, ok := e.Provider("acme")
	if !ok || acme.SecretKey != "ACME_TOKEN" {
		t.Fatalf("acme not loaded from datadir: %+v ok=%v", acme, ok)
	}
	gh, _ := e.Provider("github")
	if gh.SecretKey != "GH_OVERRIDDEN" {
		t.Errorf("datadir github.toml did not override embedded: %+v", gh)
	}
	if !e.HasCreds("acme") {
		t.Error("acme creds not bound from env")
	}
	if e.HasCreds("github") {
		t.Error("github should have no creds (no env set)")
	}
}

// A malformed / incomplete provider file fails loud, naming the file.
func TestRegistry_IncompleteProviderErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "surrogate"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Missing token_url + secret_key.
	if err := os.WriteFile(filepath.Join(dir, "surrogate", "bad.toml"),
		[]byte(`auth_url="https://bad.test/auth"`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := NewEngine(dir)
	if err == nil {
		t.Fatal("want error on incomplete provider")
	}
	if !strings.Contains(err.Error(), "bad.toml") {
		t.Errorf("error must name the file, got: %v", err)
	}
}

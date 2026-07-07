package surrogate

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	e, err := NewEngine(nil)
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

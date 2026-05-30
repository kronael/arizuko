package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	_ "modernc.org/sqlite"
)

func newOAuth(t *testing.T, a *Authd, cfg *core.Config, g GrantsFetcher) *oauth {
	t.Helper()
	if cfg == nil {
		cfg = &core.Config{AuthSecret: "csrf-key", AuthBaseURL: "https://auth.example"}
	}
	return &oauth{a: a, cfg: cfg, state: []byte(cfg.AuthSecret), grants: g}
}

// A login (here: dispatch of a resolved provider identity) creates the
// canonical authd user, links the provider, and issues an ES256 access token
// that VerifyToken accepts — the core spec assertion for the OAuth move
// (issuance switched HS256→ES256, store switched to authd's tables).
func TestOAuthDispatchMintsES256AndCreatesUser(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	o := newOAuth(t, a, nil, nil) // no grants fetcher → empty-scope session

	req := httptest.NewRequest("GET", "/auth/google/callback", nil)
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	o.dispatch(rec, req, "google", "g-123", "Alice", auth.StateIntent{})

	if rec.Code != http.StatusOK {
		t.Fatalf("dispatch status %d: %s", rec.Code, rec.Body.String())
	}
	// Canonical user created + provider linked in authd's own tables.
	if !userExists(db, "google:g-123") {
		t.Fatal("canonical user not created in auth_users")
	}
	// The JSON response carries an ES256 access token that verifies.
	tok := jsonField(t, rec.Body.Bytes(), "token")
	sub, err := auth.VerifyToken(tok, a.LocalKeySet())
	if err != nil {
		t.Fatalf("OAuth-minted access token must verify: %v", err)
	}
	if sub.Sub != "user:google:g-123" {
		t.Fatalf("minted sub = %q want user:google:g-123", sub.Sub)
	}
	// The initial refresh token rotates through authd's own store.
	refresh := jsonField(t, rec.Body.Bytes(), "refresh_token")
	if refresh == "" {
		t.Fatal("JSON login must return an initial refresh token")
	}
	if _, newR, err := a.Refresh(refresh); err != nil || newR == refresh {
		t.Fatalf("OAuth-issued refresh must rotate: newR=%q err=%v", newR, err)
	}
}

// Browser login (Accept: text/html) delivers the access token via the
// localStorage bootstrap + an HttpOnly refresh cookie, not a JSON body.
func TestOAuthBrowserDeliveryUsesCookieAndBootstrap(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	o := newOAuth(t, a, nil, nil)

	req := httptest.NewRequest("GET", "/auth/github/callback", nil) // no JSON Accept
	rec := httptest.NewRecorder()
	o.dispatch(rec, req, "github", "gh-7", "Bob", auth.StateIntent{})

	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("browser login must return HTML, got %q", ct)
	}
	if !strings.Contains(rec.Body.String(), "localStorage.setItem('jwt'") {
		t.Fatal("browser login must bootstrap the jwt into localStorage")
	}
	var refreshCookie bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == "refresh_token" {
			refreshCookie = true
			if !c.HttpOnly {
				t.Fatal("refresh cookie must be HttpOnly")
			}
		}
	}
	if !refreshCookie {
		t.Fatal("browser login must set the refresh_token cookie")
	}
	// Empty-scope session (no grants fetcher) routes a browser to /onboard.
	if !strings.Contains(rec.Body.String(), "/onboard") {
		t.Fatal("empty-scope session must send the browser to /onboard")
	}
}

// /auth/me returns the verified caller identity from the bearer.
func TestOAuthMeReturnsIdentity(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	o := newOAuth(t, a, nil, nil)

	tok := callerToken(t, a, "user:42", []string{"tasks:read"}, "atlas/main")
	req := httptest.NewRequest("GET", "/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	o.me(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("me status %d", rec.Code)
	}
	if jsonField(t, rec.Body.Bytes(), "sub") != "user:42" {
		t.Fatalf("me sub wrong: %s", rec.Body.String())
	}
	if jsonField(t, rec.Body.Bytes(), "folder") != "atlas/main" {
		t.Fatalf("me folder wrong: %s", rec.Body.String())
	}
	// No bearer → 401.
	rec2 := httptest.NewRecorder()
	o.me(rec2, httptest.NewRequest("GET", "/auth/me", nil))
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("me without bearer must be 401, got %d", rec2.Code)
	}
}

// When a grants fetcher is wired, the OAuth session stamps the target's scope +
// folder into the access JWT.
func TestOAuthSessionSnapshotsGrants(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	o := newOAuth(t, a, nil, fakeGrants{
		"google:g-9": {Scope: []string{"tasks:read"}, Folder: "atlas/main"},
	})

	req := httptest.NewRequest("GET", "/auth/google/callback", nil)
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	o.dispatch(rec, req, "google", "g-9", "Carol", auth.StateIntent{})
	if rec.Code != http.StatusOK {
		t.Fatalf("dispatch status %d", rec.Code)
	}
	sub, err := auth.VerifyToken(jsonField(t, rec.Body.Bytes(), "token"), a.LocalKeySet())
	if err != nil {
		t.Fatal(err)
	}
	if !auth.HasScope(sub.Scope, "tasks", "read") {
		t.Fatalf("session scope must snapshot grants, got %v", sub.Scope)
	}
	if sub.Extra["arz/folder"] != "atlas/main" {
		t.Fatalf("session folder must snapshot grants, got %v", sub.Extra)
	}
}

// A grants backend that is DOWN (not "no grants") fails the login closed: no
// token is minted with a masked empty scope (spec 5/1 § Login-time scope
// snapshot, "fail closed" on 5xx).
func TestOAuthSessionGrantsDownFailsClosed(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	o := newOAuth(t, a, nil, downGrants{})

	req := httptest.NewRequest("GET", "/auth/google/callback", nil)
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	o.dispatch(rec, req, "google", "g-down", "Dan", auth.StateIntent{})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("grants-down login must fail closed (503), got %d", rec.Code)
	}
}

// The CSRF state cookie round-trips: redirect writes a signed state + PKCE
// challenge cookie that the callback prologue accepts.
func TestOAuthRedirectStateRoundTrips(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	cfg := &core.Config{AuthSecret: "csrf-key", AuthBaseURL: "https://auth.example", GoogleClientID: "gid"}
	o := newOAuth(t, a, cfg, nil)

	rec := httptest.NewRecorder()
	o.redirect("google")(rec, httptest.NewRequest("GET", "/auth/google", nil))
	if rec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("redirect status %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "accounts.google.com") || !strings.Contains(loc, "code_challenge_method=S256") {
		t.Fatalf("redirect URL missing provider/PKCE: %s", loc)
	}
	// Re-present the state cookie + state param to the callback prologue.
	state := cookieValue(rec.Result().Cookies(), "oauth_state")
	cb := httptest.NewRequest("GET", "/auth/google/callback?code=abc&state="+state, nil)
	for _, c := range rec.Result().Cookies() {
		cb.AddCookie(c)
	}
	_, _, _, ok := o.callbackCode(httptest.NewRecorder(), cb)
	if !ok {
		t.Fatal("callback must accept the state written by redirect")
	}
	// A tampered state is rejected.
	bad := httptest.NewRequest("GET", "/auth/google/callback?code=abc&state=forged", nil)
	bad.AddCookie(&http.Cookie{Name: "oauth_state", Value: "forged"})
	if _, _, _, ok := o.callbackCode(httptest.NewRecorder(), bad); ok {
		t.Fatal("forged state must be rejected")
	}
}

// downGrants always reports the backend is unavailable (a non-ErrNoGrants error).
type downGrants struct{}

func (downGrants) FetchGrants(context.Context, string) (GrantsSnapshot, error) {
	return GrantsSnapshot{}, errGrantsUnavailable
}

func cookieValue(cs []*http.Cookie, name string) string {
	for _, c := range cs {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}

func jsonField(t *testing.T, body []byte, key string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("decode json: %v (body=%s)", err, body)
	}
	s, _ := m[key].(string)
	return s
}

package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
)

const telegramToken = "123456:bot-token"

type providerStub struct {
	mu    sync.Mutex
	calls []string
	reply map[string][2]string
}

func (p *providerStub) seen() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.calls...)
}

// stubProviders answers the four provider hosts from reply (keyed "host/path",
// an unlisted path answers 500). auth's http client sets no transport, so the
// swap of http.DefaultTransport is the seam; other hosts pass through.
func stubProviders(t *testing.T, reply map[string][2]string) *providerStub {
	t.Helper()
	p := &providerStub{reply: reply}
	prev := http.DefaultTransport
	hosts := map[string]bool{
		"oauth2.googleapis.com": true, "www.googleapis.com": true,
		"github.com": true, "api.github.com": true,
	}
	http.DefaultTransport = roundTrip(func(r *http.Request) (*http.Response, error) {
		if !hosts[r.URL.Host] {
			return prev.RoundTrip(r)
		}
		key := r.URL.Host + r.URL.Path
		p.mu.Lock()
		p.calls = append(p.calls, r.Method+" "+key)
		p.mu.Unlock()
		status, body := 500, "unexpected provider call"
		if v, ok := p.reply[key]; ok {
			status, _ = strconv.Atoi(v[0])
			body = v[1]
		}
		return &http.Response{
			StatusCode: status, Status: http.StatusText(status),
			Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{},
		}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = prev })
	return p
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func loginMux(t *testing.T, cfg *core.Config) (*http.ServeMux, *Authd, func(string) bool) {
	t.Helper()
	db := testDB(t)
	a := newTestAuthd(t, db)
	mux := http.NewServeMux()
	(&server{a: a}).registerOAuth(mux, cfg)
	return mux, a, func(id string) bool { return userExists(t, db, id) }
}

func loginCfg() *core.Config {
	return &core.Config{AuthSecret: "csrf-key", AuthBaseURL: "https://auth.example"}
}

func signedWidget(token string, authDate time.Time, fields map[string]string) url.Values {
	form := url.Values{"auth_date": {strconv.FormatInt(authDate.Unix(), 10)}}
	for k, v := range fields {
		form.Set(k, v)
	}
	keys := make([]string, 0, len(form))
	for k := range form {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+form.Get(k))
	}
	secret := sha256.Sum256([]byte(token))
	mac := hmac.New(sha256.New, secret[:])
	mac.Write([]byte(strings.Join(parts, "\n")))
	form.Set("hash", hex.EncodeToString(mac.Sum(nil)))
	return form
}

func postWidget(mux http.Handler, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/auth/telegram", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// A correctly signed widget posted to /auth/telegram -> 200 with an ES256
// token whose sub is user:telegram:<id>, and the canonical user exists.
func TestTelegramLoginSignedWidgetSignsIn(t *testing.T) {
	cfg := loginCfg()
	cfg.TelegramToken = telegramToken
	mux, a, exists := loginMux(t, cfg)

	form := signedWidget(telegramToken, time.Now(),
		map[string]string{"id": "777", "first_name": "Ada", "last_name": "Lovelace"})
	rec := postWidget(mux, form)

	if rec.Code != http.StatusOK {
		t.Fatalf("signed widget status %d: %s", rec.Code, rec.Body.String())
	}
	sub, err := auth.VerifyToken(jsonField(t, rec.Body.Bytes(), "token"), a.LocalKeySet())
	if err != nil {
		t.Fatalf("minted token must verify: %v", err)
	}
	if sub.Sub != "user:telegram:777" {
		t.Fatalf("sub = %q want user:telegram:777", sub.Sub)
	}
	if !exists("telegram:777") {
		t.Fatal("user telegram:777 not created")
	}
}

// A widget the handler must refuse -> 403, no token, no user created: a hash
// signed with another bot token, a field changed after signing, an auth_date
// older than five minutes, and a missing hash.
func TestTelegramLoginRefusesForgedTamperedAndStaleWidgets(t *testing.T) {
	cfg := loginCfg()
	cfg.TelegramToken = telegramToken
	mux, _, exists := loginMux(t, cfg)

	tampered := signedWidget(telegramToken, time.Now(), map[string]string{"id": "1", "first_name": "Eve"})
	tampered.Set("id", "2")

	cases := map[string]url.Values{
		"signed with another bot token": signedWidget("999:other-token", time.Now(), map[string]string{"id": "1", "first_name": "Eve"}),
		"field changed after signing":   tampered,
		"stale auth_date":               signedWidget(telegramToken, time.Now().Add(-time.Hour), map[string]string{"id": "1", "first_name": "Eve"}),
		"no hash":                       {"id": {"1"}, "first_name": {"Eve"}, "auth_date": {strconv.FormatInt(time.Now().Unix(), 10)}},
	}
	for name, form := range cases {
		rec := postWidget(mux, form)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: status %d want 403: %s", name, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "token") {
			t.Errorf("%s: body carries a token: %s", name, rec.Body.String())
		}
	}
	for _, id := range []string{"telegram:1", "telegram:2"} {
		if exists(id) {
			t.Errorf("refused widget created user %s", id)
		}
	}
}

// TELEGRAM_TOKEN unset -> the route is not mounted: POST /auth/telegram is 404,
// so a widget cannot sign in against an instance that never enabled it.
func TestTelegramLoginRouteAbsentWithoutToken(t *testing.T) {
	mux, _, exists := loginMux(t, loginCfg())
	form := signedWidget("", time.Now(), map[string]string{"id": "5", "first_name": "Zed"})
	rec := postWidget(mux, form)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d want 404 with no TELEGRAM_TOKEN", rec.Code)
	}
	if exists("telegram:5") {
		t.Fatal("user created through an unmounted route")
	}
}

func callback(mux http.Handler, provider string, cfg *core.Config) *httptest.ResponseRecorder {
	state := auth.SignState([]byte(cfg.AuthSecret), auth.StateIntent{})
	req := httptest.NewRequest("GET", "/auth/"+provider+"/callback?code=c0de&state="+url.QueryEscape(state), nil)
	req.AddCookie(&http.Cookie{Name: "oauth_state", Value: state})
	req.AddCookie(&http.Cookie{Name: "oauth_pkce", Value: "verifier"})
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func googleReplies(email string, verified bool) map[string][2]string {
	return map[string][2]string{
		"oauth2.googleapis.com/token": {"200", `{"access_token":"g-tok"}`},
		"www.googleapis.com/oauth2/v3/userinfo": {"200", fmt.Sprintf(
			`{"sub":"g-42","name":"Gee","email":%q,"email_verified":%t}`, email, verified)},
	}
}

// GoogleAllowedEmails set: a verified address matching a pattern -> 200 and the
// user is created; an address outside the list, and a matching but unverified
// one -> 307 to /auth/login?error=unauthorized with no user. Unset -> any
// verified Google account signs in.
func TestGoogleCallbackAppliesAllowedEmails(t *testing.T) {
	cases := []struct {
		name     string
		allowed  string
		email    string
		verified bool
		admit    bool
	}{
		{"verified match on a glob", "ops@x.org, *@acme.io", "ann@acme.io", true, true},
		{"verified exact match", "ann@acme.io", "ann@acme.io", true, true},
		{"address outside the list", "*@acme.io", "ann@evil.io", true, false},
		{"match but unverified", "*@acme.io", "ann@acme.io", false, false},
		{"no allowlist admits any verified account", "", "ann@evil.io", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := loginCfg()
			cfg.GoogleClientID, cfg.GoogleSecret = "gid", "gsecret"
			cfg.GoogleAllowedEmails = tc.allowed
			mux, a, exists := loginMux(t, cfg)
			stubProviders(t, googleReplies(tc.email, tc.verified))

			rec := callback(mux, "google", cfg)

			if tc.admit {
				if rec.Code != http.StatusOK {
					t.Fatalf("status %d want 200: %s", rec.Code, rec.Body.String())
				}
				sub, err := auth.VerifyToken(jsonField(t, rec.Body.Bytes(), "token"), a.LocalKeySet())
				if err != nil || sub.Sub != "user:google:g-42" {
					t.Fatalf("token sub %v err %v want user:google:g-42", sub, err)
				}
				if !exists("google:g-42") {
					t.Fatal("admitted user not created")
				}
				return
			}
			if rec.Code != http.StatusTemporaryRedirect || rec.Header().Get("Location") != "/auth/login?error=unauthorized" {
				t.Fatalf("status %d Location %q want 307 /auth/login?error=unauthorized",
					rec.Code, rec.Header().Get("Location"))
			}
			if strings.Contains(rec.Body.String(), "token") || exists("google:g-42") {
				t.Fatalf("refused user got a session: %s", rec.Body.String())
			}
		})
	}
}

// GitHubAllowedOrg set: the callback asks GitHub whether the login belongs to
// that org (GET api.github.com/orgs/<org>/members/<login>); 204 -> 200 and the
// user is created, 404 -> 307 to /auth/login?error=unauthorized with no user.
// Unset -> no membership call and any GitHub account signs in.
func TestGitHubCallbackAppliesAllowedOrg(t *testing.T) {
	replies := func(member string) map[string][2]string {
		return map[string][2]string{
			"github.com/login/oauth/access_token":   {"200", `{"access_token":"gh-tok"}`},
			"api.github.com/user":                   {"200", `{"login":"octo","name":"Octo Cat"}`},
			"api.github.com/orgs/acme/members/octo": {member, ""},
		}
	}
	const orgCall = "GET api.github.com/orgs/acme/members/octo"
	cases := []struct {
		name     string
		org      string
		member   string
		admit    bool
		wantOrgQ bool
	}{
		{"org member", "acme", "204", true, true},
		{"not an org member", "acme", "404", false, true},
		{"no org configured", "", "404", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := loginCfg()
			cfg.GitHubClientID, cfg.GitHubSecret = "ghid", "ghsecret"
			cfg.GitHubAllowedOrg = tc.org
			mux, a, exists := loginMux(t, cfg)
			stub := stubProviders(t, replies(tc.member))

			rec := callback(mux, "github", cfg)

			asked := false
			for _, c := range stub.seen() {
				if c == orgCall {
					asked = true
				}
			}
			if asked != tc.wantOrgQ {
				t.Fatalf("org membership asked = %v want %v (calls %v)", asked, tc.wantOrgQ, stub.seen())
			}
			if tc.admit {
				if rec.Code != http.StatusOK {
					t.Fatalf("status %d want 200: %s", rec.Code, rec.Body.String())
				}
				sub, err := auth.VerifyToken(jsonField(t, rec.Body.Bytes(), "token"), a.LocalKeySet())
				if err != nil || sub.Sub != "user:github:octo" {
					t.Fatalf("token sub %v err %v want user:github:octo", sub, err)
				}
				if !exists("github:octo") {
					t.Fatal("admitted user not created")
				}
				return
			}
			if rec.Code != http.StatusTemporaryRedirect || rec.Header().Get("Location") != "/auth/login?error=unauthorized" {
				t.Fatalf("status %d Location %q want 307 /auth/login?error=unauthorized",
					rec.Code, rec.Header().Get("Location"))
			}
			if strings.Contains(rec.Body.String(), "token") || exists("github:octo") {
				t.Fatalf("refused user got a session: %s", rec.Body.String())
			}
		})
	}
}

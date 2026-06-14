package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/store"
)

func testMintJWT(secret []byte, sub string) string {
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	c := fmt.Sprintf(`{"sub":%q,"name":"test","exp":%d,"iat":%d}`,
		sub, time.Now().Add(time.Hour).Unix(), time.Now().Unix())
	body := base64.RawURLEncoding.EncodeToString([]byte(c))
	h := hmac.New(sha256.New, secret)
	h.Write([]byte(hdr + "." + body))
	sig := base64.RawURLEncoding.EncodeToString(h.Sum(nil))
	return hdr + "." + body + "." + sig
}

func testServer() *server {
	return &server{
		cfg:         config{authSecret: ""},
		st:          nil,
		chatAnonDOS: newRateLimiter(10, time.Minute),
	}
}

func TestProxydHealth(t *testing.T) {
	s := testServer()
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	s.route(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["ok"] != true {
		t.Errorf("resp = %v", resp)
	}
}

func TestProxydRequireAuthNoSecret(t *testing.T) {
	// No auth secret = nobody can authenticate = private routes are unreachable.
	// Fail closed with 404 rather than bouncing to a login that can't work.
	s := testServer()
	called := false
	h := s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	})
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	h(w, req)
	if called {
		t.Error("handler should not be called when no auth secret is set")
	}
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestProxydRequireAuthRawSecretRejected(t *testing.T) {
	s := &server{cfg: config{authSecret: "rawsecret"}, chatAnonDOS: newRateLimiter(10, time.Minute)}
	called := false
	h := s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	})
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer rawsecret")
	w := httptest.NewRecorder()
	h(w, req)
	if called {
		t.Error("handler should not be called with raw secret (signing key must not be accepted as credential)")
	}
	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303 redirect to login", w.Code)
	}
}

func TestProxydRequireAuthValidJWT(t *testing.T) {
	secret := []byte("testsecret")
	s := &server{cfg: config{authSecret: "testsecret"}, chatAnonDOS: newRateLimiter(10, time.Minute)}
	called := false
	var gotSub string
	h := s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		called = true
		gotSub = r.Header.Get("X-User-Sub")
		w.WriteHeader(200)
	})
	tok := testMintJWT(secret, "user1")
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	h(w, req)
	if !called {
		t.Error("handler not called with valid JWT")
	}
	if gotSub != "user1" {
		t.Errorf("X-User-Sub = %q, want user1", gotSub)
	}
}

func TestProxydRequireAuthBadToken(t *testing.T) {
	s := &server{cfg: config{authSecret: "testsecret"}, chatAnonDOS: newRateLimiter(10, time.Minute)}
	h := s.requireAuth(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer badtoken")
	w := httptest.NewRecorder()
	h(w, req)
	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", w.Code)
	}
}

func TestProxydRequireAuthNoHeader(t *testing.T) {
	s := &server{cfg: config{authSecret: "testsecret"}, chatAnonDOS: newRateLimiter(10, time.Minute)}
	called := false
	h := s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	})
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	h(w, req)
	if called {
		t.Error("handler called without credentials")
	}
	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", w.Code)
	}
}

func testMintExpiredJWT(secret []byte, sub string) string {
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	c := fmt.Sprintf(`{"sub":%q,"name":"test","exp":%d,"iat":%d}`,
		sub, time.Now().Add(-time.Hour).Unix(), time.Now().Add(-2*time.Hour).Unix())
	body := base64.RawURLEncoding.EncodeToString([]byte(c))
	h := hmac.New(sha256.New, secret)
	h.Write([]byte(hdr + "." + body))
	sig := base64.RawURLEncoding.EncodeToString(h.Sum(nil))
	return hdr + "." + body + "." + sig
}

func TestProxydRequireAuthExpiredJWT(t *testing.T) {
	secret := []byte("testsecret")
	s := &server{cfg: config{authSecret: "testsecret"}, chatAnonDOS: newRateLimiter(10, time.Minute)}
	called := false
	h := s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	})
	tok := testMintExpiredJWT(secret, "user1")
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	h(w, req)
	if called {
		t.Error("handler called with expired JWT")
	}
	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", w.Code)
	}
}

func TestProxydChatRouteRateLimit(t *testing.T) {
	// A /chat/ route is needed so the request enters dispatchRouteToken
	// (where the rate limiter lives). Pre-fill the limiter bucket so the
	// request is denied before any upstream hop. Spec 5/W.
	s := &server{
		cfg:         config{},
		chatAnonDOS: newRateLimiter(1, time.Minute),
		rr:          newRoutesResource(nil, []Route{{Path: "/chat/", Backend: "http://stub", Auth: "public"}}),
	}
	s.chatAnonDOS.allow("9.9.9.9")

	req := httptest.NewRequest("GET", "/chat/tok1", nil)
	req.RemoteAddr = "9.9.9.9:1234"
	w := httptest.NewRecorder()
	s.route(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", w.Code)
	}
}

// With no /dash/ TOML route configured the request falls through to the
// public-redirect fallback. (When dashd is gated off the operator's
// PROXYD_ROUTES_JSON simply omits /dash/.)
func TestProxydDashNoRouteRedirectsToPub(t *testing.T) {
	s := testServer()
	req := httptest.NewRequest("GET", "/dash/status/", nil)
	w := httptest.NewRecorder()
	s.route(w, req)
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/pub/dash/status/" {
		t.Errorf("location = %q, want /pub/dash/status/", loc)
	}
}

func TestProxydSlinkRateLimit(t *testing.T) {
	rl := newRateLimiter(2, time.Minute)
	if !rl.allow("1.2.3.4") {
		t.Error("first request should be allowed")
	}
	if !rl.allow("1.2.3.4") {
		t.Error("second request should be allowed")
	}
	if rl.allow("1.2.3.4") {
		t.Error("third request should be denied")
	}
}

func TestRateLimiterDifferentKeys(t *testing.T) {
	rl := newRateLimiter(1, time.Minute)
	if !rl.allow("a") {
		t.Error("a should be allowed")
	}
	if !rl.allow("b") {
		t.Error("b should be allowed (different key)")
	}
	if rl.allow("a") {
		t.Error("a second request should be denied")
	}
}

// TestRateLimiterSweepReclaims drives a distinct-IP flood past the amortized
// sweep boundary and asserts stale buckets are reclaimed (map stays bounded).
func TestRateLimiterSweepReclaims(t *testing.T) {
	rl := newRateLimiter(1, time.Nanosecond) // window so short every hit is instantly stale
	for i := 0; i < sweepEvery+10; i++ {
		rl.allow(fmt.Sprintf("ip-%d", i))
	}
	// The sweep fires at call sweepEvery; afterwards only buckets added since
	// remain, so the map is far smaller than the number of distinct keys seen.
	rl.mu.Lock()
	n := len(rl.buckets)
	rl.mu.Unlock()
	if n > 16 {
		t.Errorf("buckets = %d after flood, sweep did not reclaim (want small)", n)
	}
}

// testServerWithUpstream returns a server whose viteProxy points at an
// httptest upstream. The caller must Close the returned upstream.
func testServerWithUpstream(t *testing.T) (*server, *httptest.Server) {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Upstream", "hit")
		w.WriteHeader(200)
		w.Write([]byte("upstream:" + r.URL.Path))
	}))
	u, err := url.Parse(up.URL)
	if err != nil {
		t.Fatal(err)
	}
	s := &server{
		cfg:         config{authSecret: "testsecret"},
		viteProxy:   httputil.NewSingleHostReverseProxy(u),
		chatAnonDOS: newRateLimiter(10, time.Minute),
		rr:          newRoutesResource(nil, nil),
	}
	return s, up
}

// regression guard — /pub is the public zone
func TestProxydPubPathNoAuth(t *testing.T) {
	s, up := testServerWithUpstream(t)
	defer up.Close()

	req := httptest.NewRequest("GET", "/pub/anything", nil)
	w := httptest.NewRecorder()
	s.route(w, req)

	if w.Code != 200 {
		t.Errorf("status = %d, want 200 (pub bypasses auth)", w.Code)
	}
	if w.Header().Get("X-Upstream") != "hit" {
		t.Error("upstream not reached")
	}
}

// web_routes redirect: a {/pub/guides/, redirect, /pub/atlas/guides/}
// row rewrites the prefix and 302s into atlas's own slot, preserving
// the suffix + query.
func TestProxydPubWebRouteRedirect(t *testing.T) {
	s, up := testServerWithUpstream(t)
	defer up.Close()
	s.rr.installManualWeb([]store.WebRoute{
		{PathPrefix: "/pub/guides/", Access: "redirect", RedirectTo: "/pub/atlas/guides/", Folder: "atlas"},
	})

	req := httptest.NewRequest("GET", "/pub/guides/x.html?v=1", nil)
	w := httptest.NewRecorder()
	s.route(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/pub/atlas/guides/x.html?v=1" {
		t.Errorf("location = %q, want /pub/atlas/guides/x.html?v=1", loc)
	}
	if w.Header().Get("X-Upstream") == "hit" {
		t.Error("upstream reached — redirect should short-circuit the proxy")
	}
}

// web_routes deny: a deny row returns 403, never proxies.
func TestProxydPubWebRouteDeny(t *testing.T) {
	s, up := testServerWithUpstream(t)
	defer up.Close()
	s.rr.installManualWeb([]store.WebRoute{
		{PathPrefix: "/pub/secret/", Access: "deny", Folder: "atlas"},
	})

	req := httptest.NewRequest("GET", "/pub/secret/x.html", nil)
	w := httptest.NewRecorder()
	s.route(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	if w.Header().Get("X-Upstream") == "hit" {
		t.Error("upstream reached — deny should short-circuit the proxy")
	}
}

// web_routes auth: an Access="auth" row gates the proxy — no credential
// bounces to /auth/login (303), a valid bearer reaches the upstream.
func TestProxydPubWebRouteAuth(t *testing.T) {
	s, up := testServerWithUpstream(t)
	defer up.Close()
	s.rr.installManualWeb([]store.WebRoute{
		{PathPrefix: "/pub/members/", Access: "auth", Folder: "atlas"},
	})

	// (a) no credentials → bounce to login, upstream untouched.
	req := httptest.NewRequest("GET", "/pub/members/x.html", nil)
	w := httptest.NewRecorder()
	s.route(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("unauthed status = %d, want 303", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/auth/login" {
		t.Errorf("unauthed location = %q, want /auth/login", loc)
	}
	if w.Header().Get("X-Upstream") == "hit" {
		t.Error("upstream reached without credentials")
	}

	// (b) valid bearer → upstream reached.
	req = httptest.NewRequest("GET", "/pub/members/x.html", nil)
	req.Header.Set("Authorization", "Bearer "+testMintJWT([]byte("testsecret"), "user:carol"))
	w = httptest.NewRecorder()
	s.route(w, req)
	if w.Code != 200 {
		t.Fatalf("authed status = %d, want 200", w.Code)
	}
	if w.Header().Get("X-Upstream") != "hit" {
		t.Error("upstream not reached with valid bearer")
	}
}

// web_routes longest-prefix wins: a more specific deny under a broader
// redirect takes precedence.
func TestProxydPubWebRouteLongestPrefix(t *testing.T) {
	s, up := testServerWithUpstream(t)
	defer up.Close()
	s.rr.installManualWeb([]store.WebRoute{
		{PathPrefix: "/pub/g/", Access: "redirect", RedirectTo: "/pub/atlas/g/", Folder: "atlas"},
		{PathPrefix: "/pub/g/secret/", Access: "deny", Folder: "atlas"},
	})

	req := httptest.NewRequest("GET", "/pub/g/secret/x.html", nil)
	w := httptest.NewRecorder()
	s.route(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (longest prefix = deny)", w.Code)
	}
}

// No matching web_route → falls through to the vited proxy (existing
// behaviour preserved).
func TestProxydPubWebRouteNoMatchProxies(t *testing.T) {
	s, up := testServerWithUpstream(t)
	defer up.Close()
	s.rr.installManualWeb([]store.WebRoute{
		{PathPrefix: "/pub/guides/", Access: "redirect", RedirectTo: "/pub/atlas/guides/", Folder: "atlas"},
	})

	req := httptest.NewRequest("GET", "/pub/other/x.html", nil)
	w := httptest.NewRecorder()
	s.route(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200 (no match → proxy)", w.Code)
	}
	if w.Header().Get("X-Upstream") != "hit" {
		t.Error("upstream not reached for non-matching path")
	}
}

// /priv/* requires a JWT — anonymous callers bounce to /auth/login.
// Spec 5/V: web/priv/ is a JWT-gated parallel tree to web/pub/.
func TestProxydPrivPathRequiresAuth(t *testing.T) {
	s, up := testServerWithUpstream(t)
	defer up.Close()

	req := httptest.NewRequest("GET", "/priv/krons/secret.html", nil)
	w := httptest.NewRecorder()
	s.route(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303 (auth gate)", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/auth/login" {
		t.Errorf("location = %q, want /auth/login", loc)
	}
	if w.Header().Get("X-Upstream") == "hit" {
		t.Error("upstream reached without auth — gate failed")
	}
}

// /priv/* with a valid JWT reaches the upstream (vite) — path is
// preserved (no /pub rewrite) so vite resolves it to web/priv/.
func TestProxydPrivPathWithJWTReachesUpstream(t *testing.T) {
	s, up := testServerWithUpstream(t)
	defer up.Close()

	tok := testMintJWT([]byte("testsecret"), "alice")
	req := httptest.NewRequest("GET", "/priv/krons/secret.html", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.route(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if w.Header().Get("X-Upstream") != "hit" {
		t.Error("upstream not reached")
	}
	// Path must NOT have been rewritten to /pub/...
	body := w.Body.String()
	if !strings.Contains(body, "/priv/krons/secret.html") {
		t.Errorf("expected /priv path preserved at upstream, got %q", body)
	}
}

// Bare /priv (no trailing slash) also requires auth.
func TestProxydPrivBareRequiresAuth(t *testing.T) {
	s, up := testServerWithUpstream(t)
	defer up.Close()

	req := httptest.NewRequest("GET", "/priv", nil)
	w := httptest.NewRecorder()
	s.route(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303 (auth gate)", w.Code)
	}
}

func TestProxydRootRedirectToPub(t *testing.T) {
	s, up := testServerWithUpstream(t)
	defer up.Close()

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	s.route(w, req)

	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/pub/" {
		t.Errorf("location = %q, want /pub/", loc)
	}
}

// stubProbe lets tests force the reachability result and count calls.
type stubProbe struct {
	mu    sync.Mutex
	ok    bool
	calls int
}

func (p *stubProbe) probe(string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return p.ok
}

// /pub/* with PUB_REDIRECT_URL set and reachable → 302 to upstream URL,
// preserving the post-/pub path and query.
func TestProxydPubExternalRedirectReachable(t *testing.T) {
	s, up := testServerWithUpstream(t)
	defer up.Close()
	sp := &stubProbe{ok: true}
	s.pubRedir = &pubRedirect{url: "https://docs.example.com", ttl: time.Minute, probe: sp.probe}

	req := httptest.NewRequest("GET", "/pub/foo/bar?x=1", nil)
	w := httptest.NewRecorder()
	s.route(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", w.Code)
	}
	if loc, want := w.Header().Get("Location"), "https://docs.example.com/foo/bar?x=1"; loc != want {
		t.Errorf("location = %q, want %q", loc, want)
	}
}

// /pub/* with PUB_REDIRECT_URL set but unreachable → falls through to
// the local viteProxy (no 5xx, no 502 — minimal graceful fallback).
func TestProxydPubExternalRedirectUnreachable(t *testing.T) {
	s, up := testServerWithUpstream(t)
	defer up.Close()
	sp := &stubProbe{ok: false}
	s.pubRedir = &pubRedirect{url: "https://docs.example.com", ttl: time.Minute, probe: sp.probe}

	req := httptest.NewRequest("GET", "/pub/anything", nil)
	w := httptest.NewRecorder()
	s.route(w, req)

	if w.Code != 200 {
		t.Errorf("status = %d, want 200 (fall back to local)", w.Code)
	}
	if w.Header().Get("X-Upstream") != "hit" {
		t.Error("local upstream not reached")
	}
}

// Root → external root redirect when reachable.
func TestProxydRootRedirectExternal(t *testing.T) {
	s, up := testServerWithUpstream(t)
	defer up.Close()
	sp := &stubProbe{ok: true}
	s.pubRedir = &pubRedirect{url: "https://docs.example.com", ttl: time.Minute, probe: sp.probe}

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	s.route(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("status = %d", w.Code)
	}
	if loc, want := w.Header().Get("Location"), "https://docs.example.com/"; loc != want {
		t.Errorf("location = %q, want %q", loc, want)
	}
}

// Reachability is cached within ttl: N hits → 1 probe call.
func TestPubRedirectCachesProbe(t *testing.T) {
	sp := &stubProbe{ok: true}
	pr := &pubRedirect{url: "https://docs.example.com", ttl: time.Minute, probe: sp.probe}
	for i := 0; i < 10; i++ {
		if !pr.reachable() {
			t.Fatal("want reachable")
		}
	}
	if sp.calls != 1 {
		t.Errorf("probe calls = %d, want 1 (cached)", sp.calls)
	}
}

// Expired cache re-probes.
func TestPubRedirectRefreshesAfterTTL(t *testing.T) {
	sp := &stubProbe{ok: true}
	pr := &pubRedirect{url: "https://docs.example.com", ttl: 10 * time.Millisecond, probe: sp.probe}
	pr.reachable()
	time.Sleep(15 * time.Millisecond)
	pr.reachable()
	if sp.calls != 2 {
		t.Errorf("probe calls = %d, want 2 (refreshed)", sp.calls)
	}
}

// Websocket upgrades on /pub/ are not redirected (they need the local
// reverse proxy with hop-by-hop handling).
func TestProxydPubWebsocketBypassesRedirect(t *testing.T) {
	s, up := testServerWithUpstream(t)
	defer up.Close()
	sp := &stubProbe{ok: true}
	s.pubRedir = &pubRedirect{url: "https://docs.example.com", ttl: time.Minute, probe: sp.probe}

	req := httptest.NewRequest("GET", "/pub/ws", nil)
	req.Header.Set("Upgrade", "websocket")
	w := httptest.NewRecorder()
	s.route(w, req)

	if w.Code != 200 {
		t.Errorf("status = %d, want 200 (ws falls through to local proxy)", w.Code)
	}
}

func TestDavRouteForbidden(t *testing.T) {
	s := &server{
		cfg:         config{authSecret: "testsecret"},
		chatAnonDOS: newRateLimiter(10, time.Minute),
	}
	req := httptest.NewRequest("GET", "/dav/bob/", nil)
	req.Header.Set("X-User-Groups", `["alice"]`)
	w := httptest.NewRecorder()
	s.davRoute(nil, w, req) // 403 short-circuits before hitting the proxy

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

func TestProxydRequireAuthExpiredRefreshToken(t *testing.T) {
	st, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateAuthUser("local:admin", "admin", "", "Admin"); err != nil {
		t.Fatal(err)
	}
	token := "expired-refresh"
	if err := st.CreateAuthSession(
		auth.HashToken(token), "local:admin", time.Now().Add(-time.Hour),
	); err != nil {
		t.Fatal(err)
	}

	s := &server{
		cfg:         config{authSecret: "testsecret"},
		st:          st,
		chatAnonDOS: newRateLimiter(10, time.Minute),
	}
	called := false
	h := s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	})
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: "refresh_token", Value: token})
	w := httptest.NewRecorder()
	h(w, req)

	if called {
		t.Error("handler called with expired refresh session")
	}
	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", w.Code)
	}
}

func TestProxydRequireAuthRefreshTokenUserMissing(t *testing.T) {
	st, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	// session exists, but no auth_users row for its UserSub
	token := "orphan-refresh"
	if err := st.CreateAuthSession(
		auth.HashToken(token), "local:ghost", time.Now().Add(time.Hour),
	); err != nil {
		t.Fatal(err)
	}

	s := &server{
		cfg:         config{authSecret: "testsecret"},
		st:          st,
		chatAnonDOS: newRateLimiter(10, time.Minute),
	}
	called := false
	h := s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	})
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: "refresh_token", Value: token})
	w := httptest.NewRecorder()
	h(w, req)

	if called {
		t.Error("handler called when session user is missing")
	}
	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", w.Code)
	}
}

// --- additional test gaps: auth gate, oauth, slink, vhost, dav --------------

// testRouteServer builds a server whose viteProxy forwards to a stub upstream.
// The returned upstream must be Closed by the caller. secret is configured as
// the auth signing key; pass empty to disable auth (fails closed for protected
// routes). Default TOML routes (/chat/, /slink/) are installed pointing at the
// same upstream so existing tests keep their behaviour.
func testRouteServer(t *testing.T, st *store.Store, secret string) (*server, *httptest.Server) {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Upstream-Path", r.URL.Path)
		w.Header().Set("X-Upstream-Host", r.Host)
		w.Header().Set("X-User-Sub", r.Header.Get("X-User-Sub"))
		w.Header().Set("X-Folder", r.Header.Get("X-Folder"))
		w.Header().Set("X-Chat-Token", r.Header.Get("X-Chat-Token"))
		w.WriteHeader(200)
		w.Write([]byte("ok:" + r.URL.Path))
	}))
	u, err := url.Parse(up.URL)
	if err != nil {
		t.Fatal(err)
	}
	chatRoute := Route{Path: "/chat/", Backend: up.URL, Auth: "public"}
	panelRoute := Route{Path: "/panel/", Backend: up.URL, Auth: "user"}
	slinkRoute := Route{Path: "/slink/", Backend: up.URL, Auth: "public"}
	s := &server{
		cfg:         config{authSecret: secret},
		st:          st,
		stRoutd:     st, // tests use single in-memory DB with all tables
		viteProxy:   httputil.NewSingleHostReverseProxy(u),
		chatAnonDOS: newRateLimiter(10, time.Minute),
		rr:          newRoutesResource(nil, []Route{chatRoute, panelRoute, slinkRoute}),
	}
	return s, up
}

// --- auth gate ---------------------------------------------------------------

// Unknown path redirects to /pub/ prefix (public fallback).
func TestProxydRouteUnknownPathRedirectsToPub(t *testing.T) {
	s, up := testRouteServer(t, nil, "testsecret")
	defer up.Close()

	req := httptest.NewRequest("GET", "/arizuko", nil)
	w := httptest.NewRecorder()
	s.route(w, req)

	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/pub/arizuko" {
		t.Errorf("location = %q, want /pub/arizuko", loc)
	}
}

// Auth-gated path (/chat/) without credentials bounces to login.
func TestProxydRouteUnauthedChatRedirectsToLogin(t *testing.T) {
	s, up := testRouteServer(t, nil, "testsecret")
	defer up.Close()

	req := httptest.NewRequest("GET", "/panel/atlas", nil)
	w := httptest.NewRecorder()
	s.route(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/auth/login" {
		t.Errorf("location = %q, want /auth/login", loc)
	}
}

// Valid JWT on an auth-gated route reaches the upstream and carries user headers.
func TestProxydRouteWithJWTReachesUpstream(t *testing.T) {
	s, up := testRouteServer(t, nil, "testsecret")
	defer up.Close()

	tok := testMintJWT([]byte("testsecret"), "alice")
	req := httptest.NewRequest("GET", "/panel/atlas", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.route(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("X-User-Sub"); got != "alice" {
		t.Errorf("X-User-Sub upstream echo = %q, want alice", got)
	}
}

// Valid refresh_token cookie admits the request to the upstream.
func TestProxydRouteWithRefreshCookieReachesUpstream(t *testing.T) {
	st, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateAuthUser("local:bob", "bob", "", "Bob"); err != nil {
		t.Fatal(err)
	}
	token := "valid-refresh"
	if err := st.CreateAuthSession(
		auth.HashToken(token), "local:bob", time.Now().Add(time.Hour),
	); err != nil {
		t.Fatal(err)
	}

	s, up := testRouteServer(t, st, "testsecret")
	defer up.Close()

	req := httptest.NewRequest("GET", "/panel/atlas", nil)
	req.AddCookie(&http.Cookie{Name: "refresh_token", Value: token})
	w := httptest.NewRecorder()
	s.route(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("X-User-Sub"); got != "local:bob" {
		t.Errorf("X-User-Sub upstream echo = %q, want local:bob", got)
	}
}

// Raw auth secret passed as refresh_token must not yield a session hit.
// This is a belt-and-braces check for the TODO note about a supposed
// raw-secret bypass: the code hashes the cookie value and looks it up as a
// session, so the raw secret can never match.
func TestProxydRouteRawSecretAsRefreshCookieRejected(t *testing.T) {
	st, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	s, up := testRouteServer(t, st, "rawsecret")
	defer up.Close()

	req := httptest.NewRequest("GET", "/panel/atlas", nil)
	req.AddCookie(&http.Cookie{Name: "refresh_token", Value: "rawsecret"})
	w := httptest.NewRecorder()
	s.route(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (raw secret is not a session token)", w.Code)
	}
}

// --- oauth / auth routes via handler ----------------------------------------

// /auth/* 302s to authd (the only OAuth host since the local HS256 mount was
// removed). Path + query carry through so logins and provider callbacks reach
// authd unchanged, bypassing requireAuth.
func TestProxydHandlerAuthRedirectsToAuthd(t *testing.T) {
	st, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	s, up := testRouteServer(t, st, "testsecret")
	defer up.Close()
	s.cfg.authdURL = "http://authd:8080"
	h := s.handler(nil)

	for _, path := range []string{"/auth/login", "/auth/google/callback?code=x&state=y"} {
		req := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusFound {
			t.Errorf("%s status = %d, want 302 to authd", path, w.Code)
		}
		if loc := w.Header().Get("Location"); loc != "http://authd:8080"+path {
			t.Errorf("%s Location = %q, want authd + path", path, loc)
		}
	}
}

// --- slink rate limiter ------------------------------------------------------

// Exactly N requests in the window are admitted; the N+1th is 429.
func TestProxydSlinkRateLimiterBoundary(t *testing.T) {
	s, up := testRouteServer(t, nil, "testsecret")
	defer up.Close()
	s.chatAnonDOS = newRateLimiter(3, time.Minute)

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest("GET", "/chat/tok/path", nil)
		req.RemoteAddr = "7.7.7.7:4242"
		w := httptest.NewRecorder()
		s.route(w, req)
		if w.Code != 200 {
			t.Fatalf("request %d: status = %d, want 200", i, w.Code)
		}
	}
	req := httptest.NewRequest("GET", "/chat/tok/path", nil)
	req.RemoteAddr = "7.7.7.7:4242"
	w := httptest.NewRecorder()
	s.route(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("4th request: status = %d, want 429", w.Code)
	}
}

// Limiter is keyed per remote IP — saturation on one IP does not lock out
// another.
func TestProxydSlinkRateLimiterPerIP(t *testing.T) {
	s, up := testRouteServer(t, nil, "testsecret")
	defer up.Close()
	s.chatAnonDOS = newRateLimiter(1, time.Minute)

	// IP A exhausts its slot.
	req := httptest.NewRequest("GET", "/chat/tok", nil)
	req.RemoteAddr = "1.1.1.1:1000"
	w := httptest.NewRecorder()
	s.route(w, req)
	if w.Code != 200 {
		t.Fatalf("A first: status = %d, want 200", w.Code)
	}

	// IP A second attempt → denied.
	req = httptest.NewRequest("GET", "/chat/tok", nil)
	req.RemoteAddr = "1.1.1.1:1000"
	w = httptest.NewRecorder()
	s.route(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("A second: status = %d, want 429", w.Code)
	}

	// IP B gets its own slot.
	req = httptest.NewRequest("GET", "/chat/tok", nil)
	req.RemoteAddr = "2.2.2.2:2000"
	w = httptest.NewRecorder()
	s.route(w, req)
	if w.Code != 200 {
		t.Errorf("B first: status = %d, want 200", w.Code)
	}
}

// Valid chat token stamps X-Folder / X-Chat-Token on the proxied request.
func TestProxydChatTokenStampsHeaders(t *testing.T) {
	st, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.PutGroup(core.Group{
		Folder:  "team-a",
		AddedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertRouteToken("tokabc", store.RouteToken{
		JID: "web:team-a", OwnerFolder: "team-a",
	}); err != nil {
		t.Fatal(err)
	}

	s, up := testRouteServer(t, st, "testsecret")
	defer up.Close()

	req := httptest.NewRequest("GET", "/chat/tokabc/deep", nil)
	req.RemoteAddr = "3.3.3.3:3000"
	w := httptest.NewRecorder()
	s.route(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("X-Folder"); got != "team-a" {
		t.Errorf("X-Folder upstream echo = %q, want team-a", got)
	}
	if got := w.Header().Get("X-Chat-Token"); got != "tokabc" {
		t.Errorf("X-Chat-Token upstream echo = %q, want tokabc", got)
	}
}

// --- derived host → world redirect (spec 5/V) --------------------------------

// vhostServer builds a route server with a fixed HOSTING_DOMAIN + optional
// aliases. No upstream is needed — the world redirect is a 302, not a proxy.
func vhostServer(domain string, aliases map[string]string) *server {
	return &server{
		cfg:         config{authSecret: "testsecret", hostingDomain: domain, vhostAliases: aliases},
		chatAnonDOS: newRateLimiter(10, time.Minute),
		rr:          newRoutesResource(nil, nil),
	}
}

// A mapped host derives its world from HOSTING_DOMAIN and 302s every path to
// the world's public slot, preserving sub-path, trailing slash, and query.
func TestProxydVhostDerivedRedirect(t *testing.T) {
	s := vhostServer("fiu.wtf", nil)
	cases := []struct{ in, want string }{
		{"/", "/pub/krons/"},
		{"/biotech-swe-guide/", "/pub/krons/biotech-swe-guide/"},
		{"/x?a=1", "/pub/krons/x?a=1"},
	}
	for _, c := range cases {
		req := httptest.NewRequest("GET", c.in, nil)
		req.Host = "krons.fiu.wtf"
		w := httptest.NewRecorder()
		s.route(w, req)
		if w.Code != http.StatusFound {
			t.Errorf("%s: status = %d, want 302", c.in, w.Code)
		}
		if loc := w.Header().Get("Location"); loc != c.want {
			t.Errorf("%s: location = %q, want %q", c.in, loc, c.want)
		}
	}
}

// An explicit WEB_VHOST_ALIASES entry maps a host whose label ≠ world name.
func TestProxydVhostAliasRedirect(t *testing.T) {
	s := vhostServer("fiu.wtf", map[string]string{"fab.krons.cx": "atlas"})
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "fab.krons.cx"
	w := httptest.NewRecorder()
	s.route(w, req)
	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/pub/atlas/" {
		t.Errorf("location = %q, want /pub/atlas/", loc)
	}
}

// Un-mapped hosts fall through to the default `/pub`+path redirect: the bare
// HOSTING_DOMAIN (no subdomain), a multi-label subdomain, and a host whose
// suffix boundary fails (`xfiu.wtf` — raw HasSuffix("fiu.wtf") is true but the
// `.`-boundary check rejects it).
func TestProxydVhostUnmappedFallsThrough(t *testing.T) {
	s := vhostServer("fiu.wtf", nil)
	for _, host := range []string{"fiu.wtf", "a.b.fiu.wtf", "xfiu.wtf"} {
		req := httptest.NewRequest("GET", "/page", nil)
		req.Host = host
		w := httptest.NewRecorder()
		s.route(w, req)
		if w.Code != http.StatusFound {
			t.Errorf("%s: status = %d, want 302", host, w.Code)
		}
		if loc := w.Header().Get("Location"); loc != "/pub/page" {
			t.Errorf("%s: location = %q, want /pub/page (default fallthrough)", host, loc)
		}
	}
}

// Boundary-checked suffix removal: a distinct single-label host maps to its own
// world (`notkrons.fiu.wtf`→`notkrons`, NOT `krons`), `xfiu.wtf` is rejected.
func TestWorldForHost(t *testing.T) {
	s := vhostServer("fiu.wtf", map[string]string{"fab.krons.cx": "atlas"})
	cases := []struct{ host, want string }{
		{"krons.fiu.wtf", "krons"},
		{"krons.fiu.wtf:8443", "krons"},  // port stripped
		{"KRONS.FIU.WTF", "krons"},       // lowercased
		{"krons.fiu.wtf.", "krons"},      // trailing dot trimmed
		{"fab.krons.cx", "atlas"},        // alias wins
		{"fiu.wtf", ""},                  // bare domain
		{"a.b.fiu.wtf", ""},              // multi-label
		{"notkrons.fiu.wtf", "notkrons"}, // valid single label, distinct world
		{"xfiu.wtf", ""},                 // no dot boundary
		{"krons.other.com", ""},          // unknown suffix
	}
	for _, c := range cases {
		if got := s.worldForHost(c.host); got != c.want {
			t.Errorf("worldForHost(%q) = %q, want %q", c.host, got, c.want)
		}
	}
}

// Reserved prefixes are served in place on a mapped host — they are NOT
// vhost-redirected, so the 302 cannot loop. /pub/other/ and /dash/ stay
// in their own branches even when Host derives a world.
func TestProxydVhostReservedInPlace(t *testing.T) {
	s, up := testRouteServer(t, nil, "testsecret")
	defer up.Close()
	s.cfg.hostingDomain = "fiu.wtf"

	// /pub/other/ is served by the /pub/ branch (proxied), not redirected.
	req := httptest.NewRequest("GET", "/pub/other/", nil)
	req.Host = "krons.fiu.wtf"
	w := httptest.NewRecorder()
	s.route(w, req)
	if w.Code != 200 {
		t.Errorf("/pub/other/: status = %d, want 200 (served in place)", w.Code)
	}
	if got := w.Header().Get("X-Upstream-Path"); got != "/pub/other/" {
		t.Errorf("/pub/other/: upstream path = %q, want /pub/other/ (no vhost rewrite)", got)
	}

	// /dash/ with no /dash/ route falls to the default /pub fallback — NOT a
	// world redirect (it would be /pub/krons/dash/ if the catch-all derived).
	req = httptest.NewRequest("GET", "/dash/", nil)
	req.Host = "krons.fiu.wtf"
	w = httptest.NewRecorder()
	s.route(w, req)
	if loc := w.Header().Get("Location"); loc != "/pub/krons/dash/" {
		t.Errorf("/dash/ on mapped host: location = %q, want /pub/krons/dash/ (catch-all derives, but /pub/ branch then serves in place)", loc)
	}
}

// X-Forwarded-Host must not influence world derivation — only the real Host.
func TestProxydVhostIgnoresXForwardedHost(t *testing.T) {
	s := vhostServer("fiu.wtf", nil)
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "fiu.wtf" // bare domain → un-mapped
	req.Header.Set("X-Forwarded-Host", "krons.fiu.wtf")
	w := httptest.NewRecorder()
	s.route(w, req)
	if loc := w.Header().Get("Location"); loc != "/pub/" {
		t.Errorf("X-Forwarded-Host leaked into vhost routing: location = %q, want /pub/", loc)
	}
}

// Path traversal on a mapped host is rejected by the redirect builder.
func TestProxydVhostRejectsTraversal(t *testing.T) {
	s := vhostServer("fiu.wtf", nil)
	req := httptest.NewRequest("GET", "/..%2fetc/passwd", nil)
	req.Host = "krons.fiu.wtf"
	w := httptest.NewRecorder()
	s.route(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 on traversal", w.Code)
	}
}

func TestParseVhostAliases(t *testing.T) {
	got := groupfolder.ParseVhostAliases("fab.krons.cx=atlas, FOO.example.com=bar ,bad,=x,y=, ")
	want := map[string]string{"fab.krons.cx": "atlas", "foo.example.com": "bar"}
	if len(got) != len(want) {
		t.Fatalf("parsed %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("alias[%q] = %q, want %q", k, got[k], v)
		}
	}
	if m := groupfolder.ParseVhostAliases(""); len(m) != 0 {
		t.Errorf("empty input → %v, want empty map", m)
	}

	// Validation (BUG #6): an invalid hostname or invalid world folder is
	// skipped loudly, never accepted — a typo'd alias must not 302 traffic to
	// /pub/<garbage>/. The valid entry survives alongside the rejects.
	got = groupfolder.ParseVhostAliases("ok.example.com=atlas,bad host=x,foo.com=../escape")
	if len(got) != 1 || got["ok.example.com"] != "atlas" {
		t.Fatalf("validation: got %v, want only {ok.example.com:atlas}", got)
	}
}

// --- /dav rewrite ------------------------------------------------------------

// testDavServer returns a server with /dav/ wired through a strip-prefix
// reverse proxy to the stub upstream. The bundled davRoute is invoked
// directly via the returned proxy.
func testDavServer(t *testing.T) (*server, *httputil.ReverseProxy, *httptest.Server) {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Upstream-Path", r.URL.Path)
		w.WriteHeader(200)
		w.Write([]byte("dav:" + r.URL.Path))
	}))
	rp := buildRouteProxy(Route{Path: "/dav/", Backend: up.URL, StripPrefix: true})
	s := &server{
		cfg:         config{authSecret: "testsecret"},
		chatAnonDOS: newRateLimiter(10, time.Minute),
		rr:          newRoutesResource(nil, []Route{{Path: "/dav/", Backend: up.URL, Auth: "user", StripPrefix: true}}),
	}
	return s, rp, up
}

// /dav/foo/bar is rewritten to /foo/bar before being forwarded.
func TestProxydDavRewriteStripsPrefix(t *testing.T) {
	s, rp, up := testDavServer(t)
	defer up.Close()

	// davRoute's group-matching gate requires the user to be in the folder —
	// hit it directly with an allowed group header so the proxy actually runs.
	req := httptest.NewRequest("GET", "/dav/foo/bar", nil)
	req.Header.Set("X-User-Groups", `["foo"]`)
	w := httptest.NewRecorder()
	s.davRoute(rp, w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("X-Upstream-Path"); got != "/foo/bar" {
		t.Errorf("upstream path = %q, want /foo/bar", got)
	}
}

// /dav with a trailing slash but no group redirects to the user's first group.
func TestProxydDavBareRedirects(t *testing.T) {
	s, rp, up := testDavServer(t)
	defer up.Close()

	req := httptest.NewRequest("GET", "/dav", nil)
	req.Header.Set("X-User-Groups", `["alpha","beta"]`)
	w := httptest.NewRecorder()
	s.davRoute(rp, w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", w.Code)
	}
	if got := w.Header().Get("Location"); got != "/dav/alpha/" {
		t.Errorf("location = %q, want /dav/alpha/", got)
	}
}

// /dav (bare) picks the first non-`**` group in *sorted* order — not the
// claim's original order. Map iteration upstream is non-deterministic, so
// "first" must be a sort, not a position.
func TestProxydDavBareSortsGroups(t *testing.T) {
	s, rp, up := testDavServer(t)
	defer up.Close()

	req := httptest.NewRequest("GET", "/dav", nil)
	req.Header.Set("X-User-Groups", `["b/x","**","a/y"]`)
	w := httptest.NewRecorder()
	s.davRoute(rp, w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", w.Code)
	}
	if got := w.Header().Get("Location"); got != "/dav/a/y/" {
		t.Errorf("location = %q, want /dav/a/y/ (sorted, skips **)", got)
	}
}

// /dav (bare) with no X-User-Groups header redirects to /dav/root/.
func TestProxydDavBareNoGroupsDefaultsRoot(t *testing.T) {
	s, rp, up := testDavServer(t)
	defer up.Close()

	req := httptest.NewRequest("GET", "/dav", nil)
	w := httptest.NewRecorder()
	s.davRoute(rp, w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", w.Code)
	}
	if got := w.Header().Get("Location"); got != "/dav/root/" {
		t.Errorf("location = %q, want /dav/root/", got)
	}
}

// Operator (grant `**`) reaches upstream for any /dav/<folder>. Operator
// is implicit — there's no separate "no header" bypass.
func TestProxydDavOperatorProxies(t *testing.T) {
	s, rp, up := testDavServer(t)
	defer up.Close()

	req := httptest.NewRequest("GET", "/dav/anything/here", nil)
	req.Header.Set("X-User-Groups", `["**"]`)
	w := httptest.NewRecorder()
	s.davRoute(rp, w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("X-Upstream-Path"); got != "/anything/here" {
		t.Errorf("upstream path = %q, want /anything/here", got)
	}
}

// A glob pattern like "pub/*" in X-User-Groups admits a matching nested
// group without needing a literal entry. This exercises the MatchGroups
// path in davRoute.
func TestProxydDavGlobGroupsAllowed(t *testing.T) {
	s, rp, up := testDavServer(t)
	defer up.Close()

	req := httptest.NewRequest("GET", "/dav/pub-alice/file", nil)
	req.Header.Set("X-User-Groups", `["pub-*"]`)
	w := httptest.NewRecorder()
	s.davRoute(rp, w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200 (glob match allowed)", w.Code)
	}
}

// Malformed X-User-Groups header rejects the request.
func TestProxydDavBadGroupsHeader(t *testing.T) {
	s, rp, up := testDavServer(t)
	defer up.Close()

	req := httptest.NewRequest("GET", "/dav/anything", nil)
	req.Header.Set("X-User-Groups", "not-json")
	w := httptest.NewRecorder()
	s.davRoute(rp, w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

// When the /dav/ route is absent (WEBDAV_ENABLED=false), /dav/* returns
// 404 with the dedicated message before auth is checked.
func TestDavRouteNotConfigured(t *testing.T) {
	s := &server{
		cfg:         config{authSecret: "testsecret"},
		chatAnonDOS: newRateLimiter(10, time.Minute),
		// routes intentionally empty: no /dav/ route → 404
	}
	for _, path := range []string{"/dav", "/dav/", "/dav/folder/file"} {
		w := httptest.NewRecorder()
		s.route(w, httptest.NewRequest("GET", path, nil))
		if w.Code != http.StatusNotFound {
			t.Errorf("path %q: status = %d, want 404", path, w.Code)
		}
		if !strings.Contains(w.Body.String(), "WebDAV not configured") {
			t.Errorf("path %q: body = %q, want 'WebDAV not configured'", path, w.Body.String())
		}
	}
}

// Sensitive write-blocks: PUT/DELETE/MOVE/COPY/MKCOL/POST/PROPPATCH on
// `.env`, `*.pem`, or under `.git/` are 403 even for the rightful group.
func TestProxydDavBlocksSensitiveWrites(t *testing.T) {
	s, rp, up := testDavServer(t)
	defer up.Close()

	cases := []struct {
		name, method, path string
	}{
		{"env_put", "PUT", "/dav/myg/.env"},
		{"env_delete", "DELETE", "/dav/myg/.env"},
		{"env_move", "MOVE", "/dav/myg/.env"},
		{"pem_put", "PUT", "/dav/myg/keys/server.pem"},
		{"pem_propppatch", "PROPPATCH", "/dav/myg/server.pem"},
		{"git_dir_put", "PUT", "/dav/myg/.git/config"},
		{"git_mkcol", "MKCOL", "/dav/myg/.git"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(c.method, c.path, nil)
			req.Header.Set("X-User-Groups", `["myg"]`)
			w := httptest.NewRecorder()
			s.davRoute(rp, w, req)
			if w.Code != http.StatusForbidden {
				t.Errorf("%s %s: status = %d, want 403", c.method, c.path, w.Code)
			}
		})
	}
}

// Read methods on sensitive files still pass through (operator can inspect).
func TestProxydDavReadsSensitiveAllowed(t *testing.T) {
	s, rp, up := testDavServer(t)
	defer up.Close()

	for _, m := range []string{"GET", "HEAD", "OPTIONS", "PROPFIND"} {
		req := httptest.NewRequest(m, "/dav/myg/.env", nil)
		req.Header.Set("X-User-Groups", `["myg"]`)
		w := httptest.NewRecorder()
		s.davRoute(rp, w, req)
		if w.Code != 200 {
			t.Errorf("%s /dav/myg/.env: status = %d, want 200", m, w.Code)
		}
	}
}

// `<group>/logs/...` is read-only — any non-read method is 403, reads pass.
func TestProxydDavLogsReadOnly(t *testing.T) {
	s, rp, up := testDavServer(t)
	defer up.Close()

	// Writes blocked.
	for _, m := range []string{"PUT", "POST", "DELETE", "MOVE", "COPY", "MKCOL", "PROPPATCH"} {
		req := httptest.NewRequest(m, "/dav/myg/logs/run.log", nil)
		req.Header.Set("X-User-Groups", `["myg"]`)
		w := httptest.NewRecorder()
		s.davRoute(rp, w, req)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s /dav/myg/logs/run.log: status = %d, want 403", m, w.Code)
		}
	}

	// Reads allowed.
	for _, m := range []string{"GET", "HEAD", "OPTIONS", "PROPFIND"} {
		req := httptest.NewRequest(m, "/dav/myg/logs/run.log", nil)
		req.Header.Set("X-User-Groups", `["myg"]`)
		w := httptest.NewRecorder()
		s.davRoute(rp, w, req)
		if w.Code != 200 {
			t.Errorf("%s /dav/myg/logs/run.log: status = %d, want 200", m, w.Code)
		}
	}

	// Bare `<group>/logs` (no trailing slash) also read-only.
	req := httptest.NewRequest("DELETE", "/dav/myg/logs", nil)
	req.Header.Set("X-User-Groups", `["myg"]`)
	w := httptest.NewRecorder()
	s.davRoute(rp, w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("DELETE /dav/myg/logs: status = %d, want 403", w.Code)
	}
}

// Writes outside logs and sensitive paths still go through.
func TestProxydDavOrdinaryWriteAllowed(t *testing.T) {
	s, rp, up := testDavServer(t)
	defer up.Close()

	req := httptest.NewRequest("PUT", "/dav/myg/notes/todo.md", nil)
	req.Header.Set("X-User-Groups", `["myg"]`)
	w := httptest.NewRecorder()
	s.davRoute(rp, w, req)
	if w.Code != 200 {
		t.Errorf("PUT /dav/myg/notes/todo.md: status = %d, want 200", w.Code)
	}
}

// davAllow unit tests — exercise the matcher directly for boundary cases.
func TestDavAllow(t *testing.T) {
	cases := []struct {
		method, rest string
		want         bool
	}{
		// Reads always allowed.
		{"GET", "g/logs/x", true},
		{"PROPFIND", "g/.env", true},
		{"HEAD", "g/sub/key.pem", true},
		// Sensitive writes blocked.
		{"PUT", "g/.env", false},
		{"DELETE", "g/sub/.env", false},
		{"PUT", "g/sub/server.pem", false},
		{"MOVE", "g/.git/HEAD", false},
		{"MKCOL", "g/.git", false},
		// logs read-only.
		{"PUT", "g/logs/file", false},
		{"DELETE", "g/logs", false},
		{"GET", "g/logs", true},
		// Innocuous writes pass.
		{"PUT", "g/notes/file.md", true},
		{"MKCOL", "g/sub", true},
		// Non-`.env` similarly named files — only exact `.env` segment blocks.
		{"PUT", "g/env.txt", true},
		{"PUT", "g/.envrc", true},
	}
	for _, c := range cases {
		got := davAllow(c.method, c.rest)
		if got != c.want {
			t.Errorf("davAllow(%q, %q) = %v, want %v", c.method, c.rest, got, c.want)
		}
	}
}

// TOML-route dispatch tests: exercise the PROXYD_ROUTES_JSON code path
// end-to-end via server.route.

// installRoute is a tiny helper for tests that wires one TOML route into
// the server's route table. Test-only: uses the manual fallback path
// (st nil); production reads from the DB per request (spec 5/36).
func installRoute(s *server, r Route) {
	if s.rr == nil {
		s.rr = newRoutesResource(nil, nil)
	}
	cur, _ := s.rr.snapshot()
	s.rr.installManual(append(append([]Route(nil), cur...), r))
}

func TestProxyd_TOMLRoute_HandlesSlackPrefix(t *testing.T) {
	var seenPath string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		w.WriteHeader(200)
	}))
	defer up.Close()

	s := testServer()
	s.cfg.authSecret = "testsecret"
	installRoute(s, Route{Path: "/slack/", Backend: up.URL, Auth: "public"})

	req := httptest.NewRequest("POST", "/slack/events", strings.NewReader("body"))
	w := httptest.NewRecorder()
	s.route(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if seenPath != "/slack/events" {
		t.Errorf("backend saw %q, want /slack/events", seenPath)
	}
}

func TestProxyd_TOMLRoute_PublicAuth_BypassesIdentityCheck(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer up.Close()

	s := testServer()
	s.cfg.authSecret = "testsecret"
	installRoute(s, Route{Path: "/slack/", Backend: up.URL, Auth: "public"})

	// No credentials at all — auth=public means proxyd never asks.
	req := httptest.NewRequest("POST", "/slack/events", nil)
	w := httptest.NewRecorder()
	s.route(w, req)

	if w.Code != 200 {
		t.Errorf("status = %d, want 200 (public route)", w.Code)
	}
}

func TestProxyd_TOMLRoute_UserAuth_RequiresIdentity(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer up.Close()

	s := testServer()
	s.cfg.authSecret = "testsecret"
	installRoute(s, Route{Path: "/dash/", Backend: up.URL, Auth: "user"})

	req := httptest.NewRequest("GET", "/dash/status", nil)
	w := httptest.NewRecorder()
	s.route(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303 (auth=user redirects to login)", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/auth/login" {
		t.Errorf("location = %q, want /auth/login", loc)
	}
}

func TestProxyd_TOMLRoute_PreservesHeaders(t *testing.T) {
	// Verifies the three contractual guarantees of `auth=public` + preserve_headers:
	//   1. body passes through verbatim (Slack HMAC is over the raw body)
	//   2. preserve_headers entries arrive verbatim
	//   3. Host is rewritten to the backend by default (no host preservation)
	var seenSig, seenTs, seenHost string
	var seenBody []byte
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenSig = r.Header.Get("X-Slack-Signature")
		seenTs = r.Header.Get("X-Slack-Request-Timestamp")
		seenHost = r.Host
		seenBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(200)
	}))
	defer up.Close()
	upURL, _ := url.Parse(up.URL)

	s := testServer()
	installRoute(s, Route{
		Path:            "/slack/",
		Backend:         up.URL,
		Auth:            "public",
		PreserveHeaders: []string{"X-Slack-Signature", "X-Slack-Request-Timestamp"},
	})

	const wantBody = "payload"
	req := httptest.NewRequest("POST", "/slack/events", strings.NewReader(wantBody))
	req.Host = "proxyd.example.com"
	req.Header.Set("X-Slack-Signature", "v0=cafebabe")
	req.Header.Set("X-Slack-Request-Timestamp", "1700000000")
	w := httptest.NewRecorder()
	s.route(w, req)

	if seenSig != "v0=cafebabe" {
		t.Errorf("backend X-Slack-Signature = %q, want v0=cafebabe", seenSig)
	}
	if seenTs != "1700000000" {
		t.Errorf("backend X-Slack-Request-Timestamp = %q, want 1700000000", seenTs)
	}
	if string(seenBody) != wantBody {
		t.Errorf("backend body = %q, want %q", seenBody, wantBody)
	}
	if seenHost != upURL.Host {
		t.Errorf("backend Host = %q, want %q (Host rewrite default ON)", seenHost, upURL.Host)
	}
}

// clientIP prefers the left-most X-Forwarded-For hop (real client) over the
// edge-proxy peer, so abuse triage sees the source not the hop.
func TestClientIP(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.5.1:54321"
	if got := clientIP(r); got != "10.0.5.1" {
		t.Errorf("no XFF: got %q, want 10.0.5.1", got)
	}
	r.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.5.1")
	if got := clientIP(r); got != "203.0.113.7" {
		t.Errorf("with XFF: got %q, want 203.0.113.7", got)
	}
}

// --- WebDAV JWT auth flow (spec M-webdav.md) ----------------------------------

// testMintJWTWithGroups produces an HS256 JWT that includes the groups claim.
// This is for direct JWT→header flows; DB-backed group lookup uses groupsForSub.
func testMintJWTWithGroups(secret []byte, sub string, groups []string) string {
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	groupsJSON, _ := json.Marshal(groups)
	c := fmt.Sprintf(`{"sub":%q,"name":"test","groups":%s,"exp":%d,"iat":%d}`,
		sub, groupsJSON, time.Now().Add(time.Hour).Unix(), time.Now().Unix())
	body := base64.RawURLEncoding.EncodeToString([]byte(c))
	h := hmac.New(sha256.New, secret)
	h.Write([]byte(hdr + "." + body))
	sig := base64.RawURLEncoding.EncodeToString(h.Sum(nil))
	return hdr + "." + body + "." + sig
}

// testDavFullServer returns a server wired through server.route for /dav/*,
// exercising the full requireAuth → davRoute flow. st may be nil for tests
// that don't need DB-backed group lookup.
func testDavFullServer(t *testing.T, st *store.Store, secret string) (*server, *httptest.Server) {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Upstream-Path", r.URL.Path)
		w.Header().Set("X-User-Sub", r.Header.Get("X-User-Sub"))
		w.Header().Set("X-User-Groups", r.Header.Get("X-User-Groups"))
		w.WriteHeader(200)
		w.Write([]byte("dav:" + r.URL.Path))
	}))
	u, _ := url.Parse(up.URL)
	davRoute := Route{Path: "/dav/", Backend: up.URL, Auth: "user", StripPrefix: true}
	s := &server{
		cfg:         config{authSecret: secret},
		st:          st,
		stRoutd:     st, // tests use single in-memory DB
		viteProxy:   httputil.NewSingleHostReverseProxy(u),
		chatAnonDOS: newRateLimiter(10, time.Minute),
		rr:          newRoutesResource(nil, []Route{davRoute}),
	}
	return s, up
}

// /dav/* without credentials bounces to /auth/login (JWT required).
func TestProxydDavAuthFlow_NoCredentials(t *testing.T) {
	s, up := testDavFullServer(t, nil, "testsecret")
	defer up.Close()

	req := httptest.NewRequest("GET", "/dav/mygroup/file.txt", nil)
	w := httptest.NewRecorder()
	s.route(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 redirect to login", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/auth/login" {
		t.Errorf("location = %q, want /auth/login", loc)
	}
}

// /dav/* with valid JWT + matching group claim reaches the upstream.
func TestProxydDavAuthFlow_ValidJWT(t *testing.T) {
	s, up := testDavFullServer(t, nil, "testsecret")
	defer up.Close()

	tok := testMintJWTWithGroups([]byte("testsecret"), "user:alice", []string{"team-a"})
	req := httptest.NewRequest("GET", "/dav/team-a/notes.md", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.route(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("X-User-Sub"); got != "user:alice" {
		t.Errorf("X-User-Sub = %q, want user:alice", got)
	}
	if got := w.Header().Get("X-Upstream-Path"); got != "/team-a/notes.md" {
		t.Errorf("X-Upstream-Path = %q, want /team-a/notes.md (prefix stripped)", got)
	}
}

// User in group A cannot access /dav/B/ — JWT is valid but group mismatch → 403.
func TestProxydDavAuthFlow_CrossGroupDenied(t *testing.T) {
	s, up := testDavFullServer(t, nil, "testsecret")
	defer up.Close()

	tok := testMintJWTWithGroups([]byte("testsecret"), "user:alice", []string{"group-a"})
	req := httptest.NewRequest("GET", "/dav/group-b/secret.md", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.route(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (cross-group access denied)", w.Code)
	}
}

// /dav/* with DB-backed group lookup via refresh_token cookie: groups come
// from acl table via UserScopes, not from JWT claims.
func TestProxydDavAuthFlow_DBGroupLookup(t *testing.T) {
	st, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	// Create user + grant via acl row.
	if err := st.CreateAuthUser("local:bob", "bob", "", "Bob"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutACLRow(core.ACLRow{
		Principal: "local:bob",
		Action:    "*",
		Scope:     "workspace-x",
		Effect:    "allow",
	}); err != nil {
		t.Fatal(err)
	}
	// Create refresh session for cookie-based auth.
	refreshTok := "dav-test-refresh"
	if err := st.CreateAuthSession(
		auth.HashToken(refreshTok), "local:bob", time.Now().Add(time.Hour),
	); err != nil {
		t.Fatal(err)
	}

	s, up := testDavFullServer(t, st, "testsecret")
	defer up.Close()

	// Use refresh_token cookie; groups come from DB via UserScopes.
	req := httptest.NewRequest("GET", "/dav/workspace-x/file.txt", nil)
	req.AddCookie(&http.Cookie{Name: "refresh_token", Value: refreshTok})
	w := httptest.NewRecorder()
	s.route(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200 (DB grant should allow)", w.Code)
	}
	if got := w.Header().Get("X-Upstream-Path"); got != "/workspace-x/file.txt" {
		t.Errorf("X-Upstream-Path = %q, want /workspace-x/file.txt", got)
	}
}

// DB grant for group A does not grant access to group B (via refresh cookie).
func TestProxydDavAuthFlow_DBCrossGroupDenied(t *testing.T) {
	st, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateAuthUser("local:carol", "carol", "", "Carol"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutACLRow(core.ACLRow{
		Principal: "local:carol",
		Action:    "*",
		Scope:     "my-folder",
		Effect:    "allow",
	}); err != nil {
		t.Fatal(err)
	}
	refreshTok := "carol-refresh"
	if err := st.CreateAuthSession(
		auth.HashToken(refreshTok), "local:carol", time.Now().Add(time.Hour),
	); err != nil {
		t.Fatal(err)
	}

	s, up := testDavFullServer(t, st, "testsecret")
	defer up.Close()

	req := httptest.NewRequest("GET", "/dav/other-folder/secret.txt", nil)
	req.AddCookie(&http.Cookie{Name: "refresh_token", Value: refreshTok})
	w := httptest.NewRecorder()
	s.route(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (no DB grant for other-folder)", w.Code)
	}
}

// Write restriction: PUT on .env is blocked even with valid auth + correct group.
func TestProxydDavAuthFlow_WriteBlockEnv(t *testing.T) {
	s, up := testDavFullServer(t, nil, "testsecret")
	defer up.Close()

	tok := testMintJWTWithGroups([]byte("testsecret"), "user:alice", []string{"proj"})
	req := httptest.NewRequest("PUT", "/dav/proj/.env", strings.NewReader("SECRET=x"))
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.route(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (write to .env blocked)", w.Code)
	}
}

// Write restriction: DELETE on .git/config is blocked.
func TestProxydDavAuthFlow_WriteBlockGit(t *testing.T) {
	s, up := testDavFullServer(t, nil, "testsecret")
	defer up.Close()

	tok := testMintJWTWithGroups([]byte("testsecret"), "user:alice", []string{"proj"})
	req := httptest.NewRequest("DELETE", "/dav/proj/.git/config", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.route(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (write to .git/** blocked)", w.Code)
	}
}

// Write restriction: MOVE on *.pem is blocked.
func TestProxydDavAuthFlow_WriteBlockPEM(t *testing.T) {
	s, up := testDavFullServer(t, nil, "testsecret")
	defer up.Close()

	tok := testMintJWTWithGroups([]byte("testsecret"), "user:alice", []string{"proj"})
	req := httptest.NewRequest("MOVE", "/dav/proj/keys/server.pem", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Destination", "/dav/proj/keys/old.pem")
	w := httptest.NewRecorder()
	s.route(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (write to *.pem blocked)", w.Code)
	}
}

// Write restriction: PUT to logs/** is blocked (read-only).
func TestProxydDavAuthFlow_WriteBlockLogs(t *testing.T) {
	s, up := testDavFullServer(t, nil, "testsecret")
	defer up.Close()

	tok := testMintJWTWithGroups([]byte("testsecret"), "user:alice", []string{"proj"})
	req := httptest.NewRequest("PUT", "/dav/proj/logs/app.log", strings.NewReader("data"))
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.route(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (logs/** is read-only)", w.Code)
	}
}

// Ordinary writes outside sensitive paths succeed.
func TestProxydDavAuthFlow_WriteAllowed(t *testing.T) {
	s, up := testDavFullServer(t, nil, "testsecret")
	defer up.Close()

	tok := testMintJWTWithGroups([]byte("testsecret"), "user:alice", []string{"proj"})
	req := httptest.NewRequest("PUT", "/dav/proj/docs/readme.md", strings.NewReader("# Hello"))
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.route(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200 (ordinary write allowed)", w.Code)
	}
}

// Path rewrite integration: dufs backend receives /<group>/<rest>, not /dav/<group>/<rest>.
func TestProxydDavAuthFlow_PathRewrite(t *testing.T) {
	s, up := testDavFullServer(t, nil, "testsecret")
	defer up.Close()

	tok := testMintJWTWithGroups([]byte("testsecret"), "user:alice", []string{"workspace"})
	req := httptest.NewRequest("GET", "/dav/workspace/deep/path/file.txt", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.route(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("X-Upstream-Path"); got != "/workspace/deep/path/file.txt" {
		t.Errorf("backend received path %q, want /workspace/deep/path/file.txt", got)
	}
}

// Path traversal in /dav/* is rejected early.
func TestProxydDavAuthFlow_TraversalRejected(t *testing.T) {
	s, up := testDavFullServer(t, nil, "testsecret")
	defer up.Close()

	tok := testMintJWTWithGroups([]byte("testsecret"), "user:alice", []string{"proj"})
	for _, path := range []string{"/dav/proj/../other/file", "/dav/proj/%2e%2e/other"} {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		s.route(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("path %q: status = %d, want 400 (traversal rejected)", path, w.Code)
		}
	}
}

package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"testing"
	"time"

	"github.com/onvos/arizuko/auth"
	"github.com/onvos/arizuko/store"
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
		cfg:     config{authSecret: ""},
		st:      nil,
		vh:      &vhosts{entries: map[string]string{}},
		slinkRL: newRateLimiter(10, time.Minute),
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
	s := &server{cfg: config{authSecret: "rawsecret"}, slinkRL: newRateLimiter(10, time.Minute)}
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
	s := &server{cfg: config{authSecret: "testsecret"}, slinkRL: newRateLimiter(10, time.Minute)}
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
	s := &server{cfg: config{authSecret: "testsecret"}, slinkRL: newRateLimiter(10, time.Minute)}
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
	s := &server{cfg: config{authSecret: "testsecret"}, slinkRL: newRateLimiter(10, time.Minute)}
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
	s := &server{cfg: config{authSecret: "testsecret"}, slinkRL: newRateLimiter(10, time.Minute)}
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

func TestProxydSlinkRouteRateLimit(t *testing.T) {
	s := &server{
		cfg:     config{},
		vh:      &vhosts{entries: map[string]string{}},
		slinkRL: newRateLimiter(1, time.Minute),
	}
	req1 := httptest.NewRequest("GET", "/slink/tok1", nil)
	req1.RemoteAddr = "9.9.9.9:1234"
	// First hit consumes the only slot; it would proceed to upstream (nil),
	// which would panic. Pre-fill the limiter bucket instead so the second
	// request is denied before touching upstream.
	s.slinkRL.allow("9.9.9.9")

	req := httptest.NewRequest("GET", "/slink/tok1", nil)
	req.RemoteAddr = "9.9.9.9:1234"
	w := httptest.NewRecorder()
	s.route(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", w.Code)
	}
}

func TestProxydDashNilProxy(t *testing.T) {
	s := testServer() // dashProxy is nil
	req := httptest.NewRequest("GET", "/dash/status/", nil)
	w := httptest.NewRecorder()
	s.route(w, req)
	if w.Code != 404 {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestProxydVhostsRewrite(t *testing.T) {
	vh := &vhosts{entries: map[string]string{"test.example.com": "myworld"}}
	// vhost rewrite serves via vite proxy; with no proxy configured → 404
	s := &server{cfg: config{}, vh: vh, slinkRL: newRateLimiter(10, time.Minute)}
	req := httptest.NewRequest("GET", "/some/path", nil)
	req.Host = "test.example.com"
	w := httptest.NewRecorder()
	s.route(w, req)
	if w.Code != 404 {
		t.Errorf("status = %d, want 404 (no vite proxy)", w.Code)
	}
}

func TestProxydVhostsPathTraversal(t *testing.T) {
	vh := &vhosts{entries: map[string]string{"evil.com": "world"}}
	s := &server{cfg: config{}, vh: vh, slinkRL: newRateLimiter(10, time.Minute)}
	req := httptest.NewRequest("GET", "/../etc/passwd", nil)
	req.Host = "evil.com"
	w := httptest.NewRecorder()
	s.route(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
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

func TestVhostsMatch(t *testing.T) {
	vh := &vhosts{entries: map[string]string{
		"exact.com":  "exact-world",
		"*.wild.com": "wild-world",
	}}
	if w, ok := vh.match("exact.com"); !ok || w != "exact-world" {
		t.Errorf("exact match: got %q %v", w, ok)
	}
	if w, ok := vh.match("sub.wild.com"); !ok || w != "wild-world" {
		t.Errorf("wildcard match: got %q %v", w, ok)
	}
	if _, ok := vh.match("unknown.com"); ok {
		t.Error("unknown host should not match")
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
		cfg:       config{authSecret: "testsecret"},
		vh:        &vhosts{entries: map[string]string{}},
		viteProxy: httputil.NewSingleHostReverseProxy(u),
		slinkRL:   newRateLimiter(10, time.Minute),
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

func TestDavRouteForbidden(t *testing.T) {
	s := &server{
		cfg:      config{authSecret: "testsecret"},
		vh:       &vhosts{entries: map[string]string{}},
		slinkRL:  newRateLimiter(10, time.Minute),
		davProxy: nil, // 403 short-circuits before hitting the proxy
	}
	req := httptest.NewRequest("GET", "/dav/bob/", nil)
	req.Header.Set("X-User-Groups", `["alice"]`)
	w := httptest.NewRecorder()
	s.davRoute(w, req)

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
		cfg:     config{authSecret: "testsecret"},
		st:      st,
		slinkRL: newRateLimiter(10, time.Minute),
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
		cfg:     config{authSecret: "testsecret"},
		st:      st,
		slinkRL: newRateLimiter(10, time.Minute),
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

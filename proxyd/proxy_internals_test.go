package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// parseTrustedProxies handles CIDRs, bare IPs, and invalid entries.
func TestParseTrustedProxies(t *testing.T) {
	cases := []struct {
		in   string
		want int // number of parsed CIDRs
	}{
		{"", 0},
		{"10.0.0.1", 1},
		{"10.0.0.0/24,192.168.1.0/24", 2},
		{"::1", 1},                       // IPv6 → /128
		{"10.0.0.1,INVALID,10.0.0.2", 2}, // bad entry skipped
		{" 10.0.0.1 , 10.0.0.2 ", 2},     // whitespace trimmed
	}
	for _, c := range cases {
		got := parseTrustedProxies(c.in)
		if len(got) != c.want {
			t.Errorf("parseTrustedProxies(%q): len=%d, want %d", c.in, len(got), c.want)
		}
	}
}

// stripClientHeaders removes all proxyd-owned headers.
func TestStripClientHeaders(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	owned := []string{
		"X-User-Sub", "X-User-Name", "X-User-Groups", "X-User-Sig",
		"X-Folder", "X-Group-Name", "X-Chat-Token", "X-Chat-Sig",
	}
	for _, h := range owned {
		r.Header.Set(h, "evil-value")
	}
	stripClientHeaders(r)
	for _, h := range owned {
		if v := r.Header.Get(h); v != "" {
			t.Errorf("header %q not stripped: %q", h, v)
		}
	}
	// Unrelated headers should be untouched.
	r2 := httptest.NewRequest("GET", "/", nil)
	r2.Header.Set("Accept", "text/html")
	stripClientHeaders(r2)
	if r2.Header.Get("Accept") != "text/html" {
		t.Error("Accept header should not be stripped")
	}
}

// fixForwardedFor with an untrusted peer overwrites XFF with the connection peer.
func TestFixForwardedFor_Untrusted(t *testing.T) {
	s := testServer()
	// no trusted proxies configured
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "5.5.5.5:1234"
	r.Header.Set("X-Forwarded-For", "1.2.3.4") // attacker-supplied
	s.fixForwardedFor(r)
	if got := r.Header.Get("X-Forwarded-For"); got != "5.5.5.5" {
		t.Errorf("XFF = %q, want 5.5.5.5 (overwritten by peer)", got)
	}
}

// fixForwardedFor with a trusted peer trusts the leftmost XFF entry.
func TestFixForwardedFor_Trusted(t *testing.T) {
	s := testServer()
	s.cfg.trustedProxies = parseTrustedProxies("10.0.0.1")
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.1:1234"
	r.Header.Set("X-Forwarded-For", "1.2.3.4, 10.0.0.1") // chain from trusted proxy
	s.fixForwardedFor(r)
	if got := r.Header.Get("X-Forwarded-For"); got != "1.2.3.4" {
		t.Errorf("XFF = %q, want 1.2.3.4 (trusted proxy → accept leftmost)", got)
	}
}

// fixForwardedFor with empty RemoteAddr clears XFF.
func TestFixForwardedFor_EmptyPeer(t *testing.T) {
	s := testServer()
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = ""
	r.Header.Set("X-Forwarded-For", "1.2.3.4")
	s.fixForwardedFor(r)
	if got := r.Header.Get("X-Forwarded-For"); got != "" {
		t.Errorf("XFF = %q, want empty when RemoteAddr blank", got)
	}
}

// setUserHeaders stamps sub/name/groups/sig on a cloned request.
func TestSetUserHeaders(t *testing.T) {
	s := &server{cfg: config{hmacSecret: "test-secret"}}
	r := httptest.NewRequest("GET", "/", nil)
	r2 := s.setUserHeaders(r, "user:bob", "Bob", []string{"grp1", "grp2"})

	if r2.Header.Get("X-User-Sub") != "user:bob" {
		t.Errorf("X-User-Sub = %q", r2.Header.Get("X-User-Sub"))
	}
	if r2.Header.Get("X-User-Name") != "Bob" {
		t.Errorf("X-User-Name = %q", r2.Header.Get("X-User-Name"))
	}
	if groups := r2.Header.Get("X-User-Groups"); !strings.Contains(groups, "grp1") {
		t.Errorf("X-User-Groups = %q", groups)
	}
	if sig := r2.Header.Get("X-User-Sig"); sig == "" {
		t.Error("X-User-Sig not set")
	}
	// Original request must not be mutated.
	if r.Header.Get("X-User-Sub") != "" {
		t.Error("original request was mutated by setUserHeaders")
	}
}

// setUserHeaders with empty hmacSecret omits X-User-Sig.
func TestSetUserHeaders_NoSecret(t *testing.T) {
	s := &server{cfg: config{hmacSecret: ""}}
	r := httptest.NewRequest("GET", "/", nil)
	r2 := s.setUserHeaders(r, "sub", "name", nil)
	if r2.Header.Get("X-User-Sig") != "" {
		t.Error("X-User-Sig should be empty when hmacSecret is unset")
	}
}

// jidFolder (proxyd-local copy) mirrors the webd helper.
func TestProxydJIDFolder(t *testing.T) {
	cases := []struct {
		jid  string
		want string
	}{
		{"web:atlas", "atlas"},
		{"web:atlas/content", "atlas/content"},
		{"hook:atlas/telegram", "atlas"},
		{"hook:atlas/eng/slack", "atlas/eng"},
		{"hook:bare", "bare"},
		{"other:x", ""},
	}
	for _, c := range cases {
		got := jidFolder(c.jid)
		if got != c.want {
			t.Errorf("jidFolder(%q) = %q, want %q", c.jid, got, c.want)
		}
	}
}

// tryAuth with a valid JWT returns a stamped request.
func TestTryAuth_ValidJWT(t *testing.T) {
	secret := []byte("testsecret")
	s := &server{cfg: config{authSecret: "testsecret"}}
	tok := testMintJWT(secret, "user:carol")
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	r2 := s.tryAuth(r)
	if r2 == nil {
		t.Fatal("tryAuth returned nil for valid JWT")
	}
	if r2.Header.Get("X-User-Sub") != "user:carol" {
		t.Errorf("X-User-Sub = %q", r2.Header.Get("X-User-Sub"))
	}
}

// tryAuth with no credentials returns nil.
func TestTryAuth_NoCredentials(t *testing.T) {
	s := &server{cfg: config{authSecret: "secret"}}
	r := httptest.NewRequest("GET", "/", nil)
	if s.tryAuth(r) != nil {
		t.Error("tryAuth without credentials should return nil")
	}
}

// tryAuth with expired JWT returns nil.
func TestTryAuth_ExpiredJWT(t *testing.T) {
	secret := []byte("testsecret")
	s := &server{cfg: config{authSecret: "testsecret"}}
	tok := testMintExpiredJWT(secret, "user:expired")
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	if s.tryAuth(r) != nil {
		t.Error("tryAuth with expired JWT should return nil")
	}
}

// /auth/* path without credentials bounces to /auth/login.
func TestProxydAuthStarGated(t *testing.T) {
	s, up := testServerWithUpstream(t)
	defer up.Close()

	req := httptest.NewRequest("GET", "/auth/google/callback?code=abc&state=s", nil)
	w := httptest.NewRecorder()
	s.route(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/auth/login" {
		t.Errorf("Location = %q, want /auth/login", loc)
	}
}

// requireAuth sets auth_return cookie with the target path for redirect.
func TestRequireAuth_SetsReturnCookie(t *testing.T) {
	s := &server{cfg: config{authSecret: "testsecret"}, chatAnonDOS: newRateLimiter(10, time.Minute)}
	h := s.requireAuth(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	req := httptest.NewRequest("GET", "/dash/status?x=1", nil)
	w := httptest.NewRecorder()
	h(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", w.Code)
	}
	var found bool
	for _, c := range w.Result().Cookies() {
		if c.Name == "auth_return" && c.Value == "/dash/status?x=1" {
			found = true
		}
	}
	if !found {
		t.Error("auth_return cookie not set with correct target path")
	}
}

// requireAuth does not set auth_return cookie for /auth/* paths.
func TestRequireAuth_NoReturnCookieForAuthPaths(t *testing.T) {
	s := &server{cfg: config{authSecret: "testsecret"}, chatAnonDOS: newRateLimiter(10, time.Minute)}
	h := s.requireAuth(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	req := httptest.NewRequest("GET", "/auth/logout", nil)
	w := httptest.NewRecorder()
	h(w, req)
	for _, c := range w.Result().Cookies() {
		if c.Name == "auth_return" {
			t.Errorf("auth_return cookie should not be set for /auth/ paths, got %q", c.Value)
		}
	}
}

// parseTrustedProxies: IPv6 bare address gets /128.
func TestParseTrustedProxies_IPv6(t *testing.T) {
	nets := parseTrustedProxies("::1")
	if len(nets) != 1 {
		t.Fatalf("expected 1 net, got %d", len(nets))
	}
	ones, _ := nets[0].Mask.Size()
	if ones != 128 {
		t.Errorf("mask = /%d, want /128", ones)
	}
}

// matchWebRoute: empty routes slice returns false.
func TestMatchWebRoute_Empty(t *testing.T) {
	_, ok := matchWebRoute(nil, "/pub/foo")
	if ok {
		t.Error("empty routes should return false")
	}
}

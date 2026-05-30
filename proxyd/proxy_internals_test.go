package main

import (
	"crypto/ecdsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
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

// es256KeySet mints a local signing key + matching KeySet for ES256 soak tests.
func es256KeySet(t *testing.T) (*auth.SigningKey, *auth.KeySet) {
	t.Helper()
	k, err := auth.NewSigningKey("k1")
	if err != nil {
		t.Fatal(err)
	}
	return k, auth.NewKeySet(map[string]*ecdsa.PublicKey{k.Kid: &k.Priv.PublicKey})
}

// Soak step 1: an ES256 bearer minted by authd's key is accepted alongside
// HS256, stamping the same X-User-* headers (X-User-Sig still present).
func TestTryAuth_ES256Bearer(t *testing.T) {
	k, ks := es256KeySet(t)
	s := &server{cfg: config{authSecret: "hs256secret", hmacSecret: "hmac", authdURL: "http://authd"}, ks: ks}
	tok, err := k.Sign(auth.TokenClaims{Sub: "user:google:123", Typ: "user", Scope: []string{"messages:send"}}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	r2 := s.tryAuth(r)
	if r2 == nil {
		t.Fatal("tryAuth returned nil for valid ES256 bearer")
	}
	if got := r2.Header.Get("X-User-Sub"); got != "user:google:123" {
		t.Errorf("X-User-Sub = %q", got)
	}
	if r2.Header.Get("X-User-Sig") == "" {
		t.Error("X-User-Sig must still be injected for backends during soak")
	}
}

// HS256 bearer keeps working with the ES256 KeySet present (additive).
func TestTryAuth_HS256StillWorksWithKeySet(t *testing.T) {
	_, ks := es256KeySet(t)
	s := &server{cfg: config{authSecret: "testsecret", authdURL: "http://authd"}, ks: ks}
	tok := testMintJWT([]byte("testsecret"), "user:hsbob")
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	r2 := s.tryAuth(r)
	if r2 == nil || r2.Header.Get("X-User-Sub") != "user:hsbob" {
		t.Fatalf("HS256 bearer not accepted alongside ES256 KeySet: %v", r2)
	}
}

// A garbage bearer is rejected on both paths.
func TestTryAuth_ES256Invalid(t *testing.T) {
	_, ks := es256KeySet(t)
	s := &server{cfg: config{authSecret: "secret", authdURL: "http://authd"}, ks: ks}
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer not.a.jwt")
	if s.tryAuth(r) != nil {
		t.Error("invalid bearer must be rejected")
	}
}

// AUTHD_URL unset → ks nil → the ES256 path is absent: a valid ES256 bearer is
// rejected, exactly the pre-soak HS256-only behavior.
func TestTryAuth_ES256RejectedWhenKeySetNil(t *testing.T) {
	k, _ := es256KeySet(t)
	s := &server{cfg: config{authSecret: "hs256secret"}} // ks: nil, authdURL: ""
	tok, err := k.Sign(auth.TokenClaims{Sub: "user:google:123", Typ: "user"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	if s.tryAuth(r) != nil {
		t.Error("ES256 bearer must be rejected when AUTHD_URL unset (HS256-only)")
	}
}

// Soak step 2: ES256 sub `user:google:123` resolves grants under the bare
// `google:123` principal — the leading `user:` is stripped before lookup.
func TestTryAuth_ES256SubPrefixGrantLookup(t *testing.T) {
	st, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.AddACLRow(core.ACLRow{
		Principal: "google:123", Action: "*", Scope: "atlas", Effect: "allow",
	}); err != nil {
		t.Fatal(err)
	}
	k, ks := es256KeySet(t)
	s := &server{cfg: config{authSecret: "hs", hmacSecret: "h", authdURL: "http://authd"}, st: st, ks: ks}
	tok, err := k.Sign(auth.TokenClaims{Sub: "user:google:123", Typ: "user"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	r2 := s.tryAuth(r)
	if r2 == nil {
		t.Fatal("tryAuth nil for ES256 bearer with grants")
	}
	var groups []string
	_ = json.Unmarshal([]byte(r2.Header.Get("X-User-Groups")), &groups)
	found := false
	for _, g := range groups {
		if g == "atlas" {
			found = true
		}
	}
	if !found {
		t.Errorf("X-User-Groups = %q; want grant for bare sub google:123 (atlas)", groups)
	}
}

// Cutover flip-blocker: an ES256-authenticated operator must get the operator
// `**` grant injected into X-User-Groups from the DB — exactly as the HS256
// path does. proxyd stays the grant-injector at flip; the ES256 path must NOT
// stamp only the token claim. Guards that backends (webd/onbod/dashd reading
// injected headers) see operator scope for ES256 sessions too.
func TestTryAuth_ES256OperatorGroupsFromDB(t *testing.T) {
	st, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	// Operator grant: bare sub `google:999` holds `**` (cross-folder operator).
	if err := st.AddACLRow(core.ACLRow{
		Principal: "google:999", Action: "*", Scope: "**", Effect: "allow",
	}); err != nil {
		t.Fatal(err)
	}
	k, ks := es256KeySet(t)
	s := &server{cfg: config{authSecret: "hs", hmacSecret: "h", authdURL: "http://authd"}, st: st, ks: ks}
	// ES256 token carries NO groups claim — proxyd must source them from the DB.
	tok, err := k.Sign(auth.TokenClaims{Sub: "user:google:999", Typ: "user"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	r2 := s.tryAuth(r)
	if r2 == nil {
		t.Fatal("tryAuth nil for ES256 operator bearer")
	}
	var groups []string
	if err := json.Unmarshal([]byte(r2.Header.Get("X-User-Groups")), &groups); err != nil {
		t.Fatalf("X-User-Groups not valid JSON: %v", err)
	}
	found := false
	for _, g := range groups {
		if g == "**" {
			found = true
		}
	}
	if !found {
		t.Errorf("X-User-Groups = %q; want operator ** for ES256 sub user:google:999", groups)
	}
	if r2.Header.Get("X-User-Sig") == "" {
		t.Error("X-User-Sig must be injected so backends trust the operator groups")
	}
}

// Soak step 6: with AUTHD_URL set, /auth/login 302s to authd; query preserved.
func TestRedirectAuthToAuthd(t *testing.T) {
	s := &server{cfg: config{authdURL: "https://authd.example"}}
	r := httptest.NewRequest("GET", "/auth/login?return=/me", nil)
	w := httptest.NewRecorder()
	s.redirectAuthToAuthd(w, r)
	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "https://authd.example/auth/login?return=/me" {
		t.Errorf("Location = %q", loc)
	}
}

// With AUTHD_URL unset, the handler serves the local HS256 OAuth flow
// (RegisterRoutes) — /auth/login returns the login page, not a redirect to authd.
func TestHandler_AuthLocalWhenAuthdUnset(t *testing.T) {
	st, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := &server{cfg: config{authSecret: "s"}, st: st, rr: newRoutesResource(st, nil)}
	h := s.handler(&core.Config{AuthSecret: "s"}, nil)
	r := httptest.NewRequest("GET", "/auth/login", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code == http.StatusFound {
		t.Errorf("with AUTHD_URL unset, /auth/login must not 302 to authd (got %d)", w.Code)
	}
}

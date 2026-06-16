package main

import (
	"crypto/ecdsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
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

// setUserHeaders stamps sub/name/groups on a cloned request and never signs
// X-User-Sig — the service bearer is the channel proof. With no service token
// (svc nil) it forwards the identity unsigned.
func TestSetUserHeaders(t *testing.T) {
	s := &server{cfg: config{}}
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
	if sig := r2.Header.Get("X-User-Sig"); sig != "" {
		t.Errorf("X-User-Sig must not be set post-flip: got %q", sig)
	}
	// Original request must not be mutated.
	if r.Header.Get("X-User-Sub") != "" {
		t.Error("original request was mutated by setUserHeaders")
	}
}

// setUserHeaders strips any stale inbound X-User-Sig (it is never trusted).
func TestSetUserHeaders_StripsStaleSig(t *testing.T) {
	s := &server{cfg: config{}}
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("X-User-Sig", "stale")
	r2 := s.setUserHeaders(r, "sub", "name", nil)
	if r2.Header.Get("X-User-Sig") != "" {
		t.Errorf("stale X-User-Sig not stripped: got %q", r2.Header.Get("X-User-Sig"))
	}
}

// With no service token, setUserHeaders must clear the caller's inbound bearer
// so the request is forwarded truly UNSIGNED — never re-derivable from the
// caller's own token by a backend's transit gate.
func TestSetUserHeaders_ClearsInboundBearer_NoService(t *testing.T) {
	s := &server{cfg: config{}} // svc nil
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer user-token")
	r2 := s.setUserHeaders(r, "github:42", "Bob", []string{"**"})
	if got := r2.Header.Get("Authorization"); got != "" {
		t.Errorf("inbound bearer not cleared with no service token: got %q", got)
	}
}

// fakeServiceAuthd serves /v1/service-token minting a service:<daemon> JWT and
// returns the KeySet that verifies it.
func fakeServiceAuthd(t *testing.T) (*httptest.Server, *auth.KeySet) {
	t.Helper()
	key, err := auth.NewSigningKey("kid-proxyd")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/service-token", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Daemon string `json:"daemon"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		tok, err := key.Sign(auth.TokenClaims{Sub: "service:" + req.Daemon, Typ: "service"}, time.Hour)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"token": tok})
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, auth.NewKeySet(map[string]*ecdsa.PublicKey{key.Kid: &key.Priv.PublicKey})
}

// With a service-token source wired, setUserHeaders attaches a verifiable
// service:proxyd bearer (the channel proof backends verify) and NO X-User-Sig.
func TestSetUserHeaders_AttachesServiceBearer(t *testing.T) {
	authd, ks := fakeServiceAuthd(t)
	src, err := auth.ServiceToken(authd.URL, "proxyd", "boot-proxyd")
	if err != nil {
		t.Fatal(err)
	}
	s := &server{cfg: config{}, svc: src, ks: ks}
	r := httptest.NewRequest("GET", "/", nil)
	r2 := s.setUserHeaders(r, "github:42", "Bob", []string{"**"})

	if r2.Header.Get("X-User-Sig") != "" {
		t.Error("X-User-Sig must not be set when presenting a service bearer")
	}
	bearer := strings.TrimPrefix(r2.Header.Get("Authorization"), "Bearer ")
	if bearer == "" {
		t.Fatal("no service bearer attached")
	}
	sub, err := auth.VerifyToken(bearer, ks)
	if err != nil || sub.Sub != "service:proxyd" {
		t.Fatalf("service bearer must verify as service:proxyd: sub=%q err=%v", sub.Sub, err)
	}
}

// channelTrusted gates proxyd's /v1/routes resource (webd→proxyd): it admits a
// stamped X-User-Sub ONLY with a valid service:webd bearer, rejects any other
// bearer (the sub pin), and is open when ks is nil (local dev).
func TestChannelTrusted(t *testing.T) {
	key, err := auth.NewSigningKey("k1")
	if err != nil {
		t.Fatal(err)
	}
	ks := auth.NewKeySet(map[string]*ecdsa.PublicKey{"k1": &key.Priv.PublicKey})

	// Forged X-User-Sub, no bearer → rejected.
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("X-User-Sub", "github:hacker")
	if channelTrusted(r, ks) {
		t.Error("admitted a forged X-User-Sub with no channel proof")
	}

	// Valid service:webd bearer → admitted.
	webdTok, _ := key.Sign(auth.TokenClaims{Sub: callerWebd, Typ: "service"}, time.Minute)
	r2 := httptest.NewRequest("GET", "/", nil)
	r2.Header.Set("X-User-Sub", "github:42")
	r2.Header.Set("Authorization", "Bearer "+webdTok)
	if !channelTrusted(r2, ks) {
		t.Error("rejected a stamped identity proven by a valid service:webd bearer")
	}

	// Valid but NON-webd bearer (same trusted key) → rejected by the sub pin.
	userTok, _ := key.Sign(auth.TokenClaims{Sub: "user:github:mallory", Typ: "user"}, time.Minute)
	r3 := httptest.NewRequest("GET", "/", nil)
	r3.Header.Set("X-User-Sub", "github:victim")
	r3.Header.Set("Authorization", "Bearer "+userTok)
	if channelTrusted(r3, ks) {
		t.Error("admitted a forged identity under a non-webd bearer (sub pin missing)")
	}

	// nil ks (local dev) → open: a present X-User-Sub is trusted.
	r4 := httptest.NewRequest("GET", "/", nil)
	r4.Header.Set("X-User-Sub", "github:dev")
	if !channelTrusted(r4, nil) {
		t.Error("nil ks must trust a present X-User-Sub (local dev)")
	}
}

// proxyd stamps X-Folder via the canonical groupfolder.JidFolder.
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
		got := groupfolder.JidFolder(c.jid)
		if got != c.want {
			t.Errorf("JidFolder(%q) = %q, want %q", c.jid, got, c.want)
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

// /priv/<folder>/ requires the caller to hold a grant covering the folder.
func TestPriv_FolderScopedGrant(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer backend.Close()
	bu, _ := url.Parse(backend.URL)
	secret := []byte("testsecret")

	s := &server{
		cfg:         config{authSecret: string(secret)},
		chatAnonDOS: newRateLimiter(10, time.Minute),
		viteProxy:   httputil.NewSingleHostReverseProxy(bu),
	}

	cases := []struct {
		name   string
		path   string
		groups []string
		want   int
	}{
		{"own folder", "/priv/atlas/file.html", []string{"atlas"}, 200},
		{"operator", "/priv/atlas/file.html", []string{"**"}, 200},
		{"parent covers child", "/priv/atlas/search/page.html", []string{"atlas"}, 200},
		{"wrong folder", "/priv/atlas/file.html", []string{"research"}, 403},
		{"no groups", "/priv/atlas/file.html", []string{}, 403},
		{"bare priv no folder", "/priv", []string{}, 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tok := testMintJWTGroups(secret, "user:1", tc.groups)
			req := httptest.NewRequest("GET", tc.path, nil)
			req.Header.Set("Authorization", "Bearer "+tok)
			w := httptest.NewRecorder()
			s.route(w, req)
			if w.Code != tc.want {
				t.Errorf("path=%q groups=%v: status=%d want=%d", tc.path, tc.groups, w.Code, tc.want)
			}
		})
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

// An ES256 bearer minted by authd's key is accepted alongside HS256, stamping
// the same X-User-* headers. Post-flip (HMAC retire step 2) NO X-User-Sig is
// injected — the bearer (or, when wired, proxyd's service token) is the proof.
func TestTryAuth_ES256Bearer(t *testing.T) {
	k, ks := es256KeySet(t)
	s := &server{cfg: config{authSecret: "hs256secret", authdURL: "http://authd"}, ks: ks}
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
	if r2.Header.Get("X-User-Sig") != "" {
		t.Error("X-User-Sig must NOT be injected post-flip")
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
	s := &server{cfg: config{authSecret: "hs", authdURL: "http://authd"}, st: st, stRoutd: st, ks: ks}
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
	s := &server{cfg: config{authSecret: "hs", authdURL: "http://authd"}, st: st, stRoutd: st, ks: ks}
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
	if r2.Header.Get("X-User-Sig") != "" {
		t.Error("X-User-Sig must NOT be injected post-flip; the bearer is the channel proof")
	}
}

// Split-read proof: scopes must come from stRoutd (routd.db), not st (messages.db).
// A grant in stRoutd but absent from st must still appear in X-User-Groups.
// This guards against the frozen-messages.db bug where post-cutover grants are
// invisible to scope stamping.
func TestGroupsForSub_ReadsFromRoutdNotMessages(t *testing.T) {
	// stMessages simulates the frozen messages.db — no grants.
	stMessages, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stMessages.Close() })

	// stRoutd simulates the live routd.db — has grants.
	stRoutd, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stRoutd.Close() })
	if err := stRoutd.PutACLRow(core.ACLRow{
		Principal: "google:split", Action: "*", Scope: "new-grant", Effect: "allow",
	}); err != nil {
		t.Fatal(err)
	}

	k, ks := es256KeySet(t)
	s := &server{
		cfg:     config{authSecret: "hs", authdURL: "http://authd"},
		st:      stMessages, // messages.db has no grants
		stRoutd: stRoutd,    // routd.db has the grant
		ks:      ks,
	}
	tok, err := k.Sign(auth.TokenClaims{Sub: "user:google:split", Typ: "user"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	r2 := s.tryAuth(r)
	if r2 == nil {
		t.Fatal("tryAuth nil for ES256 bearer with grant in routd.db")
	}
	var groups []string
	_ = json.Unmarshal([]byte(r2.Header.Get("X-User-Groups")), &groups)
	found := false
	for _, g := range groups {
		if g == "new-grant" {
			found = true
		}
	}
	if !found {
		t.Errorf("X-User-Groups = %q; want new-grant from routd.db (not frozen messages.db)", groups)
	}
}

// handleAuth proxies to authd when authdProxy is set; 404 when unset (local dev).
func TestHandleAuth(t *testing.T) {
	// With authdProxy set: request is proxied, not redirected.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Proxied-Path", r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	s := &server{authdProxy: proxy(backend.URL)}
	r := httptest.NewRequest("GET", "/auth/login?return=/me", nil)
	w := httptest.NewRecorder()
	s.handleAuth(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("X-Proxied-Path"); got != "/auth/login" {
		t.Errorf("proxied path = %q, want /auth/login", got)
	}

	// Without authdProxy (local dev): 404.
	s2 := &server{}
	w2 := httptest.NewRecorder()
	s2.handleAuth(w2, httptest.NewRequest("GET", "/auth/login", nil))
	if w2.Code != http.StatusNotFound {
		t.Fatalf("no proxy: status = %d, want 404", w2.Code)
	}
}

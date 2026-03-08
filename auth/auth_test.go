package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/onvos/arizuko/store"
	argon2id "golang.org/x/crypto/argon2"
)

func sha256sum(b []byte) [32]byte { return sha256.Sum256(b) }

func hmacSHA256(key, msg []byte) string {
	h := hmac.New(sha256.New, key)
	h.Write(msg)
	return hex.EncodeToString(h.Sum(nil))
}

func hashArgon2(password string, salt []byte) string {
	derived := argon2id.IDKey([]byte(password), salt, 3, 65536, 4, 32)
	s := base64.RawStdEncoding.EncodeToString(salt)
	h := base64.RawStdEncoding.EncodeToString(derived)
	return fmt.Sprintf("$argon2id$v=19$m=65536,t=3,p=4$%s$%s", s, h)
}

var testSecret = []byte("test-secret-key-for-testing-only")

func TestJWTRoundTrip(t *testing.T) {
	token := MintJWT(testSecret, "user1", "Test User", time.Hour)
	claims, err := VerifyJWT(testSecret, token)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Sub != "user1" {
		t.Fatalf("got sub=%q, want user1", claims.Sub)
	}
	if claims.Name != "Test User" {
		t.Fatalf("got name=%q, want Test User", claims.Name)
	}
}

func TestJWTExpired(t *testing.T) {
	token := MintJWT(testSecret, "user1", "Test", -time.Hour)
	_, err := VerifyJWT(testSecret, token)
	if err != ErrExpiredToken {
		t.Fatalf("got err=%v, want ErrExpiredToken", err)
	}
}

func TestJWTBadSignature(t *testing.T) {
	token := MintJWT(testSecret, "user1", "Test", time.Hour)
	_, err := VerifyJWT([]byte("wrong"), token)
	if err != ErrInvalidToken {
		t.Fatalf("got err=%v, want ErrInvalidToken", err)
	}
}

func TestMiddlewarePublicPaths(t *testing.T) {
	h := Middleware(testSecret, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	for _, p := range []string{"/auth/login", "/pub/index.html", "/_REDACTED/api", "/favicon.ico", "/robots.txt", "/style.css"} {
		r := httptest.NewRequest("GET", p, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code == http.StatusSeeOther {
			t.Errorf("path %s was redirected, should be public", p)
		}
	}
}

func TestMiddlewareBlocksUnauthenticated(t *testing.T) {
	h := Middleware(testSecret, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	r := httptest.NewRequest("GET", "/dashboard", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("got %d, want 303 redirect", w.Code)
	}
}

func TestMiddlewarePassesWithJWT(t *testing.T) {
	h := Middleware(testSecret, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	token := MintJWT(testSecret, "user1", "Test", time.Hour)
	r := httptest.NewRequest("GET", "/dashboard", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("got %d, want 200", w.Code)
	}
}

func newTestServer(s *store.Store) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /auth/login", handleLoginPage)
	mux.HandleFunc("POST /auth/login", handleLogin(s, testSecret))
	mux.HandleFunc("POST /auth/refresh",
		handleRefresh(s, testSecret))
	mux.HandleFunc("POST /auth/logout", handleLogout(s))
	return httptest.NewServer(mux)
}

func seedUser(t *testing.T, s *store.Store) {
	t.Helper()
	h := hashArgon2("password123", []byte("testsalt"))
	if err := s.CreateAuthUser(
		"local:admin", "admin", h, "Admin",
	); err != nil {
		t.Fatal(err)
	}
}

func extractCookie(resp *http.Response, name string) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func TestLoginCycle(t *testing.T) {
	s, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedUser(t, s)

	srv := newTestServer(s)
	defer srv.Close()

	// GET login page
	resp, err := http.Get(srv.URL + "/auth/login")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("login page: got %d", resp.StatusCode)
	}

	// POST login with wrong password
	resp, err = http.PostForm(srv.URL+"/auth/login", url.Values{
		"username": {"admin"}, "password": {"wrong"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad login: got %d, want 401", resp.StatusCode)
	}
}

func TestFullLoginRefreshLogout(t *testing.T) {
	s, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedUser(t, s)

	srv := newTestServer(s)
	defer srv.Close()

	// no-redirect client to inspect responses
	cl := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// 1. login with correct password
	resp, err := cl.PostForm(srv.URL+"/auth/login", url.Values{
		"username": {"admin"}, "password": {"password123"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("login: got %d, want 200", resp.StatusCode)
	}
	rc := extractCookie(resp, cookieName)
	if rc == nil {
		t.Fatal("login: no refresh cookie")
	}

	// 2. refresh with that cookie
	req, _ := http.NewRequest("POST", srv.URL+"/auth/refresh", nil)
	req.AddCookie(rc)
	resp, err = cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("refresh: got %d, want 200", resp.StatusCode)
	}
	rc2 := extractCookie(resp, cookieName)
	if rc2 == nil {
		t.Fatal("refresh: no new cookie")
	}
	if rc2.Value == rc.Value {
		t.Fatal("refresh: cookie should rotate")
	}

	// old refresh token should be deleted (replay fails)
	req, _ = http.NewRequest("POST", srv.URL+"/auth/refresh", nil)
	req.AddCookie(rc) // old cookie
	resp, err = cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("replay: got %d, want 401", resp.StatusCode)
	}

	// 3. logout with new cookie
	req, _ = http.NewRequest("POST", srv.URL+"/auth/logout", nil)
	req.AddCookie(rc2)
	resp, err = cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("logout: got %d, want 303", resp.StatusCode)
	}
	cleared := extractCookie(resp, cookieName)
	if cleared == nil || cleared.MaxAge != -1 {
		t.Fatal("logout: cookie not cleared")
	}

	// refresh with logged-out cookie should fail
	req, _ = http.NewRequest("POST", srv.URL+"/auth/refresh", nil)
	req.AddCookie(rc2)
	resp, err = cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("post-logout refresh: got %d, want 401",
			resp.StatusCode)
	}
}

func TestMiddlewareExpiredJWT(t *testing.T) {
	h := Middleware(testSecret, http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
		}))
	token := MintJWT(testSecret, "u", "U", -time.Hour)
	r := httptest.NewRequest("GET", "/dashboard", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("expired jwt: got %d, want 303", w.Code)
	}
}

func TestMiddlewareMalformedAuth(t *testing.T) {
	h := Middleware(testSecret, http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
		}))

	cases := []struct {
		name string
		hdr  string
	}{
		{"no prefix", "Token abc123"},
		{"empty bearer", "Bearer "},
		{"basic auth", "Basic dXNlcjpwYXNz"},
	}
	for _, tc := range cases {
		r := httptest.NewRequest("GET", "/dashboard", nil)
		r.Header.Set("Authorization", tc.hdr)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusSeeOther {
			t.Errorf("%s: got %d, want 303", tc.name, w.Code)
		}
	}
}

func TestOAuthStateExpired(t *testing.T) {
	// create state with timestamp 11 minutes in the past
	ts := fmt.Sprintf("%d", time.Now().Add(-11*time.Minute).Unix())
	mac := hmacSHA256(testSecret, []byte(ts))
	state := ts + "." + mac

	r := httptest.NewRequest(
		"GET", "/callback?state="+url.QueryEscape(state), nil)
	r.AddCookie(&http.Cookie{Name: "oauth_state", Value: state})
	if verifyState(testSecret, r) {
		t.Fatal("expired state should not verify")
	}
}

func TestSplitArgon2EdgeCases(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"no dollars", "argon2id"},
		{"partial", "$argon2id$v=19$m=65536,t=3,p=4$salt"},
		{"wrong algo", "$bcrypt$v=19$m=65536,t=3,p=4$s$h"},
		{"no hash part", "$argon2id$v=19$m=65536,t=3,p=4$saltonly"},
	}
	for _, tc := range cases {
		if p := splitArgon2(tc.input); p != nil {
			t.Errorf("%s: expected nil, got %+v", tc.name, p)
		}
	}
}

func TestRefreshExpiredSession(t *testing.T) {
	s, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedUser(t, s)

	// create session with past expiry
	token := "expired-refresh-token"
	h := hashToken(token)
	past := time.Now().Add(-time.Hour)
	if err := s.CreateAuthSession(h, "local:admin", past); err != nil {
		t.Fatal(err)
	}

	srv := newTestServer(s)
	defer srv.Close()

	cl := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, _ := http.NewRequest("POST", srv.URL+"/auth/refresh", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: token})
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expired session: got %d, want 401",
			resp.StatusCode)
	}
}

func TestLogoutWithoutCookie(t *testing.T) {
	s, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	srv := newTestServer(s)
	defer srv.Close()

	cl := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, _ := http.NewRequest("POST", srv.URL+"/auth/logout", nil)
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("logout no cookie: got %d, want 303",
			resp.StatusCode)
	}
	cleared := extractCookie(resp, cookieName)
	if cleared == nil || cleared.MaxAge != -1 {
		t.Fatal("logout: cookie not cleared")
	}
}

func TestVerifyArgon2(t *testing.T) {
	hash := hashArgon2("password123", []byte("somesalt8"))
	if !verifyArgon2(hash, "password123") {
		t.Fatal("expected valid password to verify")
	}
	if verifyArgon2(hash, "wrongpassword") {
		t.Fatal("expected wrong password to fail")
	}
}

func TestOAuthStateCookie(t *testing.T) {
	state := signState(testSecret)
	if !strings.Contains(state, ".") {
		t.Fatal("state should contain timestamp.signature")
	}
	// simulate verification
	r := httptest.NewRequest("GET", "/callback?state="+url.QueryEscape(state), nil)
	r.AddCookie(&http.Cookie{Name: "oauth_state", Value: state})
	if !verifyState(testSecret, r) {
		t.Fatal("valid state should verify")
	}
}

func TestTelegramWidgetVerify(t *testing.T) {
	botToken := "123456:ABC-DEF"
	form := url.Values{
		"id":         {"12345"},
		"first_name": {"Test"},
		"auth_date":  {"1234567890"},
	}
	// compute valid hash
	check := "auth_date=1234567890\nfirst_name=Test\nid=12345"
	secret := sha256sum([]byte(botToken))
	h := hmacSHA256(secret[:], []byte(check))
	form.Set("hash", h)

	if !verifyTelegramWidget(form, botToken) {
		t.Fatal("valid telegram widget should verify")
	}
	form.Set("hash", "invalid")
	if verifyTelegramWidget(form, botToken) {
		t.Fatal("invalid hash should fail")
	}
}

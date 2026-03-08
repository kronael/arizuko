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

func TestLoginCycle(t *testing.T) {
	s, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	hash := hashArgon2("password123", []byte("testsalt"))
	if err := s.CreateAuthUser("local:admin", "admin", hash, "Admin"); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	secret := testSecret
	mux.HandleFunc("GET /auth/login", handleLoginPage)
	mux.HandleFunc("POST /auth/login", handleLogin(s, secret))
	mux.HandleFunc("POST /auth/refresh", handleRefresh(s, secret))
	mux.HandleFunc("POST /auth/logout", handleLogout(s))

	srv := httptest.NewServer(mux)
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

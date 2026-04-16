package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/onvos/arizuko/core"
	"github.com/onvos/arizuko/store"
)

func TestGoogleRedirect(t *testing.T) {
	cfg := &core.Config{GoogleClientID: "test-gid", WebHost: "example.com"}
	secret := []byte("testsecret")
	req := httptest.NewRequest("GET", "/auth/google", nil)
	rec := httptest.NewRecorder()
	handleGoogleRedirect(cfg, secret, false).ServeHTTP(rec, req)
	if rec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("want 307, got %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "accounts.google.com") {
		t.Errorf("expected google URL, got %s", loc)
	}
	if !strings.Contains(loc, "test-gid") {
		t.Errorf("expected client_id in URL, got %s", loc)
	}
}

func TestGoogleCallback_MissingCode(t *testing.T) {
	s, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	cfg := &core.Config{GoogleClientID: "gid", GoogleSecret: "gsec"}
	secret := []byte("testsecret")

	state := signState(secret)
	req := httptest.NewRequest("GET", "/auth/google/callback?state="+url.QueryEscape(state), nil)
	req.AddCookie(&http.Cookie{Name: "oauth_state", Value: state})
	rec := httptest.NewRecorder()
	handleGoogleCallback(cfg, s, secret, false).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestGoogleCallback_InvalidState(t *testing.T) {
	s, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	cfg := &core.Config{GoogleClientID: "gid", GoogleSecret: "gsec"}
	secret := []byte("testsecret")

	req := httptest.NewRequest("GET", "/auth/google/callback?state=bad&code=x", nil)
	req.AddCookie(&http.Cookie{Name: "oauth_state", Value: "different"})
	rec := httptest.NewRecorder()
	handleGoogleCallback(cfg, s, secret, false).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", rec.Code)
	}
}

func TestGoogleCallback_Success(t *testing.T) {
	s, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// fake token server
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"access_token": "tok-abc"})
	}))
	defer tokenSrv.Close()

	// fake userinfo server
	userinfoSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"sub": "12345", "name": "Test User", "email": "user@example.com"})
	}))
	defer userinfoSrv.Close()

	orig1 := googleTokenURL
	orig2 := googleUserinfoURL
	googleTokenURL = tokenSrv.URL
	googleUserinfoURL = userinfoSrv.URL
	defer func() {
		googleTokenURL = orig1
		googleUserinfoURL = orig2
	}()

	cfg := &core.Config{GoogleClientID: "gid", GoogleSecret: "gsec", WebHost: "example.com"}
	secret := []byte("testsecret")

	state := signState(secret)
	req := httptest.NewRequest("GET", "/auth/google/callback?state="+url.QueryEscape(state)+"&code=testcode", nil)
	req.AddCookie(&http.Cookie{Name: "oauth_state", Value: state})
	rec := httptest.NewRecorder()
	handleGoogleCallback(cfg, s, secret, false).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	// session cookie should be set
	found := false
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookieName {
			found = true
		}
	}
	if !found {
		t.Fatal("expected refresh_token cookie")
	}
	// user should be created with google: prefix
	if _, ok := s.AuthUserBySub("google:12345"); !ok {
		t.Fatal("expected google user to be created")
	}
}

func TestLoginPageGoogleButton(t *testing.T) {
	cfg := &core.Config{GoogleClientID: "gid"}
	req := httptest.NewRequest("GET", "/auth/login", nil)
	rec := httptest.NewRecorder()
	handleLoginPage(cfg).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "/auth/google") {
		t.Error("expected google auth link in login page")
	}
}

func TestGoogleAllowlistPass(t *testing.T) {
	if !matchEmailAllowlist("alice@REDACTED", "*@REDACTED,admin@example.com") {
		t.Fatal("expected wildcard match to pass")
	}
	if !matchEmailAllowlist("admin@example.com", "*@REDACTED,admin@example.com") {
		t.Fatal("expected exact match to pass")
	}
}

func TestGoogleAllowlistBlock(t *testing.T) {
	s, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"access_token": "tok-xyz"})
	}))
	defer tokenSrv.Close()

	userinfoSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{
			"sub": "99999", "name": "Bad User", "email": "hacker@evil.com",
		})
	}))
	defer userinfoSrv.Close()

	orig1 := googleTokenURL
	orig2 := googleUserinfoURL
	googleTokenURL = tokenSrv.URL
	googleUserinfoURL = userinfoSrv.URL
	defer func() {
		googleTokenURL = orig1
		googleUserinfoURL = orig2
	}()

	cfg := &core.Config{
		GoogleClientID:      "gid",
		GoogleSecret:        "gsec",
		WebHost:             "example.com",
		GoogleAllowedEmails: "*@REDACTED",
	}
	secret := []byte("testsecret")

	state := signState(secret)
	req := httptest.NewRequest("GET",
		"/auth/google/callback?state="+url.QueryEscape(state)+"&code=testcode", nil)
	req.AddCookie(&http.Cookie{Name: "oauth_state", Value: state})
	rec := httptest.NewRecorder()
	handleGoogleCallback(cfg, s, secret, false).ServeHTTP(rec, req)
	if rec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("want 307 redirect, got %d", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Location"), "error=unauthorized") {
		t.Fatalf("expected unauthorized in redirect, got %s", rec.Header().Get("Location"))
	}
}

func TestLoginPageNoGoogleButton(t *testing.T) {
	cfg := &core.Config{}
	req := httptest.NewRequest("GET", "/auth/login", nil)
	rec := httptest.NewRecorder()
	handleLoginPage(cfg).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "/auth/google") {
		t.Error("expected no google link when GoogleClientID is empty")
	}
}

package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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
	s := testServer()
	called := false
	h := s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	})
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	h(w, req)
	if !called {
		t.Error("handler not called when no secret")
	}
}

func TestProxydRequireAuthRawSecret(t *testing.T) {
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
	if !called {
		t.Error("handler not called with raw secret")
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

func TestProxydDashNilProxy(t *testing.T) {
	s := testServer() // dashProxy is nil
	req := httptest.NewRequest("GET", "/dash/status/", nil)
	w := httptest.NewRecorder()
	s.route(w, req)
	if w.Code != 404 {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestProxydVhostsRedirect(t *testing.T) {
	vh := &vhosts{entries: map[string]string{"test.example.com": "myworld"}}
	s := &server{cfg: config{}, vh: vh, slinkRL: newRateLimiter(10, time.Minute)}
	req := httptest.NewRequest("GET", "/some/path", nil)
	req.Host = "test.example.com"
	w := httptest.NewRecorder()
	s.route(w, req)
	if w.Code != http.StatusMovedPermanently {
		t.Errorf("status = %d, want 301", w.Code)
	}
	loc := w.Header().Get("Location")
	if loc != "/myworld/some/path" {
		t.Errorf("Location = %q, want /myworld/some/path", loc)
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

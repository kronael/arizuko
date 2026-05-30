package main

import (
	"crypto/ecdsa"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
)

// webd dual-accept (spec 5/1 § cutover): requireUser accepts EITHER the live
// HMAC X-User-Sig OR an authd-minted ES256 bearer when AUTHD_URL is set
// (KeySet non-nil). With AUTHD_URL unset (nil KeySet) it is HMAC-only.

func bearerServer(t *testing.T, ks *auth.KeySet) *server {
	t.Helper()
	cfg := config{assistantName: "assistant", hmacSecret: testHMACSecret}
	return newServer(cfg, nil, newHub(), nil, ks)
}

func mintBearer(t *testing.T) (*auth.KeySet, string) {
	t.Helper()
	k, err := auth.NewSigningKey("k1")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := k.Sign(auth.TokenClaims{
		Sub: "u_7", Typ: "user", Scope: []string{"chats:read"},
		Extra: map[string]string{"arz/folder": "atlas/main", "name": "alice"},
	}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return auth.NewKeySet(map[string]*ecdsa.PublicKey{"k1": &k.Priv.PublicKey}), tok
}

func TestWebdRequireUser_ES256Bearer_Accepted(t *testing.T) {
	ks, tok := mintBearer(t)
	s := bearerServer(t, ks)
	var seenSub string
	h := s.requireUser(func(w http.ResponseWriter, r *http.Request) {
		seenSub = userSub(r)
		w.WriteHeader(http.StatusOK)
	})
	r := httptest.NewRequest("GET", "/me/", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	h(w, r)
	if seenSub != "u_7" {
		t.Fatalf("ES256 bearer not accepted/stamped: seenSub=%q code=%d", seenSub, w.Code)
	}
}

func TestWebdRequireUser_HMAC_StillWorks_WithKeySet(t *testing.T) {
	ks, _ := mintBearer(t)
	s := bearerServer(t, ks)
	called := false
	h := s.requireUser(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})
	r := httptest.NewRequest("GET", "/me/", nil)
	for k, v := range signUserHeaders(map[string]string{
		"X-User-Sub": "github:42", "X-User-Name": "bob", "X-User-Groups": `["**"]`,
	}) {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h(w, r)
	if !called {
		t.Fatalf("HMAC sig rejected with ES256 KeySet present (code=%d)", w.Code)
	}
}

func TestWebdRequireUser_ES256Rejected_WhenKeySetNil(t *testing.T) {
	_, tok := mintBearer(t)
	s := bearerServer(t, nil) // AUTHD_URL unset → HMAC-only
	called := false
	h := s.requireUser(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})
	r := httptest.NewRequest("GET", "/me/", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	h(w, r)
	if called {
		t.Fatal("ES256 bearer accepted with nil KeySet (HMAC-only path broken)")
	}
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status: got %d want 303 redirect to login", w.Code)
	}
}

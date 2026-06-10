package main

import (
	"crypto/ecdsa"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
)

// webd identity gate (HMAC retire step 2): requireUser trusts the proxyd-stamped
// X-User-Sub when the channel is proven — EITHER a valid authd ES256 bearer
// (e.g. service:proxyd, when AUTHD_URL is set → KeySet non-nil) OR, with
// AUTHD_URL unset (nil KeySet), the legacy HMAC X-User-Sig. The bearer is the
// CHANNEL proof, never the identity: identity is the stamped header.

func bearerServer(t *testing.T, ks *auth.KeySet) *server {
	t.Helper()
	cfg := config{assistantName: "assistant", hmacSecret: testHMACSecret}
	return newServer(cfg, nil, newHub(), nil, ks, nil)
}

// channelBearer mints a service:proxyd token (the channel proof proxyd presents)
// and a KeySet that verifies it.
func channelBearer(t *testing.T) (*auth.KeySet, string) {
	t.Helper()
	k, err := auth.NewSigningKey("k1")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := k.Sign(auth.TokenClaims{Sub: "service:proxyd", Typ: "service"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return auth.NewKeySet(map[string]*ecdsa.PublicKey{"k1": &k.Priv.PublicKey}), tok
}

// TestWebdRequireUser_StampedIdentity_TrustedUnderBearer: proxyd stamps X-User-*
// and proves the channel with its service bearer; requireUser admits the request
// and the handler sees the STAMPED sub (not the bearer's service:proxyd sub).
func TestWebdRequireUser_StampedIdentity_TrustedUnderBearer(t *testing.T) {
	ks, tok := channelBearer(t)
	s := bearerServer(t, ks)
	var seenSub string
	h := s.requireUser(func(w http.ResponseWriter, r *http.Request) {
		seenSub = userSub(r)
		w.WriteHeader(http.StatusOK)
	})
	r := httptest.NewRequest("GET", "/me/", nil)
	r.Header.Set("X-User-Sub", "github:42")
	r.Header.Set("X-User-Name", "bob")
	r.Header.Set("X-User-Groups", `["**"]`)
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	h(w, r)
	if seenSub != "github:42" {
		t.Fatalf("stamped identity not trusted under channel bearer: seenSub=%q code=%d", seenSub, w.Code)
	}
}

// TestWebdRequireUser_NoChannelProof_Rejected: stamped X-User-Sub with neither a
// valid bearer nor an HMAC sig is rejected (a forged/unsigned header).
func TestWebdRequireUser_NoChannelProof_Rejected(t *testing.T) {
	ks, _ := channelBearer(t)
	s := bearerServer(t, ks)
	called := false
	h := s.requireUser(func(w http.ResponseWriter, r *http.Request) { called = true })
	r := httptest.NewRequest("GET", "/me/", nil)
	r.Header.Set("X-User-Sub", "github:hacker") // forged, no proof
	w := httptest.NewRecorder()
	h(w, r)
	if called {
		t.Fatal("unsigned/unproven X-User-Sub admitted")
	}
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status: got %d want 303 redirect to login", w.Code)
	}
}

func TestWebdRequireUser_HMAC_StillWorks_WithKeySet(t *testing.T) {
	ks, _ := channelBearer(t)
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

// TestWebdRequireUser_HMACFallback_WhenKeySetNil: with AUTHD_URL unset (nil
// KeySet, local dev) the HMAC X-User-Sig is the only channel proof, and an
// unsigned stamped header is rejected.
func TestWebdRequireUser_HMACFallback_WhenKeySetNil(t *testing.T) {
	s := bearerServer(t, nil)
	called := false
	h := s.requireUser(func(w http.ResponseWriter, r *http.Request) { called = true })
	r := httptest.NewRequest("GET", "/me/", nil)
	r.Header.Set("X-User-Sub", "github:hacker") // unsigned
	w := httptest.NewRecorder()
	h(w, r)
	if called {
		t.Fatal("unsigned X-User-Sub admitted with nil KeySet (HMAC-only path broken)")
	}
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status: got %d want 303 redirect to login", w.Code)
	}
}

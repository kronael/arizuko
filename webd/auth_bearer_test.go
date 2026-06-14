package main

import (
	"crypto/ecdsa"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
)

// webd identity gate: requireUser trusts the proxyd-stamped X-User-Sub when the
// channel is proven by a valid authd ES256 service:proxyd bearer (the CHANNEL
// proof, never the identity — identity is the stamped header). With AUTHD_URL
// unset (nil KeySet, local dev) there is no proxyd to forge through, so the
// stamped header is trusted.

func bearerServer(t *testing.T, ks *auth.KeySet) *server {
	t.Helper()
	cfg := config{assistantName: "assistant"}
	return newServer(cfg, nil, nil, newHub(), nil, ks, nil)
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

// TestWebdRequireUser_NoChannelProof_Rejected: a stamped X-User-Sub with no
// bearer is rejected (a forged/unproven header) when a KeySet is configured.
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
		t.Fatal("unproven X-User-Sub admitted")
	}
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status: got %d want 303 redirect to login", w.Code)
	}
}

// TestWebdRequireUser_NonProxydBearer_Rejected: a VALID authd token whose subject
// is NOT service:proxyd is not a transit proof — the forged X-User-Sub must be
// rejected. Signed by the SAME trusted key, so only the subject pin rejects it.
// This is the step-5 tightening: identified() previously admitted ANY valid bearer.
func TestWebdRequireUser_NonProxydBearer_Rejected(t *testing.T) {
	k, err := auth.NewSigningKey("k1")
	if err != nil {
		t.Fatal(err)
	}
	userTok, err := k.Sign(auth.TokenClaims{Sub: "user:github:mallory", Typ: "user"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ks := auth.NewKeySet(map[string]*ecdsa.PublicKey{"k1": &k.Priv.PublicKey})
	s := bearerServer(t, ks)
	called := false
	h := s.requireUser(func(w http.ResponseWriter, r *http.Request) { called = true })
	r := httptest.NewRequest("GET", "/me/", nil)
	r.Header.Set("Authorization", "Bearer "+userTok)
	r.Header.Set("X-User-Sub", "github:victim") // forged end-user identity
	w := httptest.NewRecorder()
	h(w, r)
	if called {
		t.Fatal("non-proxyd bearer admitted forged X-User-Sub (any authd token forges identity!)")
	}
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status: got %d want 303 redirect to login", w.Code)
	}
}

// TestWebdRequireUser_NilKeySet_TrustsStamp: with AUTHD_URL unset (nil KeySet,
// local dev) there is no proxyd to forge through and no JWKS to verify, so the
// stamped header is trusted — matching every other daemon's no-verifier path.
func TestWebdRequireUser_NilKeySet_TrustsStamp(t *testing.T) {
	s := bearerServer(t, nil)
	var seenSub string
	h := s.requireUser(func(w http.ResponseWriter, r *http.Request) { seenSub = userSub(r) })
	r := httptest.NewRequest("GET", "/me/", nil)
	r.Header.Set("X-User-Sub", "github:dev")
	w := httptest.NewRecorder()
	h(w, r)
	if seenSub != "github:dev" {
		t.Fatalf("local-dev stamped sub not trusted with nil KeySet: got %q (code=%d)", seenSub, w.Code)
	}
}

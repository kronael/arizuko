package auth

import (
	"crypto/ecdsa"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// bearerKeySet mints an ES256 token and returns a local KeySet that verifies
// it — the soak-time additive path (spec 5/1 § cutover).
func bearerKeySet(t *testing.T, c TokenClaims) (*KeySet, string) {
	t.Helper()
	k, err := NewSigningKey("k1")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := k.Sign(c, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ks := NewKeySet(map[string]*ecdsa.PublicKey{"k1": &k.Priv.PublicKey})
	return ks, tok
}

func TestRequireSignedOrBearer_NilKeySet_HMACOnly(t *testing.T) {
	// Unset AUTHD_URL → nil ks → behavior is exactly RequireSigned: an ES256
	// bearer is rejected (no ES256 path), an HMAC sig passes.
	ks, tok := bearerKeySet(t, TokenClaims{Sub: "u_7", Typ: "user", Scope: []string{"tasks:read"}})
	_ = ks

	called := false
	h := RequireSignedOrBearer(mwSecret, nil, nil)(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})
	r := httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	h(w, r)
	if called {
		t.Fatal("ES256 bearer accepted with nil KeySet (HMAC-only path broken)")
	}
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status: got %d want 303", w.Code)
	}

	// HMAC still works.
	called = false
	r = httptest.NewRequest("GET", "/x", nil)
	signedRequest(r, "github:42", "alice", `["**"]`)
	w = httptest.NewRecorder()
	h(w, r)
	if !called {
		t.Fatal("valid HMAC sig rejected with nil KeySet")
	}
}

func TestRequireSignedOrBearer_ES256_Accepted(t *testing.T) {
	ks, tok := bearerKeySet(t, TokenClaims{
		Sub: "u_7", Typ: "user", Scope: []string{"tasks:read"},
		Extra: map[string]string{"arz/folder": "atlas/main", "name": "alice"},
	})
	var seen *http.Request
	h := RequireSignedOrBearer(mwSecret, ks, nil)(func(w http.ResponseWriter, r *http.Request) {
		seen = r
	})
	r := httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	h(w, r)
	if seen == nil {
		t.Fatal("valid ES256 bearer rejected")
	}
	if seen.Header.Get("X-User-Sub") != "u_7" {
		t.Fatalf("X-User-Sub not stamped from ES256: got %q", seen.Header.Get("X-User-Sub"))
	}
	if seen.Header.Get("X-User-Name") != "alice" {
		t.Fatalf("X-User-Name not stamped from ES256: got %q", seen.Header.Get("X-User-Name"))
	}
	if g := seen.Header.Get("X-User-Groups"); g != `["atlas/main"]` {
		t.Fatalf("X-User-Groups not stamped from arz/folder: got %q", g)
	}
}

func TestRequireSignedOrBearer_HMAC_StillWorks_WithKeySet(t *testing.T) {
	ks, _ := bearerKeySet(t, TokenClaims{Sub: "u_7", Typ: "user"})
	var seen *http.Request
	h := RequireSignedOrBearer(mwSecret, ks, nil)(func(w http.ResponseWriter, r *http.Request) {
		seen = r
	})
	r := httptest.NewRequest("GET", "/x", nil)
	signedRequest(r, "github:42", "alice", `["**"]`)
	w := httptest.NewRecorder()
	h(w, r)
	if seen == nil {
		t.Fatal("valid HMAC sig rejected even with ES256 KeySet present")
	}
	if seen.Header.Get("X-User-Sub") != "github:42" {
		t.Fatalf("HMAC identity clobbered: got %q", seen.Header.Get("X-User-Sub"))
	}
}

func TestRequireSignedOrBearer_NoCred_Redirects(t *testing.T) {
	ks, _ := bearerKeySet(t, TokenClaims{Sub: "u_7", Typ: "user"})
	called := false
	h := RequireSignedOrBearer(mwSecret, ks, nil)(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})
	r := httptest.NewRequest("GET", "/x", nil) // no bearer, no sig
	w := httptest.NewRecorder()
	h(w, r)
	if called {
		t.Fatal("next called with no credential")
	}
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status: got %d want 303", w.Code)
	}
}

func TestStripUnsignedOrBearer_ES256_Accepted(t *testing.T) {
	ks, tok := bearerKeySet(t, TokenClaims{
		Sub: "u_7", Typ: "user",
		Extra: map[string]string{"arz/folder": "atlas/main"},
	})
	var seen *http.Request
	h := StripUnsignedOrBearer(mwSecret, ks, nil)(func(w http.ResponseWriter, r *http.Request) {
		seen = r
	})
	r := httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	h(w, r)
	if seen == nil {
		t.Fatal("valid ES256 bearer rejected")
	}
	if seen.Header.Get("X-User-Sub") != "u_7" {
		t.Fatalf("X-User-Sub not stamped: got %q", seen.Header.Get("X-User-Sub"))
	}
}

func TestStripUnsignedOrBearer_NilKeySet_StripsUnsigned(t *testing.T) {
	// nil ks → exactly StripUnsigned: an unsigned X-User-Sub is stripped, an
	// ES256 bearer is ignored.
	_, tok := bearerKeySet(t, TokenClaims{Sub: "u_7", Typ: "user"})
	var seen *http.Request
	h := StripUnsignedOrBearer(mwSecret, nil, nil)(func(w http.ResponseWriter, r *http.Request) {
		seen = r
	})
	r := httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("X-User-Sub", "github:hacker") // forged, unsigned
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	h(w, r)
	if seen == nil {
		t.Fatal("next not called")
	}
	if seen.Header.Get("X-User-Sub") != "" {
		t.Fatalf("unsigned X-User-Sub not stripped (nil ks must equal StripUnsigned): got %q",
			seen.Header.Get("X-User-Sub"))
	}
}

func TestStripUnsignedOrBearer_HMAC_StillWorks(t *testing.T) {
	ks, _ := bearerKeySet(t, TokenClaims{Sub: "u_7", Typ: "user"})
	var seen *http.Request
	h := StripUnsignedOrBearer(mwSecret, ks, nil)(func(w http.ResponseWriter, r *http.Request) {
		seen = r
	})
	r := httptest.NewRequest("GET", "/x", nil)
	signedRequest(r, "github:42", "alice", `["**"]`)
	w := httptest.NewRecorder()
	h(w, r)
	if seen == nil || seen.Header.Get("X-User-Sub") != "github:42" {
		t.Fatalf("valid HMAC sig path broken with KeySet present")
	}
}

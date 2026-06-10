package auth

import (
	"crypto/ecdsa"
	"net/http"
	"net/http/httptest"
	"strings"
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

// TestRequireSignedOrBearer_ES256_GrantsResolved is the production webd shape:
// grants != nil (st.UserScopes). The bearer's narrow arz/folder claim is
// IGNORED in favor of the resolver's full DB-sourced grant set — so an operator
// whose token only claims arz/folder=atlas/main still gets X-User-Groups=["**"].
// A regression here silently strips operator privileges (the `**` continuity
// path) in webd.
func TestRequireSignedOrBearer_ES256_GrantsResolved(t *testing.T) {
	ks, tok := bearerKeySet(t, TokenClaims{
		Sub: "u_7", Typ: "user",
		Extra: map[string]string{"arz/folder": "atlas/main", "name": "alice"},
	})
	var gotSub string
	grants := func(bareSub string) []string {
		gotSub = bareSub
		return []string{"**", "corp/eng"} // operator + a folder, from the DB
	}
	var seen *http.Request
	h := RequireSignedOrBearer(mwSecret, ks, grants)(func(w http.ResponseWriter, r *http.Request) {
		seen = r
	})
	r := httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	h(w, r)
	if seen == nil {
		t.Fatal("valid ES256 bearer rejected")
	}
	if gotSub != "u_7" {
		t.Fatalf("grants resolver got bareSub %q, want u_7 (user: prefix stripped)", gotSub)
	}
	if g := seen.Header.Get("X-User-Groups"); g != `["**","corp/eng"]` {
		t.Fatalf("X-User-Groups = %q, want DB-sourced [**,corp/eng] (not the token's arz/folder)", g)
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

// PRECEDENCE: HMAC-header-first. When BOTH a valid HMAC sig and a valid ES256
// bearer are present, the HMAC identity wins — the middleware checks
// VerifyUserSig before tryES256, so the bearer is never consulted. This keeps
// existing HMAC callers byte-identical during the soak: adding a bearer to a
// request that already carries a valid sig changes nothing. (Once a signer
// flips to bearer it stops sending the HMAC sig, so the two never collide in
// production; the precedence only governs the transient overlap.)
func TestRequireSignedOrBearer_BothPresent_HMACWins(t *testing.T) {
	ks, tok := bearerKeySet(t, TokenClaims{
		Sub: "u_7", Typ: "user",
		Extra: map[string]string{"arz/folder": "atlas/main", "name": "bob"},
	})
	var seen *http.Request
	h := RequireSignedOrBearer(mwSecret, ks, nil)(func(w http.ResponseWriter, r *http.Request) {
		seen = r
	})
	r := httptest.NewRequest("GET", "/x", nil)
	signedRequest(r, "github:42", "alice", `["**"]`)
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	h(w, r)
	if seen == nil {
		t.Fatal("both creds present, request rejected")
	}
	if seen.Header.Get("X-User-Sub") != "github:42" {
		t.Fatalf("HMAC must win when both present: X-User-Sub=%q want github:42 (bearer would stamp u_7)",
			seen.Header.Get("X-User-Sub"))
	}
}

func TestStripUnsignedOrBearer_BothPresent_HMACWins(t *testing.T) {
	ks, tok := bearerKeySet(t, TokenClaims{
		Sub: "u_7", Typ: "user", Extra: map[string]string{"arz/folder": "atlas/main"},
	})
	var seen *http.Request
	h := StripUnsignedOrBearer(mwSecret, ks, nil)(func(w http.ResponseWriter, r *http.Request) {
		seen = r
	})
	r := httptest.NewRequest("GET", "/x", nil)
	signedRequest(r, "github:42", "alice", `["**"]`)
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	h(w, r)
	if seen == nil || seen.Header.Get("X-User-Sub") != "github:42" {
		t.Fatalf("HMAC must win when both present: got %q want github:42", seen.Header.Get("X-User-Sub"))
	}
}

// A tampered ES256 bearer (mutated last char of the signature) must NOT admit.
// With no HMAC fallback, RequireSignedOrBearer redirects; StripUnsignedOrBearer
// strips. This proves the bearer path verifies the signature, not merely the
// presence of a Bearer header.
func TestRequireSignedOrBearer_TamperedES256_Rejected(t *testing.T) {
	ks, tok := bearerKeySet(t, TokenClaims{Sub: "u_7", Typ: "user"})
	called := false
	h := RequireSignedOrBearer(mwSecret, ks, nil)(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})
	r := httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("Authorization", "Bearer "+tamper(tok))
	w := httptest.NewRecorder()
	h(w, r)
	if called {
		t.Fatal("tampered ES256 bearer admitted")
	}
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status: got %d want 303", w.Code)
	}
}

func TestStripUnsignedOrBearer_TamperedES256_NoIdentity(t *testing.T) {
	ks, tok := bearerKeySet(t, TokenClaims{Sub: "u_7", Typ: "user"})
	var seen *http.Request
	h := StripUnsignedOrBearer(mwSecret, ks, nil)(func(w http.ResponseWriter, r *http.Request) {
		seen = r
	})
	r := httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("Authorization", "Bearer "+tamper(tok))
	w := httptest.NewRecorder()
	h(w, r)
	if seen == nil {
		t.Fatal("next not called (lenient variant must pass through)")
	}
	if seen.Header.Get("X-User-Sub") != "" {
		t.Fatalf("tampered bearer stamped an identity: X-User-Sub=%q", seen.Header.Get("X-User-Sub"))
	}
}

// Forged/unsigned HMAC headers + a VALID bearer: the bearer carries the load.
// VerifyUserSig fails on the bad sig, the middleware falls through to tryES256,
// which verifies the bearer and RE-STAMPS the identity headers from the token —
// so the forged X-User-Sub is overwritten by the token's authentic sub, never
// trusted. This is the migration's whole point: a caller can drop HMAC entirely
// and present only a bearer.
func TestRequireSignedOrBearer_ForgedHMAC_ValidBearer_AdmitsOnBearer(t *testing.T) {
	ks, tok := bearerKeySet(t, TokenClaims{
		Sub: "u_7", Typ: "user", Extra: map[string]string{"arz/folder": "atlas/main"},
	})
	var seen *http.Request
	h := RequireSignedOrBearer(mwSecret, ks, nil)(func(w http.ResponseWriter, r *http.Request) {
		seen = r
	})
	r := httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("X-User-Sub", "github:hacker") // forged claim, garbage sig
	r.Header.Set("X-User-Sig", "garbage")
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	h(w, r)
	if seen == nil {
		t.Fatal("valid bearer rejected when forged HMAC headers also present")
	}
	if seen.Header.Get("X-User-Sub") != "u_7" {
		t.Fatalf("forged X-User-Sub not overwritten by bearer identity: got %q want u_7",
			seen.Header.Get("X-User-Sub"))
	}
}

// A typ=service token must NOT authenticate these human-identity middlewares
// (they replace the proxyd-signed user path). Admitting it would be admission
// widening: a service token could impersonate a human surface. Strict rejects;
// lenient stamps no identity.
func TestRequireSignedOrBearer_ServiceToken_Rejected(t *testing.T) {
	ks, tok := bearerKeySet(t, TokenClaims{Sub: "service:teled", Typ: "service"})
	called := false
	h := RequireSignedOrBearer(mwSecret, ks, nil)(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})
	r := httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	h(w, r)
	if called {
		t.Fatal("typ=service bearer admitted on a human-identity middleware")
	}
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status: got %d want 303", w.Code)
	}
}

func TestStripUnsignedOrBearer_ServiceToken_NoIdentity(t *testing.T) {
	ks, tok := bearerKeySet(t, TokenClaims{Sub: "service:teled", Typ: "service"})
	var seen *http.Request
	h := StripUnsignedOrBearer(mwSecret, ks, nil)(func(w http.ResponseWriter, r *http.Request) {
		seen = r
	})
	r := httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	h(w, r)
	if seen == nil {
		t.Fatal("lenient variant must pass through")
	}
	if seen.Header.Get("X-User-Sub") != "" {
		t.Fatalf("typ=service stamped an identity: X-User-Sub=%q", seen.Header.Get("X-User-Sub"))
	}
}

// A typ=downscoped token (descends from a user) IS admitted.
func TestRequireSignedOrBearer_DownscopedToken_Accepted(t *testing.T) {
	ks, tok := bearerKeySet(t, TokenClaims{
		Sub: "user:u_7", Typ: "downscoped", ParentJTI: "parent-jti",
		Extra: map[string]string{"arz/folder": "atlas/main"},
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
		t.Fatal("typ=downscoped bearer rejected")
	}
	if seen.Header.Get("X-User-Sub") != "u_7" {
		t.Fatalf("X-User-Sub not stamped from downscoped token: got %q", seen.Header.Get("X-User-Sub"))
	}
}

// On bearer admit, a stale client-supplied X-User-Sig is deleted — only
// verified-token headers survive the stamp.
func TestRequireSignedOrBearer_BearerAdmit_StripsStaleSig(t *testing.T) {
	ks, tok := bearerKeySet(t, TokenClaims{Sub: "u_7", Typ: "user"})
	var seen *http.Request
	h := RequireSignedOrBearer(mwSecret, ks, nil)(func(w http.ResponseWriter, r *http.Request) {
		seen = r
	})
	r := httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("X-User-Sig", "attacker-garbage")
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	h(w, r)
	if seen == nil {
		t.Fatal("valid bearer rejected")
	}
	if seen.Header.Get("X-User-Sig") != "" {
		t.Fatalf("stale X-User-Sig survived bearer stamp: got %q", seen.Header.Get("X-User-Sig"))
	}
}

// tamper mutates a char in the JWS PAYLOAD segment so the token still parses
// (three dot-separated segments, valid base64url) but fails signature
// verification. We avoid flipping the final signature char: a base64url char
// can carry unused low bits, so a flip there sometimes decodes to identical
// signature bytes and still verifies (a flaky no-op). Mutating the payload
// always changes the signed-over bytes, so verification deterministically
// fails.
func tamper(tok string) string {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 || len(parts[1]) == 0 {
		return tok
	}
	p := []byte(parts[1])
	if p[0] == 'A' {
		p[0] = 'B'
	} else {
		p[0] = 'A'
	}
	parts[1] = string(p)
	return strings.Join(parts, ".")
}

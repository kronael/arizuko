package auth

import (
	"crypto/ecdsa"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The arz/folder claim must serialize as a TOP-LEVEL JWT claim, not a nested
// "extra" object — routd/runed read Subject.Extra["arz/folder"] back. A
// regression to nested serialization makes folder-scope authz silently absent.
func TestExtraSerializesTopLevelAndRoundTrips(t *testing.T) {
	k, _ := NewSigningKey("k1")
	tok, err := k.Sign(TokenClaims{
		Sub:   "user:7",
		Scope: []string{"tasks:read"},
		Extra: map[string]string{"arz/folder": "atlas/main"},
	}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// The minted payload carries arz/folder at the top level, and NO nested
	// "extra" object.
	payload := decodeJWTPayload(t, tok)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(payload, &raw); err != nil {
		t.Fatal(err)
	}
	if _, nested := raw["extra"]; nested {
		t.Fatalf("Extra must not serialize as nested 'extra' object: %s", payload)
	}
	var folder string
	if err := json.Unmarshal(raw["arz/folder"], &folder); err != nil || folder != "atlas/main" {
		t.Fatalf("arz/folder not a top-level claim: %s", payload)
	}
	// And it round-trips through verify into Subject.Extra.
	ks := NewKeySet(map[string]*ecdsa.PublicKey{"k1": &k.Priv.PublicKey})
	sub, err := VerifyToken(tok, ks)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if sub.Extra["arz/folder"] != "atlas/main" {
		t.Fatalf("Subject.Extra round-trip lost folder: %+v", sub.Extra)
	}
}

// decodeJWTPayload returns the raw JSON payload (middle segment) of a compact
// JWS without verifying — for asserting on-the-wire claim shape.
func decodeJWTPayload(t *testing.T, tok string) []byte {
	t.Helper()
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("not a compact JWS: %q", tok)
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A retired key keeps verifying tokens it signed BEFORE retirement, but a
// token whose iat is AFTER retired_at could only have been minted by a thief
// holding the retired private key. The local KeySet must reject it.
func TestRetiredKeyRejectsPostRetirementForgery(t *testing.T) {
	k, _ := NewSigningKey("k1")
	retiredAt := time.Now().Add(-time.Hour) // retired an hour ago, still in window
	// A token minted NOW (iat = now) is after retired_at — forgery.
	forged, _ := k.Sign(TokenClaims{Sub: "user:1"}, time.Minute)
	ks := NewKeySetWithRetirement(
		map[string]*ecdsa.PublicKey{"k1": &k.Priv.PublicKey},
		map[string]time.Time{"k1": retiredAt})
	if _, err := VerifyToken(forged, ks); err != ErrInvalidToken {
		t.Fatalf("token minted after retired_at must be rejected, got %v", err)
	}
	// A token whose iat predates retirement still verifies (legit pre-retire
	// token inside the overlap window). Same key, retired_at set to the future.
	legit, _ := k.Sign(TokenClaims{Sub: "user:1"}, time.Minute)
	ksLegit := NewKeySetWithRetirement(
		map[string]*ecdsa.PublicKey{"k1": &k.Priv.PublicKey},
		map[string]time.Time{"k1": time.Now().Add(time.Hour)})
	if _, err := VerifyToken(legit, ksLegit); err != nil {
		t.Fatalf("pre-retirement token must still verify in window: %v", err)
	}
}

func TestSignVerifyRoundTrip(t *testing.T) {
	k, err := NewSigningKey("k1")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := k.Sign(TokenClaims{Sub: "user:42", Scope: []string{"tasks:read"}, Aud: "app"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ks := NewKeySet(map[string]*ecdsa.PublicKey{"k1": &k.Priv.PublicKey})
	sub, err := VerifyToken(tok, ks)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if sub.Sub != "user:42" || sub.Iss != Issuer {
		t.Fatalf("bad subject: %+v", sub)
	}
	if !HasScope(sub.Scope, "tasks", "read") {
		t.Fatalf("expected tasks:read in %v", sub.Scope)
	}
	if !MatchesAudience(sub, "app") || MatchesAudience(sub, "other") {
		t.Fatalf("audience check wrong for %+v", sub)
	}
}

func TestVerifyRejectsWrongKey(t *testing.T) {
	k1, _ := NewSigningKey("k1")
	k2, _ := NewSigningKey("k2")
	tok, _ := k1.Sign(TokenClaims{Sub: "user:1"}, time.Minute)
	// KeySet only knows k2's public key; k1-signed token must fail.
	ks := NewKeySet(map[string]*ecdsa.PublicKey{"k2": &k2.Priv.PublicKey})
	if _, err := VerifyToken(tok, ks); err != ErrInvalidToken {
		t.Fatalf("expected ErrInvalidToken, got %v", err)
	}
}

func TestVerifyRejectsExpired(t *testing.T) {
	k, _ := NewSigningKey("k1")
	tok, _ := k.Sign(TokenClaims{Sub: "user:1"}, -2*time.Minute) // already expired past skew
	ks := NewKeySet(map[string]*ecdsa.PublicKey{"k1": &k.Priv.PublicKey})
	if _, err := VerifyToken(tok, ks); err != ErrExpiredToken {
		t.Fatalf("expected ErrExpiredToken, got %v", err)
	}
}

func TestPublicJWKSServesByKid(t *testing.T) {
	k1, _ := NewSigningKey("k1")
	k2, _ := NewSigningKey("k2")
	body, err := PublicJWKS(k1, k2)
	if err != nil {
		t.Fatal(err)
	}
	// A token from k2 must verify against a KeySet parsed from the JWKS doc.
	tok, _ := k2.Sign(TokenClaims{Sub: "user:2"}, time.Minute)
	ks := keySetFromJWKS(t, body)
	if _, err := VerifyToken(tok, ks); err != nil {
		t.Fatalf("verify against published jwks: %v", err)
	}
	// A made-up kid must miss.
	if _, ok := ks.lookup("nope"); ok {
		t.Fatal("unknown kid should not resolve")
	}
}

func TestMintForSubjectBoundsToTargetGrants(t *testing.T) {
	k, _ := NewSigningKey("k1")
	// Target only granted tasks:read; requesting tasks:write+read yields just read.
	tok, err := k.MintForSubject(
		[]string{"tasks:read"},
		TokenClaims{Sub: "user:1", Scope: []string{"tasks:write", "tasks:read"}},
		time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ks := NewKeySet(map[string]*ecdsa.PublicKey{"k1": &k.Priv.PublicKey})
	sub, _ := VerifyToken(tok, ks)
	if HasScope(sub.Scope, "tasks", "write") {
		t.Fatalf("tasks:write must be dropped, got %v", sub.Scope)
	}
	if !HasScope(sub.Scope, "tasks", "read") {
		t.Fatalf("tasks:read must survive, got %v", sub.Scope)
	}
}

func TestMintForSubjectEmptyRequestGetsAllGrants(t *testing.T) {
	k, _ := NewSigningKey("k1")
	tok, _ := k.MintForSubject([]string{"a:read", "b:write"}, TokenClaims{Sub: "x"}, time.Minute)
	ks := NewKeySet(map[string]*ecdsa.PublicKey{"k1": &k.Priv.PublicKey})
	sub, _ := VerifyToken(tok, ks)
	if !HasScope(sub.Scope, "a", "read") || !HasScope(sub.Scope, "b", "write") {
		t.Fatalf("empty request should inherit all grants, got %v", sub.Scope)
	}
}

func TestMintNarrowerRejectsBroaderScope(t *testing.T) {
	k, _ := NewSigningKey("k1")
	// Parent has only tasks:read; requesting tasks:write must fail.
	if _, err := k.MintNarrower(
		[]string{"tasks:read"},
		TokenClaims{Sub: "agent:sub", Scope: []string{"tasks:write"}},
		time.Minute); err != ErrScopeTooBroad {
		t.Fatalf("expected ErrScopeTooBroad, got %v", err)
	}
}

func TestMintNarrowerAllowsSubset(t *testing.T) {
	k, _ := NewSigningKey("k1")
	tok, err := k.MintNarrower(
		[]string{"tasks:read", "tasks:write"},
		TokenClaims{Sub: "agent:sub", Scope: []string{"tasks:read"}},
		time.Minute)
	if err != nil {
		t.Fatalf("subset should mint: %v", err)
	}
	ks := NewKeySet(map[string]*ecdsa.PublicKey{"k1": &k.Priv.PublicKey})
	sub, _ := VerifyToken(tok, ks)
	if HasScope(sub.Scope, "tasks", "write") {
		t.Fatalf("narrower token must not gain tasks:write: %v", sub.Scope)
	}
}

func TestMintNarrowerWildcardParentCoversVerb(t *testing.T) {
	k, _ := NewSigningKey("k1")
	// Parent tasks:* covers the requested tasks:write.
	if _, err := k.MintNarrower(
		[]string{"tasks:*"},
		TokenClaims{Sub: "agent:sub", Scope: []string{"tasks:write"}},
		time.Minute); err != nil {
		t.Fatalf("tasks:* should cover tasks:write: %v", err)
	}
}

// typClaim decodes the minted JWS payload and returns its "typ" claim — the
// spec-mandated token-kind claim, distinct from the JWS header "typ":"JWT".
func typClaim(t *testing.T, tok string) string {
	t.Helper()
	var c TokenClaims
	if err := json.Unmarshal(decodeJWTPayload(t, tok), &c); err != nil {
		t.Fatal(err)
	}
	return c.Typ
}

// Each mint mode stamps the spec typ claim: MintForSubject carries whatever typ
// the caller sets ("user"/"service"); MintNarrower forces "downscoped".
func TestTypClaimPerMintMode(t *testing.T) {
	k, _ := NewSigningKey("k1")

	user, err := k.MintForSubject([]string{"tasks:read"},
		TokenClaims{Sub: "user:1", Typ: "user", Scope: []string{"tasks:read"}}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if got := typClaim(t, user); got != "user" {
		t.Fatalf("user mint typ = %q want user", got)
	}

	svc, err := k.MintForSubject([]string{"tasks:read"},
		TokenClaims{Sub: "service:timed", Typ: "service", Scope: []string{"tasks:read"}}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if got := typClaim(t, svc); got != "service" {
		t.Fatalf("service mint typ = %q want service", got)
	}

	down, err := k.MintNarrower([]string{"tasks:read", "tasks:write"},
		TokenClaims{Sub: "user:1", Scope: []string{"tasks:read"}}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if got := typClaim(t, down); got != "downscoped" {
		t.Fatalf("downscope typ = %q want downscoped (forced by MintNarrower)", got)
	}
}

// The typ claim is reserved, so it never leaks into Subject.Extra on round-trip.
func TestTypNotFoldedIntoExtra(t *testing.T) {
	k, _ := NewSigningKey("k1")
	tok, _ := k.Sign(TokenClaims{Sub: "user:1", Typ: "user", Scope: []string{"a:read"}}, time.Minute)
	ks := NewKeySet(map[string]*ecdsa.PublicKey{"k1": &k.Priv.PublicKey})
	sub, err := VerifyToken(tok, ks)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := sub.Extra["typ"]; ok {
		t.Fatal("typ is a reserved claim; it must not fold into Extra")
	}
}

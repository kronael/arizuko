package auth

import (
	"crypto/ecdsa"
	"testing"
	"time"
)

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

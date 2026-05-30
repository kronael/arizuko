package auth

// Verify-path threat scenarios for the offline ES256 verifier (specs/5/1
// "offline verification" + "Crypto stack ... pins ES256 (closes
// alg-confusion)"). Each test asserts a SECURITY property of VerifyToken /
// subjectFromPayload that, if it regressed, would let a forged or stale token
// through. These exercise the local (NewKeySet) verify path; the HTTP/
// RemoteKeySet path is covered end-to-end in authd/scenario_test.go.

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

func newRequestWithAuth(t *testing.T, header string) *http.Request {
	t.Helper()
	r, err := http.NewRequest(http.MethodGet, "/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if header != "" {
		r.Header.Set("Authorization", header)
	}
	return r
}

// signRaw signs an arbitrary claims map with the given key/alg, stamping kid in
// the protected header — lets a test forge tokens the normal Sign() path would
// never produce (wrong alg, absent exp, future nbf, wrong iss).
func signRaw(t *testing.T, alg jose.SignatureAlgorithm, key any, kid string, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	opts := (&jose.SignerOptions{}).WithType("JWT")
	opts.WithHeader("kid", kid)
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: alg, Key: key}, opts)
	if err != nil {
		t.Fatalf("new signer (%s): %v", alg, err)
	}
	jws, err := signer.Sign(payload)
	if err != nil {
		t.Fatalf("sign (%s): %v", alg, err)
	}
	tok, err := jws.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// validClaims is a well-formed claim set for the given key's kid; tests mutate
// one field to isolate the property under test.
func validClaims() map[string]any {
	now := time.Now().Unix()
	return map[string]any{
		"sub":   "user:1",
		"scope": []string{"tasks:read"},
		"iss":   Issuer,
		"iat":   now,
		"nbf":   now,
		"exp":   now + 900,
	}
}

// Threat: a token from a different issuer (another authd deployment, a stolen
// upstream IdP token) must not authorize here. specs/5/1 "Verifiers pin this
// exact value" (iss=="authd").
func TestVerifyRejectsWrongIssuer(t *testing.T) {
	k, _ := NewSigningKey("k1")
	ks := NewKeySet(map[string]*ecdsa.PublicKey{"k1": &k.Priv.PublicKey})

	c := validClaims()
	c["iss"] = "evil-idp"
	tok := signRaw(t, jose.ES256, k.Priv, "k1", c)

	if _, err := VerifyToken(tok, ks); err != ErrInvalidToken {
		t.Fatalf("wrong-issuer token must be rejected, got %v", err)
	}
}

// Threat: a token with no exp is an immortal token. specs/5/1 just-fixed
// "iat/nbf/exp are required ... no fail-open" — a missing exp is a reject, not
// "no constraint".
func TestVerifyRejectsAbsentExp(t *testing.T) {
	k, _ := NewSigningKey("k1")
	ks := NewKeySet(map[string]*ecdsa.PublicKey{"k1": &k.Priv.PublicKey})

	c := validClaims()
	delete(c, "exp")
	tok := signRaw(t, jose.ES256, k.Priv, "k1", c)

	if _, err := VerifyToken(tok, ks); err != ErrInvalidToken {
		t.Fatalf("token with absent exp must be rejected, got %v", err)
	}
}

// Threat: a token with no nbf bypasses the not-before window. Same just-fixed
// no-fail-open clause (specs/5/1).
func TestVerifyRejectsAbsentNbf(t *testing.T) {
	k, _ := NewSigningKey("k1")
	ks := NewKeySet(map[string]*ecdsa.PublicKey{"k1": &k.Priv.PublicKey})

	c := validClaims()
	delete(c, "nbf")
	tok := signRaw(t, jose.ES256, k.Priv, "k1", c)

	if _, err := VerifyToken(tok, ks); err != ErrInvalidToken {
		t.Fatalf("token with absent nbf must be rejected, got %v", err)
	}
}

// Threat: an expired token replayed past the clock-skew grace must fail.
// specs/5/1 TTL table "Access JWT ... the revocation blast-radius bound".
func TestVerifyRejectsExpiredPastSkew(t *testing.T) {
	k, _ := NewSigningKey("k1")
	ks := NewKeySet(map[string]*ecdsa.PublicKey{"k1": &k.Priv.PublicKey})

	now := time.Now().Unix()
	c := validClaims()
	c["iat"] = now - 7200
	c["nbf"] = now - 7200
	c["exp"] = now - 3600 // an hour past exp, well beyond the 30s skew
	tok := signRaw(t, jose.ES256, k.Priv, "k1", c)

	if _, err := VerifyToken(tok, ks); err != ErrExpiredToken {
		t.Fatalf("expired-past-skew token must be ErrExpiredToken, got %v", err)
	}
}

// Threat: a token whose nbf is in the future (pre-dated / not-yet-valid) must
// not verify before its window opens. Guards the `now+skew < nbf` branch.
func TestVerifyRejectsNotYetValid(t *testing.T) {
	k, _ := NewSigningKey("k1")
	ks := NewKeySet(map[string]*ecdsa.PublicKey{"k1": &k.Priv.PublicKey})

	now := time.Now().Unix()
	c := validClaims()
	c["nbf"] = now + 3600 // valid in an hour, well beyond the 30s skew
	c["iat"] = now + 3600
	c["exp"] = now + 7200
	tok := signRaw(t, jose.ES256, k.Priv, "k1", c)

	if _, err := VerifyToken(tok, ks); err != ErrInvalidToken {
		t.Fatalf("not-yet-valid (future nbf) token must be rejected, got %v", err)
	}
}

// Threat: nbf within the 30s skew grace is accepted — the verifier tolerates
// minor clock drift, matching the skew the issuer uses. (Positive control for
// the previous test: the rejection is the skew boundary, not all nbf>now.)
func TestVerifyAcceptsNbfWithinSkew(t *testing.T) {
	k, _ := NewSigningKey("k1")
	ks := NewKeySet(map[string]*ecdsa.PublicKey{"k1": &k.Priv.PublicKey})

	now := time.Now().Unix()
	c := validClaims()
	c["nbf"] = now + 10 // inside the 30s skew window
	c["iat"] = now
	c["exp"] = now + 900
	tok := signRaw(t, jose.ES256, k.Priv, "k1", c)

	if _, err := VerifyToken(tok, ks); err != nil {
		t.Fatalf("nbf within skew must verify, got %v", err)
	}
}

// Threat (CLASSIC): alg-confusion. An attacker re-signs the same claims with a
// symmetric HS256 MAC keyed on the public key bytes, hoping the verifier
// treats the public key as a shared secret. verifyLocal pins ES256, so parsing
// itself must refuse the HS256 token. specs/5/1 "pins ES256 (closes
// alg-confusion)".
func TestVerifyRejectsAlgConfusionHS256(t *testing.T) {
	k, _ := NewSigningKey("k1")
	ks := NewKeySet(map[string]*ecdsa.PublicKey{"k1": &k.Priv.PublicKey})

	// HMAC key = the marshalled public key — the canonical alg-confusion forgery.
	ecdhPub, err := k.Priv.PublicKey.ECDH()
	if err != nil {
		t.Fatal(err)
	}
	tok := signRaw(t, jose.HS256, ecdhPub.Bytes(), "k1", validClaims())

	if _, err := VerifyToken(tok, ks); err != ErrInvalidToken {
		t.Fatalf("HS256 alg-confusion token must be rejected, got %v", err)
	}
}

// Threat: a token validly signed under a DIFFERENT key type (RSA/RS256) but
// stamping a known kid must be rejected — the verifier only accepts ES256 and
// only ECDSA public keys. Guards against an attacker who controls a non-EC key.
func TestVerifyRejectsRSAKeyType(t *testing.T) {
	k, _ := NewSigningKey("k1")
	ks := NewKeySet(map[string]*ecdsa.PublicKey{"k1": &k.Priv.PublicKey})

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tok := signRaw(t, jose.RS256, rsaKey, "k1", validClaims())

	if _, err := VerifyToken(tok, ks); err != ErrInvalidToken {
		t.Fatalf("RS256 token must be rejected by an ES256-only verifier, got %v", err)
	}
}

// Threat: a token correctly ES256-signed by an UNKNOWN key (attacker generated
// their own ES256 keypair and stamped a kid) must miss the keyset and fail —
// signature validity alone is not trust; the kid must resolve to a key authd
// published. (Distinct from es256_test's wrong-key test in that the kid here
// is one the verifier knows nothing about.)
func TestVerifyRejectsUnknownKid(t *testing.T) {
	known, _ := NewSigningKey("known")
	attacker, _ := NewSigningKey("attacker-kid")
	ks := NewKeySet(map[string]*ecdsa.PublicKey{"known": &known.Priv.PublicKey})

	tok := signRaw(t, jose.ES256, attacker.Priv, "attacker-kid", validClaims())

	if _, err := VerifyToken(tok, ks); err != ErrInvalidToken {
		t.Fatalf("token under an unknown kid must be rejected, got %v", err)
	}
}

// Threat: garbage / truncated / wrong-shape input must return an error, never
// panic — VerifyToken sits on the request hot path and sees attacker-controlled
// bytes. A panic here is a DoS.
func TestVerifyGarbageDoesNotPanic(t *testing.T) {
	k, _ := NewSigningKey("k1")
	ks := NewKeySet(map[string]*ecdsa.PublicKey{"k1": &k.Priv.PublicKey})
	good, _ := k.Sign(TokenClaims{Sub: "user:1", Scope: []string{"a:read"}}, time.Minute)

	garbage := []string{
		"",
		".",
		"..",
		"not.a.jwt",
		"only-one-segment",
		"a.b", // two segments
		strings.Repeat("A", 5000),
		good[:len(good)-5],                          // truncated signature
		good[:len(good)/2],                          // truncated mid-token
		"eyJhbGciOiJFUzI1NiJ9." + good,              // mangled header prefix
		base64.RawURLEncoding.EncodeToString([]byte("{}")) + ".x.y",
	}
	for _, g := range garbage {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("VerifyToken panicked on %q: %v", g, r)
				}
			}()
			if _, err := VerifyToken(g, ks); err == nil {
				t.Fatalf("garbage token %q must error, got nil", g)
			}
		}()
	}
}

// Threat: a token whose signature was made over DIFFERENT bytes than the
// presented payload (signature lifted from another token) must fail the
// signature check. Builds a token then swaps its payload segment.
func TestVerifyRejectsTamperedPayload(t *testing.T) {
	k, _ := NewSigningKey("k1")
	ks := NewKeySet(map[string]*ecdsa.PublicKey{"k1": &k.Priv.PublicKey})

	c := validClaims()
	tok := signRaw(t, jose.ES256, k.Priv, "k1", c)
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("expected 3 JWS segments, got %d", len(parts))
	}
	// Re-encode the payload with an escalated scope, keep the old signature.
	tampered := validClaims()
	tampered["scope"] = []string{"secrets:read", "tasks:write"}
	pb, _ := json.Marshal(tampered)
	parts[1] = base64.RawURLEncoding.EncodeToString(pb)
	forged := strings.Join(parts, ".")

	if _, err := VerifyToken(forged, ks); err != ErrInvalidToken {
		t.Fatalf("payload-tampered token must fail signature check, got %v", err)
	}
}

// Threat: a token carrying the global scope "*:*" authorizes nothing —
// HasScope must never honor the global wildcard even when present in a verified
// token. specs/5/1 / 5-uniform-mcp-rest "never the global *:*".
func TestGlobalWildcardScopeAuthorizesNothing(t *testing.T) {
	k, _ := NewSigningKey("k1")
	ks := NewKeySet(map[string]*ecdsa.PublicKey{"k1": &k.Priv.PublicKey})

	tok, err := k.Sign(TokenClaims{Sub: "user:1", Scope: []string{"*:*", "*"}}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := VerifyToken(tok, ks)
	if err != nil {
		t.Fatalf("token itself is well-formed and must verify: %v", err)
	}
	if HasScope(sub.Scope, "tasks", "read") || HasScope(sub.Scope, "secrets", "read") {
		t.Fatalf("global *:* must authorize nothing, but HasScope passed for %v", sub.Scope)
	}
}

// Threat: MintNarrower must never let a *:* request smuggle through. A "*:*"
// parent covers nothing (scopeCoveredBy uses scopeMatches, which rejects *:*),
// so requesting any concrete scope from a *:* parent fails closed.
func TestMintNarrowerRejectsGlobalWildcardParent(t *testing.T) {
	k, _ := NewSigningKey("k1")
	if _, err := k.MintNarrower(
		[]string{"*:*"},
		TokenClaims{Sub: "agent:sub", Scope: []string{"tasks:read"}},
		time.Minute); err != ErrScopeTooBroad {
		t.Fatalf("a *:* parent must not cover any concrete scope, got %v", err)
	}
}

// VerifyHTTP threat: a request with no/!Bearer Authorization header is
// rejected; a well-formed Bearer verifies. Guards the header-extraction path.
func TestVerifyHTTPBearerExtraction(t *testing.T) {
	k, _ := NewSigningKey("k1")
	ks := NewKeySet(map[string]*ecdsa.PublicKey{"k1": &k.Priv.PublicKey})
	good, _ := k.Sign(TokenClaims{Sub: "user:1", Scope: []string{"a:read"}}, time.Minute)

	mk := func(h string) (Subject, error) {
		r := newRequestWithAuth(t, h)
		return VerifyHTTP(r, ks)
	}
	if _, err := mk(""); err != ErrInvalidToken {
		t.Fatalf("missing Authorization must reject, got %v", err)
	}
	if _, err := mk("Token " + good); err != ErrInvalidToken {
		t.Fatalf("non-Bearer scheme must reject, got %v", err)
	}
	if _, err := mk("Bearer " + good); err != nil {
		t.Fatalf("valid Bearer must verify, got %v", err)
	}
}

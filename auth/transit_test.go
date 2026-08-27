package auth

import (
	"crypto/ecdsa"
	"net/http"
	"testing"
	"time"
)

// ProxydTransit is the whole trust boundary between the web edge and every
// backend: proxyd strips client-supplied X-User-* headers, authenticates the
// user, re-stamps them, and proves it was proxyd with this bearer. A backend
// trusts the stamped headers ONLY on that proof, so every way of passing this
// check that is not "a live proxyd token" is a cross-user impersonation.
//
// The function had no test at all. These lock the four ways a forger could
// try to reach a backend directly and be believed.

func transitKeys(t *testing.T, keys ...*SigningKey) *KeySet {
	t.Helper()
	pub := map[string]*ecdsa.PublicKey{}
	for _, k := range keys {
		pub[k.Kid] = &k.Priv.PublicKey
	}
	return NewKeySet(pub)
}

func bearerReq(t *testing.T, tok string) *http.Request {
	t.Helper()
	r, err := http.NewRequest("GET", "http://backend/v1/tasks", nil)
	if err != nil {
		t.Fatal(err)
	}
	if tok != "" {
		r.Header.Set("Authorization", "Bearer "+tok)
	}
	return r
}

func TestProxydTransitAcceptsALiveProxydServiceToken(t *testing.T) {
	k, err := NewSigningKey("k1")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := k.Sign(TokenClaims{Sub: ProxydTransitSub, Typ: "service"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !ProxydTransit(bearerReq(t, tok), transitKeys(t, k)) {
		t.Fatal("a live authd-minted service:proxyd bearer must be accepted; every stamped header depends on it")
	}
}

// The subject pin is the load-bearing half. Without it any holder of ANY valid
// authd token — a user's own session bearer, an adapter's service token —
// could reach a backend directly, stamp X-User-Sub for somebody else, and be
// treated as that end user.
func TestProxydTransitRejectsEveryOtherValidToken(t *testing.T) {
	k, err := NewSigningKey("k1")
	if err != nil {
		t.Fatal(err)
	}
	ks := transitKeys(t, k)

	cases := []struct {
		name   string
		claims TokenClaims
	}{
		{"a user's own session bearer", TokenClaims{Sub: "user:alice", Typ: "user"}},
		{"a user token wearing proxyd's subject", TokenClaims{Sub: ProxydTransitSub, Typ: "user"}},
		{"another daemon's service token", TokenClaims{Sub: "service:webd", Typ: "service"}},
		{"a downscoped token", TokenClaims{Sub: ProxydTransitSub, Typ: "downscoped"}},
		{"the bare subject without the service: prefix", TokenClaims{Sub: "proxyd", Typ: "service"}},
		{"no typ at all", TokenClaims{Sub: ProxydTransitSub}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tok, err := k.Sign(c.claims, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if ProxydTransit(bearerReq(t, tok), ks) {
				t.Fatalf("%s passed the transit proof — it can forge X-User-* on any backend", c.name)
			}
		})
	}
}

func TestProxydTransitRejectsAnExpiredOrForeignToken(t *testing.T) {
	good, err := NewSigningKey("k1")
	if err != nil {
		t.Fatal(err)
	}
	attacker, err := NewSigningKey("k1") // same kid, different private key
	if err != nil {
		t.Fatal(err)
	}
	ks := transitKeys(t, good)
	claims := TokenClaims{Sub: ProxydTransitSub, Typ: "service"}

	// Past tokenClockSkew (30s). Inside it an expired token still verifies on
	// purpose; the assertion below pins that allowance so nobody widens it.
	expired, err := good.Sign(claims, -2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if ProxydTransit(bearerReq(t, expired), ks) {
		t.Error("an expired proxyd bearer must not prove transit")
	}

	forged, err := attacker.Sign(claims, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if ProxydTransit(bearerReq(t, forged), ks) {
		t.Error("a token signed by a key authd does not publish must not prove transit")
	}
}

func TestProxydTransitRejectsAMissingOrMalformedHeader(t *testing.T) {
	k, err := NewSigningKey("k1")
	if err != nil {
		t.Fatal(err)
	}
	ks := transitKeys(t, k)
	tok, err := k.Sign(TokenClaims{Sub: ProxydTransitSub, Typ: "service"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	if ProxydTransit(bearerReq(t, ""), ks) {
		t.Error("a request with no Authorization header must not prove transit")
	}

	// The token without the scheme: VerifyHTTP requires "Bearer ".
	raw, err := http.NewRequest("GET", "http://backend/v1/tasks", nil)
	if err != nil {
		t.Fatal(err)
	}
	raw.Header.Set("Authorization", tok)
	if ProxydTransit(raw, ks) {
		t.Error("a bare token with no Bearer scheme must not prove transit")
	}
}

// ks == nil means AUTHD_URL is unset (local dev). The doc comment is explicit
// that this returns false and that callers gate the open path on ks == nil
// separately — a nil keyset must never READ as proof, or "no auth configured"
// would silently become "everything is proxyd".
func TestProxydTransitIsFalseWithNoKeySet(t *testing.T) {
	k, err := NewSigningKey("k1")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := k.Sign(TokenClaims{Sub: ProxydTransitSub, Typ: "service"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if ProxydTransit(bearerReq(t, tok), nil) {
		t.Fatal("ProxydTransit(nil keyset) must be false, never an open-for-dev true")
	}
}

// The verifier accepts a token up to tokenClockSkew past exp so a backend
// whose clock runs fast does not reject live proxyd traffic. That allowance is
// a deliberate 30 seconds; an hour would turn a leaked bearer into an hour of
// impersonation, so the boundary is pinned here rather than left to a const
// nobody re-reads.
func TestProxydTransitClockSkewAllowanceIsThirtySeconds(t *testing.T) {
	k, err := NewSigningKey("k1")
	if err != nil {
		t.Fatal(err)
	}
	ks := transitKeys(t, k)
	claims := TokenClaims{Sub: ProxydTransitSub, Typ: "service"}

	justInside, err := k.Sign(claims, -20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !ProxydTransit(bearerReq(t, justInside), ks) {
		t.Error("20s past expiry is inside the 30s skew and must still verify")
	}

	justOutside, err := k.Sign(claims, -45*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if ProxydTransit(bearerReq(t, justOutside), ks) {
		t.Error("45s past expiry is outside the 30s skew and must be rejected")
	}
}

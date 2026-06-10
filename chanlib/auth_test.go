package chanlib

// Adapter-side Auth gate under ES256 (HMAC→ES256 retire step 4): routd presents
// a service:routd token → admit; any other token / no token → 401; with no
// keyset (local dev) → CHANNEL_SECRET constant-time compare.

import (
	"crypto/ecdsa"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
)

// signToken mints an ES256 JWT for sub/typ via key, plus the local KeySet that
// verifies it.
func signToken(t *testing.T, kid, sub, typ string) (token string, ks *auth.KeySet) {
	t.Helper()
	key, err := auth.NewSigningKey(kid)
	if err != nil {
		t.Fatalf("signing key: %v", err)
	}
	tok, err := key.Sign(auth.TokenClaims{Sub: sub, Typ: typ, Scope: []string{"messages:write"}}, time.Hour)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return tok, auth.NewKeySet(map[string]*ecdsa.PublicKey{kid: &key.Priv.PublicKey})
}

// callGate drives authGate with the given bearer and reports the status code.
func callGate(ks *auth.KeySet, secret, bearer string) int {
	h := authGate(ks, secret, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	r := httptest.NewRequest("POST", "/send", nil)
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	w := httptest.NewRecorder()
	h(w, r)
	return w.Code
}

// TestAuthGateES256AdmitsRoutd: a valid service:routd token is admitted.
func TestAuthGateES256AdmitsRoutd(t *testing.T) {
	tok, ks := signToken(t, "k-routd", CallerRoutd, "service")
	if got := callGate(ks, "channel-secret", tok); got != 200 {
		t.Fatalf("service:routd token: status %d, want 200", got)
	}
}

// TestAuthGateES256RejectsOtherCaller: a valid token for a non-routd principal
// (an adapter's own service:teled, or a user token) is 401 — only routd drives
// an adapter's egress.
func TestAuthGateES256RejectsOtherCaller(t *testing.T) {
	for _, c := range []struct {
		name, sub, typ string
	}{
		{"adapter principal", "service:teled", "service"},
		{"user token", "user:42", "user"},
		{"service-typed non-routd", "service:proxyd", "service"},
	} {
		t.Run(c.name, func(t *testing.T) {
			tok, ks := signToken(t, "k1", c.sub, c.typ)
			if got := callGate(ks, "channel-secret", tok); got != 401 {
				t.Fatalf("%s: status %d, want 401", c.name, got)
			}
		})
	}
}

// TestAuthGateES256RejectsMissingAndGarbage: no bearer / a non-JWT string are
// both 401 under the ES256 gate (no fail-open).
func TestAuthGateES256RejectsMissingAndGarbage(t *testing.T) {
	_, ks := signToken(t, "k1", CallerRoutd, "service")
	if got := callGate(ks, "channel-secret", ""); got != 401 {
		t.Fatalf("no token: status %d, want 401", got)
	}
	if got := callGate(ks, "channel-secret", "not-a-jwt"); got != 401 {
		t.Fatalf("garbage token: status %d, want 401", got)
	}
	// The legacy CHANNEL_SECRET must NOT pass once ES256 is active.
	if got := callGate(ks, "channel-secret", "channel-secret"); got != 401 {
		t.Fatalf("CHANNEL_SECRET against ES256 gate: status %d, want 401", got)
	}
}

// TestAuthGateSecretFallback: with no keyset (ks==nil, local dev) the gate is
// the CHANNEL_SECRET constant-time compare.
func TestAuthGateSecretFallback(t *testing.T) {
	if got := callGate(nil, "the-secret", "the-secret"); got != 200 {
		t.Fatalf("matching secret: status %d, want 200", got)
	}
	if got := callGate(nil, "the-secret", "wrong"); got != 401 {
		t.Fatalf("wrong secret: status %d, want 401", got)
	}
	// ks==nil AND empty secret → open (single-process tests).
	if got := callGate(nil, "", ""); got != 200 {
		t.Fatalf("open gate: status %d, want 200", got)
	}
}

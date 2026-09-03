package main

import (
	"crypto/ecdsa"
	"net/http"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
)

// A verifier returning a partial subject alongside an error hands a caller's
// identity to a handler that only checks err on some paths.
func TestKeysetVerifierReturnsNothingOnAFailedVerify(t *testing.T) {
	k, err := auth.NewSigningKey("k1")
	if err != nil {
		t.Fatal(err)
	}
	other, err := auth.NewSigningKey("k1")
	if err != nil {
		t.Fatal(err)
	}
	ks := auth.NewKeySet(map[string]*ecdsa.PublicKey{k.Kid: &k.Priv.PublicKey})
	v := keysetVerifier{ks: ks}

	claims := auth.TokenClaims{
		Sub: "service:routd", Typ: "service", Scope: []string{"runs:write"},
		Extra: map[string]string{"arz/folder": "acme/eng"},
	}
	good, err := k.Sign(claims, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	sub, scope, folder, err := v.Verify(bearer(t, good))
	if err != nil {
		t.Fatalf("a live token failed verification: %v", err)
	}
	if sub != "service:routd" || folder != "acme/eng" || len(scope) != 1 {
		t.Errorf("verified (%q, %v, %q)", sub, scope, folder)
	}

	expired, err := k.Sign(claims, -2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	forged, err := other.Sign(claims, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for name, req := range map[string]*http.Request{
		"no header":             bearer(t, ""),
		"not a token":           bearer(t, "garbage"),
		"expired":               bearer(t, expired),
		"signed by another key": bearer(t, forged),
	} {
		sub, scope, folder, err := v.Verify(req)
		if err == nil {
			t.Errorf("%s: Verify accepted it", name)
		}
		if sub != "" || scope != nil || folder != "" {
			t.Errorf("%s: a failed verify returned (%q, %v, %q)", name, sub, scope, folder)
		}
	}
}

func bearer(t *testing.T, tok string) *http.Request {
	t.Helper()
	r, err := http.NewRequest("POST", "http://runed/v1/runs", nil)
	if err != nil {
		t.Fatal(err)
	}
	if tok != "" {
		r.Header.Set("Authorization", "Bearer "+tok)
	}
	return r
}

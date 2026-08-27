package main

import (
	"crypto/ecdsa"
	"net/http"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
)

// runed's env readers and its verifier. Both must fail toward the documented
// default and toward "no identity" respectively: RUNED_RUN_TIMEOUT parsed as 0
// would kill every run the instant it starts, and a verifier returning a
// partial subject alongside an error hands a caller's identity to a handler
// that only checks err on some paths.

func TestEnvOrFallsBackOnUnsetAndEmpty(t *testing.T) {
	const k = "ARIZUKO_RUNED_TEST_STR"
	t.Setenv(k, "")
	if got := envOr(k, ":8080"); got != ":8080" {
		t.Errorf("empty: %q, want the default", got)
	}
	t.Setenv(k, ":9099")
	if got := envOr(k, ":8080"); got != ":9099" {
		t.Errorf("set: %q", got)
	}
}

func TestDurOrRejectsAnythingUnparseable(t *testing.T) {
	const k = "ARIZUKO_RUNED_TEST_DUR"
	cases := map[string]time.Duration{
		"":           20 * time.Minute,
		"20":         20 * time.Minute, // no unit
		"20 minutes": 20 * time.Minute,
		"never":      20 * time.Minute,
		"90s":        90 * time.Second,
		"1h":         time.Hour,
	}
	for in, want := range cases {
		t.Setenv(k, in)
		if got := durOr(k, 20*time.Minute); got != want {
			t.Errorf("durOr(%q) = %s, want %s", in, got, want)
		}
	}
}

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

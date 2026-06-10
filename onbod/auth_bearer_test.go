package main

import (
	"crypto/ecdsa"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
)

// onbod transit-proof gate: stripUnsignedGuard keeps the proxyd-stamped
// X-User-Sub only when the request proves it transited proxyd — a valid authd
// ES256 service:proxyd transit bearer. The bearer is a transit proof ONLY:
// onbod's /onboard reads X-User-Sub as the OAuth'd end-user (matchGate's
// github:/google: checks), so the bearer's own service:proxyd subject must NOT
// overwrite it. Unproven → stripped. No verifier (local dev) → pass through.

// proxydBearer mints the service:proxyd transit token proxyd attaches + its KeySet.
func proxydBearer(t *testing.T) (*auth.KeySet, string) {
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

// TestOnbodGuard_ProxydBearer_KeepsEndUserSub is the onbod analogue of the
// dashd-403 regression: a valid service:proxyd bearer admits the request AND the
// proxyd-stamped end-user X-User-Sub (google:123) survives — it is NOT clobbered
// by the bearer's service:proxyd subject. A clobbered sub would stall onboarding
// (matchGate's google:/github: checks would see service:proxyd).
func TestOnbodGuard_ProxydBearer_KeepsEndUserSub(t *testing.T) {
	ks, tok := proxydBearer(t)
	g := stripUnsignedGuard(ks)
	var seenSub string
	h := g(func(w http.ResponseWriter, r *http.Request) { seenSub = r.Header.Get("X-User-Sub") })

	r := httptest.NewRequest("GET", "/onboard", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	r.Header.Set("X-User-Sub", "google:123")
	h(httptest.NewRecorder(), r)

	if seenSub != "google:123" {
		t.Fatalf("end-user sub lost behind proxyd bearer: got %q, want google:123", seenSub)
	}
	if matchGate([]gate{{kind: "google"}}, seenSub) == nil {
		t.Fatalf("google gate did not match end-user sub %q (onboarding would stall)", seenSub)
	}
}

// TestOnbodGuard_UnprovenSub_Stripped: an X-User-Sub with neither proof (a
// request bypassing proxyd) is stripped — public flow then redirects to login.
// A bogus bearer is no proof.
func TestOnbodGuard_UnprovenSub_Stripped(t *testing.T) {
	ks, _ := proxydBearer(t)
	g := stripUnsignedGuard(ks)
	var seenSub string
	h := g(func(w http.ResponseWriter, r *http.Request) { seenSub = r.Header.Get("X-User-Sub") })

	r := httptest.NewRequest("GET", "/onboard", nil)
	r.Header.Set("X-User-Sub", "github:hacker") // forged, unsigned, no bearer
	h(httptest.NewRecorder(), r)

	if seenSub != "" {
		t.Fatalf("unproven sub not stripped: got %q", seenSub)
	}
}

// TestOnbodGuard_BogusBearer_Stripped: a bearer signed by a key the KeySet
// doesn't know is no proof — the sub is stripped.
func TestOnbodGuard_BogusBearer_Stripped(t *testing.T) {
	ks, _ := proxydBearer(t)
	other, err := auth.NewSigningKey("other")
	if err != nil {
		t.Fatal(err)
	}
	bogus, err := other.Sign(auth.TokenClaims{Sub: "service:proxyd", Typ: "service"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	g := stripUnsignedGuard(ks)
	var seenSub string
	h := g(func(w http.ResponseWriter, r *http.Request) { seenSub = r.Header.Get("X-User-Sub") })

	r := httptest.NewRequest("GET", "/onboard", nil)
	r.Header.Set("Authorization", "Bearer "+bogus)
	r.Header.Set("X-User-Sub", "github:hacker")
	h(httptest.NewRecorder(), r)

	if seenSub != "" {
		t.Fatalf("bogus-bearer sub not stripped: got %q", seenSub)
	}
}

// TestOnbodGuard_NonProxydBearer_Stripped: a VALID authd token whose subject is
// NOT service:proxyd is not a transit proof — the forged X-User-Sub is stripped.
// Signed by the SAME trusted key, so only the subject pin rejects it.
func TestOnbodGuard_NonProxydBearer_Stripped(t *testing.T) {
	k, err := auth.NewSigningKey("k1")
	if err != nil {
		t.Fatal(err)
	}
	userTok, err := k.Sign(auth.TokenClaims{Sub: "user:google:mallory", Typ: "user"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ks := auth.NewKeySet(map[string]*ecdsa.PublicKey{"k1": &k.Priv.PublicKey})
	g := stripUnsignedGuard(ks)
	var seenSub string
	h := g(func(w http.ResponseWriter, r *http.Request) { seenSub = r.Header.Get("X-User-Sub") })

	r := httptest.NewRequest("GET", "/onboard", nil)
	r.Header.Set("Authorization", "Bearer "+userTok)
	r.Header.Set("X-User-Sub", "google:victim") // forged end-user identity
	h(httptest.NewRecorder(), r)

	if seenSub != "" {
		t.Fatalf("non-proxyd bearer kept forged sub: got %q (any authd token forges identity!)", seenSub)
	}
}

// TestOnbodGuard_NoProof_PassesThrough: both proofs unconfigured (local dev) →
// the header passes through unchecked.
func TestOnbodGuard_NoProof_PassesThrough(t *testing.T) {
	g := stripUnsignedGuard(nil)
	var seenSub string
	h := g(func(w http.ResponseWriter, r *http.Request) { seenSub = r.Header.Get("X-User-Sub") })

	r := httptest.NewRequest("GET", "/onboard", nil)
	r.Header.Set("X-User-Sub", "github:dev")
	h(httptest.NewRecorder(), r)

	if seenSub != "github:dev" {
		t.Fatalf("local-dev pass-through stripped sub: got %q", seenSub)
	}
}

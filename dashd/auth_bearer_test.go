package main

import (
	"crypto/ecdsa"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
)

// dashd transit-proof gate: guard admits a request proving it transited proxyd
// via a valid authd ES256 service:proxyd bearer. The bearer is a transit proof
// ONLY — dashd's authz reads X-User-Sub/-Groups directly, so the END-USER
// identity proxyd stamped must survive untouched; the bearer's own service:proxyd
// subject must NOT clobber it.

// proxydBearerKS mints a service:proxyd token (what proxyd attaches) and the
// KeySet that verifies it.
func proxydBearerKS(t *testing.T) (*auth.KeySet, string) {
	t.Helper()
	k, err := auth.NewSigningKey("k1")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := k.Sign(auth.TokenClaims{
		Sub: "service:proxyd", Typ: "service", Scope: []string{},
	}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return auth.NewKeySet(map[string]*ecdsa.PublicKey{"k1": &k.Priv.PublicKey}), tok
}

// guardedDash wires a dash whose guard requires a transit proof (ks non-nil)
// over an operator-granted routd.db, so the portal renders the operator nav iff
// the end-user identity reaches the handler.
func guardedDash(t *testing.T, ks *auth.KeySet) *dash {
	t.Helper()
	d, _, _ := splitAdminDash(t, "github:alice")
	d.ks = ks
	return d
}

// TestDashGuard_ProxydBearer_EndUserIdentity is the dashd-403 regression: a
// request carrying proxyd's valid service:proxyd ES256 bearer + the
// proxyd-stamped end-user X-User-Sub/-Groups passes the /dash/ gate AND is seen
// as github:alice (operator), not service:proxyd. A clobbered identity would
// hide the operator-only "invites" nav link (and break every per-folder authz).
func TestDashGuard_ProxydBearer_EndUserIdentity(t *testing.T) {
	ks, tok := proxydBearerKS(t)
	mux := newMux(guardedDash(t, ks))

	r := httptest.NewRequest("GET", "/dash/", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	r.Header.Set("X-User-Sub", "github:alice")
	r.Header.Set("X-User-Groups", `["**"]`)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("guarded /dash/ with proxyd bearer = %d, want 200 (body=%q)", w.Code, w.Body.String())
	}
	// Operator-only nav link proves the end-user (github:alice, `**`) identity
	// reached the handler — not the bearer's service:proxyd subject.
	if !containsInvitesNav(w.Body.String()) {
		t.Errorf("operator nav missing — end-user identity lost (bearer subject clobbered X-User-Sub?)")
	}
}

// TestDashGuard_NoBearer_NoSig_Redirects: a request with neither proof is
// rejected (redirect to /auth/login), so a caller bypassing proxyd cannot reach
// the dash with a raw forged X-User-Sub.
func TestDashGuard_NoBearer_NoSig_Redirects(t *testing.T) {
	ks, _ := proxydBearerKS(t)
	mux := newMux(guardedDash(t, ks))

	r := httptest.NewRequest("GET", "/dash/", nil)
	r.Header.Set("X-User-Sub", "github:alice")
	r.Header.Set("X-User-Groups", `["**"]`)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("unproven /dash/ = %d, want 303 redirect to login", w.Code)
	}
}

// TestDashGuard_BogusBearer_Redirects: an unverifiable bearer (wrong key) is no
// proof — rejected even though X-User-Sub looks legitimate.
func TestDashGuard_BogusBearer_Redirects(t *testing.T) {
	ks, _ := proxydBearerKS(t)
	// A token signed by a DIFFERENT key the KeySet doesn't know.
	other, err := auth.NewSigningKey("other")
	if err != nil {
		t.Fatal(err)
	}
	bogus, err := other.Sign(auth.TokenClaims{Sub: "service:proxyd", Typ: "service"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	mux := newMux(guardedDash(t, ks))

	r := httptest.NewRequest("GET", "/dash/", nil)
	r.Header.Set("Authorization", "Bearer "+bogus)
	r.Header.Set("X-User-Sub", "github:alice")
	r.Header.Set("X-User-Groups", `["**"]`)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("bogus bearer /dash/ = %d, want 303 redirect", w.Code)
	}
}

// TestDashGuard_NonProxydBearer_Redirects: a VALID authd token whose subject is
// NOT service:proxyd (e.g. a user's own token, or another service) is not a
// transit proof — it must be rejected, else any authd-token holder reaching
// dashd directly could forge X-User-Sub. Signed by the SAME key the KeySet
// trusts, so only the subject pin can reject it.
func TestDashGuard_NonProxydBearer_Redirects(t *testing.T) {
	k, err := auth.NewSigningKey("k1")
	if err != nil {
		t.Fatal(err)
	}
	userTok, err := k.Sign(auth.TokenClaims{Sub: "user:github:mallory", Typ: "user"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ks := auth.NewKeySet(map[string]*ecdsa.PublicKey{"k1": &k.Priv.PublicKey})
	mux := newMux(guardedDash(t, ks))

	r := httptest.NewRequest("GET", "/dash/", nil)
	r.Header.Set("Authorization", "Bearer "+userTok)
	r.Header.Set("X-User-Sub", "github:alice") // forged end-user identity
	r.Header.Set("X-User-Groups", `["**"]`)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("non-proxyd bearer admitted /dash/ = %d, want 303 (any authd token forges identity!)", w.Code)
	}
}

func containsInvitesNav(body string) bool {
	return containsStr(body, `href="/dash/invites/"`)
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

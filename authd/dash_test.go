package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/resreg/resources"
)

// A signing key's private half must never reach the page. The /v1 row type
// omits it by construction, and the dashboard renders only that row — this
// pins that the page did not reach around it.
func TestSigningKeyTableCarriesNoKeyMaterial(t *testing.T) {
	got := signingKeyTable([]resources.SigningKeysRow{
		{Kid: "k1", Alg: "ES256", Active: true, Status: "active", CreatedAt: "2026-01-01"},
	})
	for _, forbidden := range []string{"PRIVATE KEY", "priv_pem", "BEGIN EC"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("the key table rendered %q", forbidden)
		}
	}
	if !strings.Contains(got, "k1") || !strings.Contains(got, "ES256") {
		t.Error("the key table omits the metadata it exists to show")
	}
}

// No signing keys is not an empty table — authd cannot mint a token in that
// state, and the page must say so rather than render a blank panel.
func TestNoSigningKeysIsAnError(t *testing.T) {
	got := signingKeyTable(nil)
	if !strings.Contains(got, "banner-err") {
		t.Error("zero signing keys rendered as a normal empty table")
	}
}

// Only a live family offers sign-out: the writer reports zero rows for an
// already-revoked one, so the button would invite a press that fails.
func TestOnlyActiveSessionsOfferRevoke(t *testing.T) {
	live := sessionTable([]resources.SessionsRow{{FamilyID: "f1", Sub: "u", Status: "active"}})
	if !strings.Contains(live, "/dash/authd/revoke") {
		t.Error("an active session has no sign-out button")
	}
	for _, st := range []string{"revoked", "expired"} {
		got := sessionTable([]resources.SessionsRow{{FamilyID: "f1", Sub: "u", Status: st}})
		if strings.Contains(got, "/dash/authd/revoke") {
			t.Errorf("status %q offers sign-out; the writer reports zero rows for it", st)
		}
	}
}

// A sub is operator-supplied identity data rendered into HTML.
func TestSessionTableEscapes(t *testing.T) {
	got := sessionTable([]resources.SessionsRow{{
		FamilyID: "f1", Sub: `<script>alert(1)</script>`, Status: "active",
	}})
	if strings.Contains(got, "<script>") {
		t.Error("a sub rendered as live markup")
	}
}

// The dashboard and its control are gated to operators when a keyset is armed.
func TestAuthdDashboardGate(t *testing.T) {
	s := &server{dashKeys: &auth.KeySet{}}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "http://k.example/dash/authd/", nil)
	r.Header.Set("X-User-Groups", `["acme"]`)
	s.handleDash(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("folder-scoped caller = %d, want 403", w.Code)
	}

	// The write gate also refuses a cross-origin operator.
	w2 := httptest.NewRecorder()
	r2 := httptest.NewRequest(http.MethodPost, "http://k.example/dash/authd/revoke",
		strings.NewReader("family_id=f1"))
	r2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r2.Header.Set("X-User-Groups", `["**"]`)
	r2.Header.Set("Origin", "http://evil.example")
	s.handleDashRevoke(w2, r2)
	if w2.Code != http.StatusForbidden {
		t.Errorf("cross-origin revoke = %d, want 403", w2.Code)
	}
}

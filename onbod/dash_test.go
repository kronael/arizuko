package main

import (
	"crypto/ecdsa"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
	_ "modernc.org/sqlite"
)

// operatorKeySet returns a KeySet and the proxyd service bearer that verifies
// against it. A stamped X-User-Groups is trustworthy only behind that bearer,
// so a dashboard test must carry both halves to reach the page it is about.
func operatorKeySet(t *testing.T) (*auth.KeySet, string) {
	t.Helper()
	k, err := auth.NewSigningKey("k1")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := k.Sign(auth.TokenClaims{Sub: auth.ProxydTransitSub, Typ: "service"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return auth.NewKeySet(map[string]*ecdsa.PublicKey{"k1": &k.Priv.PublicKey}), tok
}

// dashBearer is the proxyd service bearer the current test's admin verifies
// against; a stamped header without it is refused at the gate.
var dashBearer string

func newDashAdmin(t *testing.T) *admin {
	t.Helper()
	db := ownedDB(t)
	ks, tok := operatorKeySet(t)
	dashBearer = tok
	return &admin{st: db, ks: ks}
}

// TestDashOperatorRendersQueue: an operator GET (`**` in X-User-Groups) renders
// the queue page with the freshly inserted onboarding row in the table.
func TestDashOperatorRendersQueue(t *testing.T) {
	a := newDashAdmin(t)
	if err := a.st.InsertOnboarding("telegram:user/42"); err != nil {
		t.Fatalf("seed onboarding: %v", err)
	}

	r := httptest.NewRequest("GET", "/dash/onbod/", nil)
	r.Header.Set("X-User-Groups", `["**"]`)
	r.Header.Set("Authorization", "Bearer "+dashBearer)
	w := httptest.NewRecorder()
	a.handleDash(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("operator GET status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "<table") {
		t.Fatalf("page missing table: %s", body)
	}
	if !strings.Contains(body, "telegram:user/42") {
		t.Fatalf("page missing seeded row: %s", body)
	}
	if !strings.Contains(body, "/dash/onbod/approve/") {
		t.Fatalf("page missing approve action: %s", body)
	}
}

// TestDashNonOperatorForbidden: a caller without `**` gets 403, never the queue.
func TestDashNonOperatorForbidden(t *testing.T) {
	a := newDashAdmin(t)

	r := httptest.NewRequest("GET", "/dash/onbod/", nil)
	r.Header.Set("X-User-Groups", `["solo/inbox"]`)
	w := httptest.NewRecorder()
	a.handleDash(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("non-operator GET status=%d, want 403", w.Code)
	}
}

// TestDashApproveFlipsRow: the approve shim flips a row to approved through the
// same store writer the /v1 handler uses, then redirects to the queue.
func TestDashApproveFlipsRow(t *testing.T) {
	a := newDashAdmin(t)
	if err := a.st.InsertOnboarding("telegram:user/7"); err != nil {
		t.Fatalf("seed onboarding: %v", err)
	}

	r := httptest.NewRequest("POST", "/dash/onbod/approve/telegram:user/7", nil)
	r.SetPathValue("jid", "telegram:user/7")
	r.Header.Set("X-User-Groups", `["**"]`)
	r.Header.Set("Authorization", "Bearer "+dashBearer)
	w := httptest.NewRecorder()
	a.handleDashApprove(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("approve status=%d, want 303", w.Code)
	}
	rows, err := a.st.ListOnboarding("approved")
	if err != nil {
		t.Fatalf("list approved: %v", err)
	}
	if len(rows) != 1 || rows[0].JID != "telegram:user/7" {
		t.Fatalf("approve did not flip row: %+v", rows)
	}
}

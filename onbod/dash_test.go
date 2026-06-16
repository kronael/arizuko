package main

import (
	"crypto/ecdsa"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/store"
	_ "modernc.org/sqlite"
)

// operatorKeySet returns a non-nil KeySet so the dash operator gate engages
// (nil ks short-circuits to open). The dashboard trusts the proxyd-stamped
// X-User-Groups, not the bearer, so the KeySet's contents don't matter here —
// only that requireOperator runs its `**` check.
func operatorKeySet(t *testing.T) *auth.KeySet {
	t.Helper()
	k, err := auth.NewSigningKey("k1")
	if err != nil {
		t.Fatal(err)
	}
	return auth.NewKeySet(map[string]*ecdsa.PublicKey{"k1": &k.Priv.PublicKey})
}

func newDashAdmin(t *testing.T) *admin {
	t.Helper()
	path := filepath.Join(t.TempDir(), "store", "onbod.db")
	mustMkdir(t, filepath.Dir(path))
	db, err := openOwnedDB(path)
	if err != nil {
		t.Fatalf("openOwnedDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return &admin{db: db, ks: operatorKeySet(t)}
}

// TestDashOperatorRendersQueue: an operator GET (`**` in X-User-Groups) renders
// the queue page with the freshly inserted onboarding row in the table.
func TestDashOperatorRendersQueue(t *testing.T) {
	a := newDashAdmin(t)
	if err := store.New(a.db).InsertOnboarding("telegram:user/42"); err != nil {
		t.Fatalf("seed onboarding: %v", err)
	}

	r := httptest.NewRequest("GET", "/dash/onbod/", nil)
	r.Header.Set("X-User-Groups", `["**"]`)
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
	if err := store.New(a.db).InsertOnboarding("telegram:user/7"); err != nil {
		t.Fatalf("seed onboarding: %v", err)
	}

	r := httptest.NewRequest("POST", "/dash/onbod/approve/telegram:user/7", nil)
	r.SetPathValue("jid", "telegram:user/7")
	r.Header.Set("X-User-Groups", `["**"]`)
	w := httptest.NewRecorder()
	a.handleDashApprove(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("approve status=%d, want 303", w.Code)
	}
	rows, err := store.New(a.db).ListOnboarding("approved")
	if err != nil {
		t.Fatalf("list approved: %v", err)
	}
	if len(rows) != 1 || rows[0].JID != "telegram:user/7" {
		t.Fatalf("approve did not flip row: %+v", rows)
	}
}

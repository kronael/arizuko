package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kronael/arizuko/auth/authtest"
)

// dashBearer is the proxyd service bearer the current test's admin verifies
// against; a stamped header without it is refused at the gate.
var dashBearer string

func newDashAdmin(t *testing.T) *admin {
	t.Helper()
	db := ownedDB(t)
	ks, tok := authtest.Operator(t)
	dashBearer = tok
	return &admin{st: db, ks: ks}
}

// TestDashOperatorRendersQueue: an operator GET (`**` in X-User-Groups) renders
// the queue page with the freshly inserted onboarding row in the table.
func TestDashOperatorRendersQueue(t *testing.T) {
	a := newDashAdmin(t)
	if err := a.st.InsertOnboarding("telegram:user/42", "telegram:user/42"); err != nil {
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
	if err := a.st.InsertOnboarding("telegram:user/7", "telegram:user/7"); err != nil {
		t.Fatalf("seed onboarding: %v", err)
	}

	r := httptest.NewRequest("POST", "/dash/onbod/approve/telegram:user/7", nil)
	r.SetPathValue("jid", "telegram:user/7")
	r.Header.Set("X-User-Groups", `["**"]`)
	r.Header.Set("Authorization", "Bearer "+dashBearer)
	// Origin is what a browser sends on every dashboard POST. The write gate needs
	// it (BUGS `L403`); a request carrying neither Origin nor Referer is refused
	// rather than admitted, because their joint absence is not the ordinary case.
	r.Header.Set("Origin", "http://"+r.Host)
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

// TestDashWritesRefuseCrossOrigin pins BUGS `L403`. All three onbod dash mutations are
// browser POSTs authenticated by the operator's cookie, and they gated on
// RequireOperator — the scope alone. Without the same-origin half a third-party page
// can drive an operator's browser into ADMITTING a group, which is the one onbod
// decision nobody takes back by reading a log.
//
// authd, proxyd, routd and runed have always used RequireWrite here; onbod was the one
// daemon that read the scope and skipped the origin (spec `6/28:138`).
func TestDashWritesRefuseCrossOrigin(t *testing.T) {
	for _, tc := range []struct {
		name   string
		call   func(*admin, http.ResponseWriter, *http.Request)
		target string
	}{
		{"approve", (*admin).handleDashApprove, "approve"},
		{"deny", (*admin).handleDashDeny, "deny"},
		{"reprompt", (*admin).handleDashReprompt, "reprompt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newDashAdmin(t)
			if err := a.st.InsertOnboarding("telegram:user/9", "telegram:user/9"); err != nil {
				t.Fatalf("seed onboarding: %v", err)
			}
			r := httptest.NewRequest("POST", "/dash/onbod/"+tc.target+"/telegram:user/9", nil)
			r.SetPathValue("jid", "telegram:user/9")
			r.Header.Set("X-User-Groups", `["**"]`)
			r.Header.Set("Authorization", "Bearer "+dashBearer)
			r.Header.Set("Origin", "https://evil.example.com")
			w := httptest.NewRecorder()
			tc.call(a, w, r)

			if w.Code != http.StatusForbidden {
				t.Fatalf("cross-origin %s status=%d, want 403", tc.target, w.Code)
			}
			// And it changed nothing: a refused write must not have run the writer.
			// A fresh row is awaiting_message, which is what InsertOnboarding writes.
			rows, err := a.st.ListOnboarding("")
			if err != nil {
				t.Fatalf("list onboarding: %v", err)
			}
			if len(rows) != 1 || rows[0].Status != "awaiting_message" {
				t.Fatalf("the row moved on a refused %s: %+v", tc.target, rows)
			}
		})
	}
}

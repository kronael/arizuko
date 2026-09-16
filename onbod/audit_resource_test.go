package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kronael/arizuko/store"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/resreg"
)

// BUGS F35: onbod is the fourth audit_log owner and was the only one whose rows
// needed sqlite3 on the box. These pin the mounted read.

// auditAdmin opens onbod's OWNED db through the real migrations, so the
// audit_log the handler reads is the one migration 0002 creates — not a
// hand-written restatement of it.
func auditAdmin(t *testing.T, ks *auth.KeySet) *admin {
	t.Helper()
	db := ownedDB(t)
	return &admin{st: db, ks: ks}
}

// seedOnbodAudit records rows and COUNTS them: an assertion that a row IS
// returned passes vacuously on an empty log, and so does one that a string is
// absent.
func seedOnbodAudit(t *testing.T, db *store.Store, rows ...[2]string) {
	t.Helper()
	snap := audit.Snapshot{}
	for i, r := range rows {
		snap.Rows = append(snap.Rows, audit.LegacyRow{
			ID:        int64(i + 1),
			CreatedAt: "2026-08-01T00:0" + string(rune('0'+i)) + ":00.000Z",
			Category:  "mutation", Action: r[0], Actor: "system", Folder: r[1],
			Outcome: "ok", ParamsSummary: `{"jid":"telegram:user/42"}`,
		})
	}
	n, err := audit.Import(t.Context(), db.Obj(), snap)
	if err != nil || n != len(rows) {
		t.Fatalf("fixture wrote %d rows, want %d (%v)", n, len(rows), err)
	}
}

// auditRowsFor reads back every recorded row carrying one action, through the
// method /v1/audit answers with.
func auditRowsFor(t *testing.T, db *store.Store, action string) []audit.Row {
	t.Helper()
	rows, err := audit.Query(t.Context(), db.Obj(), audit.Filter{Limit: audit.MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	var out []audit.Row
	for _, r := range rows {
		if r.Action == action {
			out = append(out, r)
		}
	}
	return out
}

func auditGET(t *testing.T, a *admin, target string) (int, []audit.Row, string) {
	t.Helper()
	mux := http.NewServeMux()
	a.mountAudit(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", target, nil))
	body := rec.Body.String()
	var rows []audit.Row
	_ = json.Unmarshal([]byte(body), &rows)
	return rec.Code, rows, body
}

// TestOnbodAuditServesItsOwnRows closes F35: the admission and invite trail is
// readable over the same GET /v1/audit path the other three owners serve, so
// dashd's fan-out reaches four of four. ks=nil is the local-dev open branch,
// the same one the gates/invites tests drive.
func TestOnbodAuditServesItsOwnRows(t *testing.T) {
	a := auditAdmin(t, nil)
	seedOnbodAudit(t, a.st,
		[2]string{"onboarding.approve", "acme/support"},
		[2]string{"invite.consume", "acme"},
	)

	code, rows, body := auditGET(t, a, "/v1/audit")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", code, body)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2: %s", len(rows), body)
	}
	if !strings.Contains(body, "onboarding.approve") || !strings.Contains(body, "invite.consume") {
		t.Errorf("response is missing onbod's own actions: %s", body)
	}
}

// TestOnbodAuditFolderClaimCannotWiden: a folder-claimed caller is pinned to its
// own subtree and its `folder` argument cannot widen the window. This is the
// containment the handler — not the gate — owns, because it bounds rows.
func TestOnbodAuditFolderClaimCannotWiden(t *testing.T) {
	a := auditAdmin(t, nil)
	seedOnbodAudit(t, a.st,
		[2]string{"onboarding.approve", "acme/support"},
		[2]string{"onboarding.approve", "other"},
	)

	rows, err := a.auditHandler(t.Context(), resreg.Execution{
		Action: resreg.ActionList,
		Caller: resreg.Caller{Folder: "acme"},
		Args:   resreg.Args{"folder": ""},
	})
	if err != nil {
		t.Fatalf("auditHandler: %v", err)
	}
	got, ok := rows.([]audit.Row)
	if !ok {
		t.Fatalf("handler returned %T, want []audit.Row", rows)
	}
	if len(got) != 1 || got[0].Folder != "acme/support" {
		t.Fatalf("folder-claimed caller saw %d rows %+v, want only acme's subtree", len(got), got)
	}
}

// TestOnbodAuditRESTGateRequiresAuditRead: with a KeySet wired the gate enforces
// audit:read. gates:read is the adversarial choice — a real, currently-granted
// onbod scope, which an inverted gate would let through.
func TestOnbodAuditRESTGateRequiresAuditRead(t *testing.T) {
	a := &admin{ks: &auth.KeySet{}}
	call := func(scopes string) error {
		return a.auditRESTGate(resreg.Execution{
			Action: resreg.ActionList,
			Caller: resreg.Caller{Claims: resreg.ScopeClaims(strings.Fields(scopes))},
		}, "", nil)
	}
	for _, c := range []struct {
		scopes string
		ok     bool
	}{
		{"audit:read", true},
		{"gates:read", false},
		{"invites:write", false},
		{"", false},
		// A user token's scope list holds folder globs, which carry no colon and
		// so can never satisfy a resource:verb scope. Operator `**` included.
		{"acme/**", false},
		{"**", false},
	} {
		if err := call(c.scopes); (err == nil) != c.ok {
			t.Errorf("scopes=%q: err=%v, want ok=%v", c.scopes, err, c.ok)
		}
	}
}

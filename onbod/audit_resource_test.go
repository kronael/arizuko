package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

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
	path := filepath.Join(t.TempDir(), "store", "onbod.db")
	mustSeedDB(t, path)
	db, err := openOwnedDB(path)
	if err != nil {
		t.Fatalf("openOwnedDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return &admin{db: db, ks: ks}
}

// seedOnbodAudit inserts rows and COUNTS them: an assertion that a row IS
// returned passes vacuously on an empty table, and so does one that a string is
// absent.
func seedOnbodAudit(t *testing.T, db *sql.DB, rows ...[2]string) {
	t.Helper()
	for i, r := range rows {
		if _, err := db.Exec(
			`INSERT INTO audit_log (created_at, category, action, actor, folder, outcome, params_summary)
			 VALUES (?, 'mutation', ?, 'system', ?, 'ok', '{"jid":"telegram:user/42"}')`,
			"2026-08-01T00:0"+string(rune('0'+i))+":00.000Z", r[0], r[1]); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_log`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != len(rows) {
		t.Fatalf("fixture wrote %d rows, want %d", n, len(rows))
	}
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
	seedOnbodAudit(t, a.db,
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
	seedOnbodAudit(t, a.db,
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

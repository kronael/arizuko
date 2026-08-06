package runed

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/kronael/arizuko/audit"
)

// seedAuditRows writes rows straight into runed.db's audit_log.
//
// It writes them RATHER than calling audit.EmitDB because audit.Emit's
// package-level DB is process-global test state; the point of the fixture is
// that the rows are certainly there. It then COUNTS them, because the
// containment tests below assert that a folder's rows are ABSENT from a
// response, and "absent" is satisfied just as well by an empty table — that is
// precisely how four audit tests shipped vacuous this week.
func seedAuditRows(t *testing.T, db *DB, rows ...[2]string) {
	t.Helper()
	for i, r := range rows {
		if _, err := db.SQL().Exec(
			`INSERT INTO audit_log (created_at, category, action, actor, folder, outcome)
			 VALUES (?, 'agent', ?, 'github:op', ?, 'ok')`,
			"2026-08-01T00:0"+string(rune('0'+i))+":00.000Z", r[0], r[1]); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM audit_log`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != len(rows) {
		t.Fatalf("fixture wrote %d audit rows, want %d — every assertion below "+
			"would pass vacuously on an empty table", n, len(rows))
	}
}

// decodeAuditRows parses the response body, failing loudly on a non-array so a
// 403 body cannot be mistaken for zero rows.
func decodeAuditRows(t *testing.T, body string) []audit.Row {
	t.Helper()
	var rows []audit.Row
	if err := json.Unmarshal([]byte(body), &rows); err != nil {
		t.Fatalf("body is not a row array (%v): %s", err, body)
	}
	return rows
}

// TestAuditReadRequiresScope: the endpoint exists and answers 403 without
// audit:read. The positive half of the same fixture is asserted immediately
// after, so a 403 caused by a broken mount rather than the gate would show up
// as BOTH halves failing.
func TestAuditReadRequiresScope(t *testing.T) {
	db, srv := serverWith(t, FakeRuntime{}, fakeVerifier{sub: "user:github:regular", scope: []string{"runs:kill"}})
	seedAuditRows(t, db, [2]string{"run.kill", "alice"})

	rec := req(t, srv.Handler(), "GET", "/v1/audit")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 without audit:read: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "run.kill") {
		t.Errorf("denied response leaked a row: %s", rec.Body.String())
	}
}

// TestAuditReadWithScopeReturnsRows is the positive control for the test above
// AND the close of BUGS F29: runed's run.kill rows are readable over its API.
func TestAuditReadWithScopeReturnsRows(t *testing.T) {
	db, srv := serverWith(t, FakeRuntime{}, fakeVerifier{sub: "service:dashd", scope: []string{"audit:read"}})
	seedAuditRows(t, db, [2]string{"run.kill", "alice"}, [2]string{"run.hold", "bob"})

	rec := req(t, srv.Handler(), "GET", "/v1/audit")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	rows := decodeAuditRows(t, rec.Body.String())
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2 — an empty result would make every "+
			"containment assertion below meaningless", len(rows))
	}
	if rows[0].Action != "run.hold" {
		t.Errorf("rows[0].Action = %q, want the newest (run.hold)", rows[0].Action)
	}
}

// TestAuditFolderClaimPinsRows: a token bound to a folder sees only that
// subtree, and its own `folder` argument cannot widen the bound.
//
// The adversarial half is the ARGUMENT: the request asks for bob's folder
// explicitly. A test that asked for its own folder would be answered by the
// filter rather than the pin, and would still pass if the pin were deleted.
func TestAuditFolderClaimPinsRows(t *testing.T) {
	db, srv := serverWith(t, FakeRuntime{}, fakeVerifier{
		sub: "service:dashd", scope: []string{"audit:read"}, folder: "alice"})
	seedAuditRows(t, db,
		[2]string{"alice-row", "alice"},
		[2]string{"alice-child-row", "alice/support"},
		[2]string{"bob-row", "bob"})

	rec := req(t, srv.Handler(), "GET", "/v1/audit?folder=bob")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	rows := decodeAuditRows(t, body)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2 (alice + alice/support): %s", len(rows), body)
	}
	// Content-level: assert on the raw body, not on a parsed field a renderer
	// could have dropped.
	if strings.Contains(body, "bob-row") {
		t.Errorf("folder=bob argument widened past the alice claim — cross-tenant leak: %s", body)
	}
	if !strings.Contains(body, "alice-child-row") {
		t.Errorf("subtree row missing; the pin must bound by subtree, not exact match: %s", body)
	}
}

// TestAuditClaimlessTokenReadsInstanceWide: a token with no folder claim reads
// every folder — the dashd federation case. This is the behaviour the recorded
// list-all leak warns about, so it is pinned here deliberately: it is safe ONLY
// because the gate above already proved audit:read, which no human bearer can
// hold (a user token's scopes are folder globs and auth.scopeMatches rejects
// any held value without a colon). TestAuditFolderGlobIsNotAScope is the other
// half of that argument.
func TestAuditClaimlessTokenReadsInstanceWide(t *testing.T) {
	db, srv := serverWith(t, FakeRuntime{}, fakeVerifier{
		sub: "service:dashd", scope: []string{"audit:read"}}) // folder=""
	seedAuditRows(t, db, [2]string{"alice-row", "alice"}, [2]string{"bob-row", "bob"})

	rec := req(t, srv.Handler(), "GET", "/v1/audit")
	rows := decodeAuditRows(t, rec.Body.String())
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want both folders: %s", len(rows), rec.Body.String())
	}
}

// TestAuditFolderGlobIsNotAScope is the load-bearing half of the argument that
// audit:read is operator-only: a USER token carries folder globs in its scope
// list, and none of them — not `acme/**`, not an operator's own `**` — can
// satisfy a resource:verb scope. Without this, "only service:dashd holds
// audit:read" would be a convention rather than a mechanism.
func TestAuditFolderGlobIsNotAScope(t *testing.T) {
	for _, held := range [][]string{{"acme/**"}, {"**"}, {"acme", "beta"}, {}} {
		db, srv := serverWith(t, FakeRuntime{}, fakeVerifier{sub: "user:github:op", scope: held})
		seedAuditRows(t, db, [2]string{"run.kill", "alice"})
		rec := req(t, srv.Handler(), "GET", "/v1/audit")
		if rec.Code != http.StatusForbidden {
			t.Errorf("scope %v got status %d, want 403 — a folder glob must never "+
				"satisfy audit:read: %s", held, rec.Code, rec.Body.String())
		}
	}
}

// TestAuditLimitIsClamped: a caller cannot drain the whole table in one call.
// audit_log grows monotonically (retention is 5/I open question 3), so an
// unclamped limit is a full-history download per request.
func TestAuditLimitIsClamped(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i := range audit.MaxLimit + 10 {
		if _, err := db.SQL().Exec(
			`INSERT INTO audit_log (category, action, actor, outcome)
			 VALUES ('agent', ?, 'op', 'ok')`, "act"+string(rune('a'+i%26))); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := audit.Query(context.Background(), db.SQL(), audit.Filter{Limit: 100000})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != audit.MaxLimit {
		t.Errorf("limit=100000 returned %d rows, want the %d clamp", len(rows), audit.MaxLimit)
	}
}

// TestAuditReadWritesNoRow: reading the log must not append to it. resreg skips
// the audit insert for a successful read; without that, one operator page-load
// would grow the table forever and each refresh would show its own last visit.
func TestAuditReadWritesNoRow(t *testing.T) {
	db, srv := serverWith(t, FakeRuntime{}, fakeVerifier{sub: "service:dashd", scope: []string{"audit:read"}})
	seedAuditRows(t, db, [2]string{"run.kill", "alice"})

	for range 3 {
		if rec := req(t, srv.Handler(), "GET", "/v1/audit"); rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
	}
	var n int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM audit_log`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("audit_log grew to %d rows after 3 reads, want 1 — a read is auditing itself", n)
	}
}

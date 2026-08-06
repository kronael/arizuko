package main

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/audit"
)

// authd is the sole ES256 signer, so its new read surface gets the strictest
// tests in this change: what the column actually contains, who can reach it,
// and that a folder claim cannot be widened.

func auditTestAuthd(t *testing.T) (*sql.DB, *Authd) {
	t.Helper()
	db := testDB(t)
	a, err := newAuthd(db, 15*time.Minute, 30*24*time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return db, a
}

// seedAuthdAudit inserts rows and COUNTS them. The leak tests below assert a
// string is absent from a response, which an empty table satisfies just as
// well — the exact way four audit tests shipped vacuous this week.
func seedAuthdAudit(t *testing.T, db *sql.DB, rows ...[3]string) {
	t.Helper()
	for i, r := range rows {
		if _, err := db.Exec(
			`INSERT INTO audit_log (created_at, category, action, actor, folder, outcome, params_summary)
			 VALUES (?, 'authn', ?, 'user:google:114', ?, 'ok', ?)`,
			"2026-08-01T00:0"+string(rune('0'+i))+":00.000Z", r[0], r[1], r[2]); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_log`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != len(rows) {
		t.Fatalf("fixture wrote %d rows, want %d — every assertion below would "+
			"pass vacuously on an empty table", n, len(rows))
	}
}

func auditGET(t *testing.T, base, path, token string) (int, string) {
	t.Helper()
	req, err := http.NewRequest("GET", base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// TestAuthdAuditRequiresAuditReadScope: a valid bearer WITHOUT audit:read is
// 403, and its body carries no row. The positive control follows so a 403 from
// a broken mount would fail both.
func TestAuthdAuditRequiresAuditReadScope(t *testing.T) {
	db, a := auditTestAuthd(t)
	_, ts := newServer(t, a)
	seedAuthdAudit(t, db, [3]string{"login", "alice", ""})

	// identity:read is a real, currently-granted scope — the adversarial choice.
	// A token with NO scope at all would be rejected by any gate, including a
	// mistakenly-inverted one.
	tok, err := a.MintForSubject("service:routd", "service", nil, []string{"identity:read"}, "")
	if err != nil {
		t.Fatal(err)
	}
	code, body := auditGET(t, ts.URL, "/v1/audit", tok)
	if code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 without audit:read: %s", code, body)
	}
	if strings.Contains(body, "login") {
		t.Errorf("denied response leaked a row: %s", body)
	}
}

// TestAuthdAuditUnauthenticatedIs401: no bearer at all never reaches the gate.
func TestAuthdAuditUnauthenticatedIs401(t *testing.T) {
	db, a := auditTestAuthd(t)
	_, ts := newServer(t, a)
	seedAuthdAudit(t, db, [3]string{"login", "alice", ""})

	code, body := auditGET(t, ts.URL, "/v1/audit", "")
	if code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 with no bearer: %s", code, body)
	}
}

// TestAuthdAuditWithScopeReturnsRows: the close of BUGS F29 for authd — the
// login trail is readable over the API. Positive control for the 403 above.
func TestAuthdAuditWithScopeReturnsRows(t *testing.T) {
	db, a := auditTestAuthd(t)
	_, ts := newServer(t, a)
	seedAuthdAudit(t, db, [3]string{"login", "alice", ""}, [3]string{"daemon.start", "", ""})

	tok, err := a.MintForSubject("service:dashd", "service", nil, serviceGrants["service:dashd"], "")
	if err != nil {
		t.Fatal(err)
	}
	code, body := auditGET(t, ts.URL, "/v1/audit", tok)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", code, body)
	}
	var rows []audit.Row
	if err := json.Unmarshal([]byte(body), &rows); err != nil {
		t.Fatalf("body is not a row array (%v): %s", err, body)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2: %s", len(rows), body)
	}
}

// TestServiceDashdTokenCarriesAuditRead pins the wiring end-to-end: the scope
// the gate demands is one a real service:dashd token actually receives. Without
// this, serviceGrants and the gate could each be individually correct and the
// federation still 403 in production — the exact shape of BUGS F15a.
func TestServiceDashdTokenCarriesAuditRead(t *testing.T) {
	db, a := auditTestAuthd(t)
	_, ts := newServer(t, a)
	seedAuthdAudit(t, db, [3]string{"login", "alice", ""})

	// Minted the way handleServiceToken mints it, from serviceGrants alone.
	tok, err := a.MintForSubject("service:dashd", "service", nil, serviceGrants["service:dashd"], "")
	if err != nil {
		t.Fatal(err)
	}
	if code, body := auditGET(t, ts.URL, "/v1/audit", tok); code != http.StatusOK {
		t.Fatalf("a real service:dashd token got %d — /dash/audit/ would 403 in "+
			"production: %s", code, body)
	}
}

// TestAuthdAuditFolderClaimPinsRows: a folder-bound token cannot widen its
// bound with the `folder` argument. Adversarial by construction — the request
// asks for the OTHER folder; asking for its own would be answered by the plain
// filter and would still pass with the pin deleted.
func TestAuthdAuditFolderClaimPinsRows(t *testing.T) {
	db, a := auditTestAuthd(t)
	_, ts := newServer(t, a)
	seedAuthdAudit(t, db,
		[3]string{"login-alice", "alice", ""},
		[3]string{"login-bob", "bob", ""})

	// IssuerMint stamps arz/folder (folderExtra); that claim is what the
	// handler pins the row filter on.
	m, err := a.IssuerMint("service:dashd", "service", []string{"audit:read"},
		[]string{"audit:read"}, "alice", "", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	code, body := auditGET(t, ts.URL, "/v1/audit?folder=bob", m.token)
	if code != http.StatusOK {
		t.Fatalf("status = %d: %s", code, body)
	}
	var rows []audit.Row
	if err := json.Unmarshal([]byte(body), &rows); err != nil {
		t.Fatalf("body is not a row array (%v): %s", err, body)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want alice's 1: %s", len(rows), body)
	}
	// Content-level, on the raw body.
	if strings.Contains(body, "login-bob") {
		t.Errorf("folder=bob widened past the alice claim — cross-tenant leak: %s", body)
	}
	if !strings.Contains(body, "login-alice") {
		t.Errorf("own-folder row missing: %s", body)
	}
}

// TestAuthdParamsSummaryHasOneWriter is the audit the read surface is owed: it
// pins WHAT authd puts in params_summary, because publishing a column means
// having read it. daemon.start is the only emitter that sets one (the login row
// sets none), and its keys are exactly these three.
//
// A new authd emit site with a new params key fails here, before the column it
// writes into is served to anyone.
func TestAuthdParamsSummaryHasOneWriter(t *testing.T) {
	var src strings.Builder
	for _, f := range []string{"main.go", "oauth.go", "http.go", "server.go", "grants.go", "store.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src.Write(b)
	}
	if n := strings.Count(src.String(), "ParamsSummary:"); n != 1 {
		t.Fatalf("authd sets ParamsSummary at %d sites, want 1 (daemon.start) — "+
			"a new site must be audited before /v1/audit publishes it", n)
	}
	for _, key := range []string{`"dsn"`, `"serving_keys"`, `"service_subs"`} {
		if !strings.Contains(src.String(), key) {
			t.Errorf("expected params key %s missing — the pinned content changed", key)
		}
	}
}

// TestAuthdDSNIsRedactedAtTheWriter: the one params field that was not a count
// never reaches the wire in the clear. The DSN is not key material, but it is a
// host path, it is operator-controlled through DATABASE, and a DSN is where a
// credential would hide — so it is redacted at the writer for every daemon
// (audit.redactRE) rather than filtered at this one read.
func TestAuthdDSNIsRedactedAtTheWriter(t *testing.T) {
	db, a := auditTestAuthd(t)
	_, ts := newServer(t, a)

	const dsn = "/srv/data/arizuko_krons/store/auth.db"
	// Written through the real emitter, not hand-inserted: the claim under test
	// is that the WRITE path redacts.
	if _, err := audit.EmitDB(t.Context(), db, audit.Event{
		Category: audit.CategorySystem, Action: "daemon.start", Actor: "system",
		Outcome: audit.OutcomeOK,
		ParamsSummary: map[string]any{
			"dsn": dsn, "serving_keys": 2, "service_subs": 3,
		},
	}); err != nil {
		t.Fatal(err)
	}
	var stored int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_log`).Scan(&stored); err != nil || stored != 1 {
		t.Fatalf("emit wrote %d rows (err=%v) — the assertion below would be vacuous", stored, err)
	}

	tok, err := a.MintForSubject("service:dashd", "service", nil, []string{"audit:read"}, "")
	if err != nil {
		t.Fatal(err)
	}
	code, body := auditGET(t, ts.URL, "/v1/audit", tok)
	if code != http.StatusOK {
		t.Fatalf("status = %d: %s", code, body)
	}
	if !strings.Contains(body, "serving_keys") {
		t.Fatalf("the row did not reach the wire at all — nothing was tested: %s", body)
	}
	if strings.Contains(body, dsn) {
		t.Errorf("the DSN reached the wire in the clear: %s", body)
	}
	if !strings.Contains(body, "redacted") {
		t.Errorf("dsn should render as <redacted:Nchars>: %s", body)
	}
}

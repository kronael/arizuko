package main

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/resreg/resources"
)

// operatorToken mints a service token holding exactly the given scopes — the
// way handleServiceToken mints one, and with no folder claim.
func operatorToken(t *testing.T, a *Authd, scopes ...string) string {
	t.Helper()
	tok, err := a.MintForSubject("service:dashd", "service", nil, scopes, "")
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func sessionsDELETE(t *testing.T, base, path, token string) (int, string) {
	t.Helper()
	req, err := http.NewRequest("DELETE", base+path, nil)
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

// liveFamilyRows counts a family's rows and how many are unrevoked.
func liveFamilyRows(t *testing.T, db *sql.DB, fam string) (total, live int) {
	t.Helper()
	if err := db.QueryRow(
		`SELECT COUNT(*), COUNT(CASE WHEN revoked_at IS NULL THEN 1 END)
		   FROM refresh_tokens WHERE family_id = ?`, fam).Scan(&total, &live); err != nil {
		t.Fatal(err)
	}
	return total, live
}

// TestSessionsNeverServeTokenValues is the content-level leak test. The raw
// refresh token is held by the test, its sha256 is in the DB, and neither may
// appear in the listing. Asserted on the RAW body, not on a decoded struct — a
// decode into SessionsRow would silently drop an extra field and report clean.
//
// The row count is asserted FIRST: "the body does not contain the token" is
// also true of an empty list.
func TestSessionsNeverServeTokenValues(t *testing.T) {
	_, a := auditTestAuthd(t)
	_, ts := newServer(t, a)

	raw, err := a.IssueRefresh("google:114alice", []string{"acme/**"}, "")
	if err != nil {
		t.Fatal(err)
	}
	hash := hashToken(raw)
	var stored string
	if err := a.db.QueryRow(
		`SELECT token_hash FROM refresh_tokens WHERE token_hash = ?`, hash).Scan(&stored); err != nil {
		t.Fatalf("fixture session not in the DB (%v) — every assertion below "+
			"would pass vacuously", err)
	}

	tok := operatorToken(t, a, "sessions:read")
	code, body := auditGET(t, ts.URL, "/v1/sessions", tok)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", code, body)
	}
	var rows []resources.SessionsRow
	if err := json.Unmarshal([]byte(body), &rows); err != nil {
		t.Fatalf("body is not a row array (%v): %s", err, body)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d sessions, want the 1 issued: %s", len(rows), body)
	}
	if rows[0].Sub != "google:114alice" || rows[0].Status != "active" || rows[0].Rotations != 1 {
		t.Fatalf("session row is wrong: %+v", rows[0])
	}

	for _, needle := range []string{raw, hash, `"token_hash"`, `"used_at"`} {
		if strings.Contains(body, needle) {
			t.Errorf("session listing leaked %q: %s", needle, body)
		}
	}
}

// TestSessionsRequireScope: a valid bearer holding a real, currently-granted
// scope is 403 on both faces. audit:read is the adversarial choice — it is a
// live authd scope, so a gate that merely checked "has any scope" would pass it.
func TestSessionsRequireScope(t *testing.T) {
	_, a := auditTestAuthd(t)
	_, ts := newServer(t, a)
	raw, err := a.IssueRefresh("google:114alice", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	fam := familyOf(t, a.db, raw)

	tok := operatorToken(t, a, "audit:read")
	if code, body := auditGET(t, ts.URL, "/v1/sessions", tok); code != http.StatusForbidden {
		t.Fatalf("list status = %d, want 403 without sessions:read: %s", code, body)
	}
	if code, body := sessionsDELETE(t, ts.URL, "/v1/sessions/"+fam, tok); code != http.StatusForbidden {
		t.Fatalf("delete status = %d, want 403 without sessions:write: %s", code, body)
	}
	// The denial must not have killed anything.
	if _, live := liveFamilyRows(t, a.db, fam); live != 1 {
		t.Fatalf("a 403 revoked the family anyway: %d live rows", live)
	}
}

// TestSessionsReadScopeCannotRevoke: read and write are separate authorities.
// A dashboard that renders the table must not be able to end a session, so the
// token here holds sessions:read and ONLY that.
func TestSessionsReadScopeCannotRevoke(t *testing.T) {
	_, a := auditTestAuthd(t)
	_, ts := newServer(t, a)
	raw, err := a.IssueRefresh("google:114alice", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	fam := familyOf(t, a.db, raw)

	tok := operatorToken(t, a, "sessions:read")
	// Positive control: the same token CAN list, so the 403 below is about the
	// verb and not about a broken mount.
	if code, body := auditGET(t, ts.URL, "/v1/sessions", tok); code != http.StatusOK {
		t.Fatalf("list status = %d, want 200 with sessions:read: %s", code, body)
	}
	if code, body := sessionsDELETE(t, ts.URL, "/v1/sessions/"+fam, tok); code != http.StatusForbidden {
		t.Fatalf("delete status = %d, want 403 with only sessions:read: %s", code, body)
	}
	if _, live := liveFamilyRows(t, a.db, fam); live != 1 {
		t.Fatalf("read-only token revoked the family: %d live rows", live)
	}
}

// TestSessionsRefuseFolderClaim: refresh_tokens is keyed by `sub` and carries
// no folder column, so a folder-bound caller cannot be contained. 403, not a
// silent full listing.
func TestSessionsRefuseFolderClaim(t *testing.T) {
	_, a := auditTestAuthd(t)
	_, ts := newServer(t, a)
	if _, err := a.IssueRefresh("google:114alice", nil, ""); err != nil {
		t.Fatal(err)
	}

	m, err := a.IssuerMint("service:dashd", "service", []string{"sessions:read"},
		[]string{"sessions:read"}, "acme", "", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	code, body := auditGET(t, ts.URL, "/v1/sessions", m.token)
	if code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a folder-bound caller: %s", code, body)
	}
	if strings.Contains(body, "114alice") {
		t.Errorf("denied response leaked a session: %s", body)
	}
}

// TestSessionsRevokeKillsTheFamilyAndAudits is the close of BUGS F15's
// admin-revocation gap, end to end: the operator verb kills the lineage, the
// killed session can no longer refresh, and the act is on the record in auth.db
// — which /dash/audit/ federates since 5/I, the condition F15a named as owed
// before this endpoint could exist.
func TestSessionsRevokeKillsTheFamilyAndAudits(t *testing.T) {
	db, a := auditTestAuthd(t)
	_, ts := newServer(t, a)
	audit.Init(db, "test")
	t.Cleanup(func() { audit.Init(nil, "") })

	raw, err := a.IssueRefresh("google:114alice", []string{"acme/**"}, "")
	if err != nil {
		t.Fatal(err)
	}
	fam := familyOf(t, db, raw)
	if _, live := liveFamilyRows(t, db, fam); live != 1 {
		t.Fatalf("fixture family is not live (%d rows) — the kill below would prove nothing", live)
	}

	tok := operatorToken(t, a, "sessions:read", "sessions:write")
	code, body := sessionsDELETE(t, ts.URL, "/v1/sessions/"+fam, tok)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", code, body)
	}

	total, live := liveFamilyRows(t, db, fam)
	if total != 1 {
		t.Fatalf("want the 1 original row still present, got %d", total)
	}
	if live != 0 {
		t.Fatalf("revoke left %d live row(s)", live)
	}
	// End to end, not just on the column: the killed session is dead on the wire.
	if _, _, err := a.Refresh(t.Context(), raw); err == nil {
		t.Fatal("a revoked session still refreshes")
	}

	// The audit row. Counted before it is read — an empty table would satisfy
	// any "the row says X" assertion written as a scan-and-compare.
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM audit_log WHERE action = 'sessions:delete'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("want exactly 1 sessions:delete audit row, got %d", n)
	}
	var actor, outcome, params, category string
	if err := db.QueryRow(
		`SELECT actor, outcome, params_summary, category FROM audit_log
		  WHERE action = 'sessions:delete'`).Scan(&actor, &outcome, &params, &category); err != nil {
		t.Fatal(err)
	}
	if actor != "service:dashd" || outcome != audit.OutcomeOK || category != audit.CategoryMutation {
		t.Fatalf("audit row = actor %q outcome %q category %q", actor, outcome, category)
	}
	// The row must name WHICH session died — an audit trail of "someone revoked
	// something" is not one.
	if !strings.Contains(params, fam) {
		t.Fatalf("audit params_summary does not name the family: %s", params)
	}
	// And it must not name the credential.
	if strings.Contains(params, raw) || strings.Contains(params, hashToken(raw)) {
		t.Fatalf("audit params_summary leaked the token: %s", params)
	}
}

// TestSessionsRevokeUnknownFamilyIs404: an operator who mistypes an id during
// an incident must not be told the session is dead. Re-revoking an
// already-revoked family answers the same way, and for the same reason —
// nothing was killed by this call.
func TestSessionsRevokeUnknownFamilyIs404(t *testing.T) {
	db, a := auditTestAuthd(t)
	_, ts := newServer(t, a)
	raw, err := a.IssueRefresh("google:114alice", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	fam := familyOf(t, db, raw)
	tok := operatorToken(t, a, "sessions:read", "sessions:write")

	if code, body := sessionsDELETE(t, ts.URL, "/v1/sessions/no-such-family", tok); code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for an unknown family: %s", code, body)
	}
	// The real family is untouched by the miss.
	if _, live := liveFamilyRows(t, db, fam); live != 1 {
		t.Fatalf("a 404 revoked the wrong family: %d live rows", live)
	}

	if code, _ := sessionsDELETE(t, ts.URL, "/v1/sessions/"+fam, tok); code != http.StatusOK {
		t.Fatal("first revoke must succeed")
	}
	if code, body := sessionsDELETE(t, ts.URL, "/v1/sessions/"+fam, tok); code != http.StatusNotFound {
		t.Fatalf("second revoke status = %d, want 404 — nothing was killed: %s", code, body)
	}
}

// TestSessionsRowPerFamilyInvariant pins what selectSessions' `used_at IS NULL`
// WHERE clause relies on: a family has exactly ONE unredeemed row, however many
// times it has rotated. issueRefresh starts one, and each rotation marks the
// presented row used while inserting a single unused successor. If that ever
// forked, the listing would show one session twice and the count would be a lie.
func TestSessionsRowPerFamilyInvariant(t *testing.T) {
	db, a := auditTestAuthd(t)
	raw, err := a.IssueRefresh("google:114alice", []string{"acme/**"}, "")
	if err != nil {
		t.Fatal(err)
	}
	fam := familyOf(t, db, raw)

	for i := range 3 {
		_, next, err := a.Refresh(t.Context(), raw)
		if err != nil {
			t.Fatalf("rotation %d: %v", i, err)
		}
		raw = next
	}

	rows, err := selectSessions(db, "", 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("3 rotations produced %d session rows, want 1 per family: %+v", len(rows), rows)
	}
	if rows[0].FamilyID != fam {
		t.Fatalf("family = %q, want %q", rows[0].FamilyID, fam)
	}
	// The count is the anti-vacuity half: 1 issue + 3 rotations = 4 rows.
	if rows[0].Rotations != 4 {
		t.Fatalf("rotations = %d, want 4 (1 issue + 3 refreshes)", rows[0].Rotations)
	}
	if rows[0].StartedAt == "" || rows[0].RenewedAt == "" {
		t.Fatalf("lifecycle timestamps missing: %+v", rows[0])
	}
}

// TestSessionsSubFilterIsBound: the `sub` filter narrows to one principal and
// does not leak the other. Adversarial — it asks for alice while bob exists,
// so a dropped WHERE shows up as bob in the body.
func TestSessionsSubFilterIsBound(t *testing.T) {
	_, a := auditTestAuthd(t)
	_, ts := newServer(t, a)
	if _, err := a.IssueRefresh("google:114alice", nil, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := a.IssueRefresh("google:220bob", nil, ""); err != nil {
		t.Fatal(err)
	}

	tok := operatorToken(t, a, "sessions:read")
	code, body := auditGET(t, ts.URL, "/v1/sessions?sub=google:114alice", tok)
	if code != http.StatusOK {
		t.Fatalf("status = %d: %s", code, body)
	}
	var rows []resources.SessionsRow
	if err := json.Unmarshal([]byte(body), &rows); err != nil {
		t.Fatalf("body is not a row array (%v): %s", err, body)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want alice's 1: %s", len(rows), body)
	}
	if strings.Contains(body, "220bob") {
		t.Errorf("sub filter leaked the other principal: %s", body)
	}
}

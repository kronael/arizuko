package routd

// Tests for routd's audit read (spec 5/I, BUGS F29): one handler, two faces,
// two injected gates.
//
// The agent socket is where a genuinely non-operator caller exists, so it is
// where folder containment does real work and where these tests concentrate.
// The REST face's gate is the audit:read scope, which no human bearer can hold
// (runed/audit_resource_test.go proves that half against the same evaluator).

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/ipc"

	"github.com/kronael/arizuko/audit"

	"context"
)

// auditFixture opens a routd DB with two sibling groups and a nested child.
// It seeds NO audit rows — grantAt writes its own acl.add rows through the
// audited store path, so the seeding has to happen after the grants. Call
// seedAuditRows once the grants are in.
func auditFixture(t *testing.T) *DB {
	t.Helper()
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, f := range []string{"hq", "hq/support", "rival"} {
		if err := db.PutGroup(core.Group{Folder: f}); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

// seedAuditRows records four known rows — one per folder under test plus a
// folder-less instance row — and asserts it wrote them.
//
// The assertions below count THESE rows by name rather than the whole trail,
// because grantAt goes through the AUDITED acl write and every grant a test
// sets up leaves an acl.add row of its own. Counting the trail would make the
// expected totals a function of how many grants a test happened to need.
//
// The count matters: every containment assertion below is "row X is not in this
// response", which an empty table satisfies. Four audit tests shipped vacuous
// this week for exactly that reason.
func seedAuditRows(t *testing.T, db *DB) {
	t.Helper()
	rows := [][2]string{
		{"hq-own-row", "hq"},
		{"hq-child-row", "hq/support"},
		{"rival-secret-row", "rival"},
		{"instance-wide-row", ""},
	}
	snap := audit.Snapshot{}
	for i, r := range rows {
		snap.Rows = append(snap.Rows, audit.LegacyRow{
			ID:        int64(i + 1),
			CreatedAt: "2026-08-01T00:0" + string(rune('0'+i)) + ":00.000Z",
			Category:  "mutation", Action: r[0], Actor: "github:op",
			Folder: r[1], Outcome: "ok",
		})
	}
	n, err := audit.Import(context.Background(), db.Obj(), snap)
	if err != nil || n != len(rows) {
		t.Fatalf("the fixture wrote %d audit rows, want %d (%v) — every assertion below "+
			"would pass vacuously", n, len(rows), err)
	}
}

// seededRows counts the rows seedAuditRows wrote that came back, by name.
func seededRows(rows []string) int {
	seeded := map[string]bool{
		"hq-own-row": true, "hq-child-row": true,
		"rival-secret-row": true, "instance-wide-row": true,
	}
	n := 0
	for _, a := range rows {
		if seeded[a] {
			n++
		}
	}
	return n
}

// serveAuditMCP stands up the agent socket for folder + the audit resreg seam.
func serveAuditMCP(t *testing.T, db *DB, folder, callerSub string) string {
	t.Helper()
	srv := NewServer(db, nil, nil, nil, 0, "")
	sock := groupfolder.IpcSocket(t.TempDir())
	pb := srv.auditPostBuild(folder, callerSub, srv.turnAuthorize(turnMCP{folder: folder}),
		srv.turnVisible(turnMCP{folder: folder}))
	stop, err := ipc.ServeMCP(sock, srv.buildGatedFns(turnMCP{folder: folder}),
		srv.buildStoreFns(turnMCP{folder: folder}), folder, false, 0, callerSub, pb)
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	t.Cleanup(stop)
	deadline := time.Now().Add(2 * time.Second)
	for !fileExists(sock) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	return sock
}

// TestQueryAuditAgentSeesOnlyItsOwnSubtree is the containment assertion, and it
// is adversarial in the way the recorded near-miss was not: the agent ASKS for
// the rival folder by name. A test that asked for its own folder would be
// answered by the ordinary filter and would still pass with the pin deleted.
func TestQueryAuditAgentSeesOnlyItsOwnSubtree(t *testing.T) {
	db := auditFixture(t)
	grantAt(t, db, "folder:hq", "mcp:query_audit", "hq/**")
	grantAt(t, db, "folder:hq", "mcp:query_audit", "hq")
	seedAuditRows(t, db)
	sock := serveAuditMCP(t, db, "hq", "folder:hq")

	text, e := callToolText(t, sock, "query_audit", map[string]any{"folder": "rival"})
	if e != "" {
		t.Fatalf("query_audit errored: %s", e)
	}
	var rows []struct {
		Action string `json:"action"`
		Folder string `json:"folder"`
	}
	if err := json.Unmarshal([]byte(text), &rows); err != nil {
		t.Fatalf("not a row array (%v): %s", err, text)
	}
	// Own subtree = hq + hq/support. The count is over the fixture's own rows,
	// and it is asserted BEFORE the absence checks below so those cannot pass on
	// an empty result.
	var actions []string
	for _, r := range rows {
		actions = append(actions, r.Action)
	}
	if n := seededRows(actions); n != 2 {
		t.Fatalf("got %d fixture rows, want 2 (hq + hq/support): %s", n, text)
	}
	// Content-level, on the raw response body.
	if strings.Contains(text, "rival-secret-row") {
		t.Errorf("folder=rival widened past the socket folder — cross-tenant leak: %s", text)
	}
	if strings.Contains(text, "instance-wide-row") {
		t.Errorf("folder-less instance row leaked to a tenant: %s", text)
	}
	if !strings.Contains(text, "hq-child-row") {
		t.Errorf("subtree row missing — containment must be by subtree, not exact match: %s", text)
	}
}

// TestQueryAuditDeniedWithoutGrant: the tool is default-deny. The caller is
// granted the action at ANOTHER folder, so the failure mode under test is the
// scope match rather than the absence of any row at all.
func TestQueryAuditDeniedWithoutGrant(t *testing.T) {
	db := auditFixture(t)
	grantAt(t, db, "folder:hq", "mcp:query_audit", "rival/**")
	seedAuditRows(t, db)
	sock := serveAuditMCP(t, db, "hq", "folder:hq")

	text, e := callToolText(t, sock, "query_audit", nil)
	if e == "" {
		t.Fatalf("query_audit should be denied at a foreign scope, got: %s", text)
	}
	if strings.Contains(e, "hq-own-row") || strings.Contains(text, "hq-own-row") {
		t.Errorf("denial leaked a row: %s / %s", e, text)
	}
}

// TestAuditRESTRequiresScope: routd's REST face answers 403 without audit:read,
// with the positive control beside it so a broken mount fails both.
func TestAuditRESTRequiresScope(t *testing.T) {
	db := auditFixture(t)
	seedAuditRows(t, db)
	get := func(scope []string) (int, string) {
		srv := NewServer(db, nil, nil, scopeVerifier{sub: "service:x", scope: scope}, 0, "")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/audit", nil))
		return rec.Code, rec.Body.String()
	}

	code, body := get([]string{"routes:read"})
	if code != http.StatusForbidden {
		t.Fatalf("status = %d without audit:read, want 403: %s", code, body)
	}
	if strings.Contains(body, "hq-own-row") {
		t.Errorf("denied response leaked a row: %s", body)
	}

	code, body = get([]string{"audit:read"})
	if code != http.StatusOK {
		t.Fatalf("status = %d with audit:read, want 200: %s", code, body)
	}
	var rows []struct {
		Action string `json:"action"`
	}
	if err := json.Unmarshal([]byte(body), &rows); err != nil {
		t.Fatalf("not a row array (%v): %s", err, body)
	}
	var actions []string
	for _, r := range rows {
		actions = append(actions, r.Action)
	}
	if n := seededRows(actions); n != 4 {
		t.Fatalf("got %d fixture rows, want all 4 — a claimless service token reads "+
			"instance-wide: %s", n, body)
	}
}

// scopeVerifier is a routd Verifier returning a fixed (sub, scope, folder).
type scopeVerifier struct {
	sub    string
	scope  []string
	folder string
}

func (v scopeVerifier) Verify(*http.Request) (string, []string, string, error) {
	return v.sub, v.scope, v.folder, nil
}

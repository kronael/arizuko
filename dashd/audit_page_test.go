package main

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// auditDB builds an in-memory messages.db with just the audit_log table.
func auditDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE audit_log (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
		category TEXT NOT NULL, action TEXT NOT NULL, actor TEXT NOT NULL,
		actor_sub TEXT, resource TEXT, scope TEXT, surface TEXT,
		params_summary TEXT, outcome TEXT NOT NULL, error_msg TEXT,
		duration_ms INTEGER, turn_id TEXT, folder TEXT, instance TEXT,
		request_id TEXT, source_ip TEXT)`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

// seedAudit inserts an audit_log row with the given core fields.
func seedAudit(t *testing.T, db *sql.DB, category, action, actor, folder, outcome string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO audit_log (created_at, category, action, actor, folder, outcome)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		"2026-06-16T10:00:00Z", category, action, actor, folder, outcome); err != nil {
		t.Fatal(err)
	}
}

func auditGet(t *testing.T, db *sql.DB, url string) string {
	t.Helper()
	d := &dash{db: db}
	mux := http.NewServeMux()
	d.registerRoutes(mux)
	req := asOperator(httptest.NewRequest("GET", url, nil))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d for %s", w.Code, url)
	}
	return w.Body.String()
}

// TestAuditEmpty: an empty audit_log renders the empty-table marker.
func TestAuditEmpty(t *testing.T) {
	db := auditDB(t)
	defer db.Close()
	body := auditGet(t, db, "/dash/audit/")
	if !strings.Contains(body, `class="empty"`) {
		t.Errorf("empty audit log should render the empty marker: %s", body)
	}
}

// TestAuditRows: seeded rows render their columns (action, actor, folder,
// outcome class).
func TestAuditRows(t *testing.T) {
	db := auditDB(t)
	defer db.Close()
	seedAudit(t, db, "mutation", "routd.retry", "rest:dashd", "alice", "ok")
	seedAudit(t, db, "grant", "grant.revoke", "github:op", "bob", "error")

	body := auditGet(t, db, "/dash/audit/")
	for _, want := range []string{"routd.retry", "rest:dashd", "alice", "grant.revoke", "github:op", "bob"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in audit rows: %s", want, body)
		}
	}
	if !strings.Contains(body, "status-ok") || !strings.Contains(body, "status-err") {
		t.Errorf("outcome cells should be color-classed: %s", body)
	}
}

// TestAuditCategoryFilter: ?cat= limits rows to that category and the matching
// <option> renders selected.
func TestAuditCategoryFilter(t *testing.T) {
	db := auditDB(t)
	defer db.Close()
	seedAudit(t, db, "mutation", "routd.retry", "a", "alice", "ok")
	seedAudit(t, db, "grant", "grant.add", "b", "bob", "ok")

	body := auditGet(t, db, "/dash/audit/?cat=grant")
	if !strings.Contains(body, "grant.add") {
		t.Errorf("grant row should be present: %s", body)
	}
	if strings.Contains(body, "routd.retry") {
		t.Errorf("mutation row should be filtered out: %s", body)
	}
	if !strings.Contains(body, `value="grant" selected`) {
		t.Errorf("category dropdown should mark grant selected: %s", body)
	}
}

// TestAuditPagination: 51 rows trigger the "older" link with a before= cursor;
// the cursor then limits to older rows.
func TestAuditPagination(t *testing.T) {
	db := auditDB(t)
	defer db.Close()
	for i := 0; i < 60; i++ {
		seedAudit(t, db, "mutation", fmt.Sprintf("act%d", i), "actor", "f", "ok")
	}

	body := auditGet(t, db, "/dash/audit/")
	if !strings.Contains(body, "older &rarr;") {
		t.Errorf("51+ rows should render an older link: %s", body)
	}
	if !strings.Contains(body, "before=") {
		t.Errorf("older link should carry a before cursor: %s", body)
	}

	// 60 rows total; first page shows the newest 50 (ids 60..11). The 50th row
	// on the page has id 11, so before=11 must return only ids 1..10 = 10 rows.
	page2 := auditGet(t, db, "/dash/audit/?before=11")
	// 10 rows is below the 51 threshold → no further "older" link.
	if strings.Contains(page2, "older &rarr;") {
		t.Errorf("last page should not render an older link: %s", page2)
	}
	if !strings.Contains(page2, "act0") {
		t.Errorf("oldest row act0 should appear on the last page: %s", page2)
	}
}

// TestAuditNonOperatorForbidden: the audit page is operator-only.
func TestAuditNonOperatorForbidden(t *testing.T) {
	db := auditDB(t)
	defer db.Close()
	d := &dash{db: db, dbRoutd: db}
	mux := http.NewServeMux()
	d.registerRoutes(mux)
	req := httptest.NewRequest("GET", "/dash/audit/", nil)
	req.Header.Set("X-User-Sub", "github:regular")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func testDBWithInvites(t *testing.T) *sql.DB {
	t.Helper()
	db := testDB(t)
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS invites (
			token TEXT PRIMARY KEY,
			target_glob TEXT NOT NULL,
			issued_by_sub TEXT NOT NULL,
			issued_at TEXT NOT NULL,
			expires_at TEXT,
			max_uses INTEGER NOT NULL DEFAULT 1,
			used_count INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS acl (
			principal TEXT, action TEXT, scope TEXT, effect TEXT,
			params TEXT, predicate TEXT, granted_at TEXT, granted_by TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS acl_membership (
			child TEXT, parent TEXT, added_at TEXT, added_by TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS audit_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
			category TEXT NOT NULL, action TEXT NOT NULL, actor TEXT NOT NULL,
			actor_sub TEXT, resource TEXT, scope TEXT, surface TEXT,
			params_summary TEXT, outcome TEXT NOT NULL, error_msg TEXT,
			duration_ms INTEGER, turn_id TEXT, folder TEXT, instance TEXT,
			request_id TEXT, source_ip TEXT
		)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("schema: %v", err)
		}
	}
	return db
}

func TestHandleInvitesEmpty(t *testing.T) {
	db := testDBWithInvites(t)
	defer db.Close()
	d := &dash{db: db, dbRW: db, dbRoutd: db, dbOnbod: db}
	mux := http.NewServeMux()
	d.registerRoutes(mux)

	req := httptest.NewRequest("GET", "/dash/invites/", nil)
	req.Header.Set("X-User-Sub", "github:operator")
	req.Header.Set("X-User-Groups", `["**"]`)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, body = %q", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "No invites") {
		t.Errorf("expected empty-state message, got: %q", body)
	}
	if !strings.Contains(body, "Create invite") {
		t.Errorf("expected create form, got: %q", body)
	}
}

func TestHandleInviteCreateAndList(t *testing.T) {
	db := testDBWithInvites(t)
	defer db.Close()
	// Grant operator (**) admin so requireAdmin passes.
	if _, err := db.Exec(
		`INSERT INTO acl (principal, action, scope, effect, params, predicate, granted_at, granted_by)
		 VALUES ('github:operator', 'admin', '**', 'allow', '', '', datetime('now'), 'test')`,
	); err != nil {
		t.Fatal(err)
	}

	d := &dash{db: db, dbRW: db, dbRoutd: db, dbOnbod: db}
	mux := http.NewServeMux()
	d.registerRoutes(mux)

	form := url.Values{}
	form.Set("target_glob", "alice")
	form.Set("max_uses", "3")
	req := httptest.NewRequest("POST", "/dash/invites/",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", "github:operator")
	// No Origin header — requireSameOrigin allows missing-origin (curl / server-to-server).
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("create status = %d, body = %q", w.Code, w.Body.String())
	}

	// List page should now show the invite.
	req2 := httptest.NewRequest("GET", "/dash/invites/", nil)
	req2.Header.Set("X-User-Sub", "github:operator")
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req2)

	if w2.Code != 200 {
		t.Fatalf("list status = %d", w2.Code)
	}
	body := w2.Body.String()
	if !strings.Contains(body, "alice") {
		t.Errorf("invite target not in list: %q", body)
	}
	if strings.Contains(body, "No invites") {
		t.Errorf("expected non-empty list, got empty-state message")
	}
}

func TestHandleInviteCreateForbiddenWithoutAdminGrant(t *testing.T) {
	db := testDBWithInvites(t)
	defer db.Close()
	// No acl row → requireAdmin denies.
	d := &dash{db: db, dbRW: db, dbRoutd: db, dbOnbod: db}
	mux := http.NewServeMux()
	d.registerRoutes(mux)

	form := url.Values{}
	form.Set("target_glob", "bob")
	form.Set("max_uses", "1")
	req := httptest.NewRequest("POST", "/dash/invites/",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Sub", "github:regular")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
}

func TestHandleInviteRevokeAndGone(t *testing.T) {
	db := testDBWithInvites(t)
	defer db.Close()
	if _, err := db.Exec(
		`INSERT INTO acl (principal, action, scope, effect, params, predicate, granted_at, granted_by)
		 VALUES ('github:operator', 'admin', '**', 'allow', '', '', datetime('now'), 'test')`,
	); err != nil {
		t.Fatal(err)
	}
	// Seed one invite directly (token long enough for the [:8] display slice).
	const token = "tok-abcdef0123456789abcdef0123456789"
	if _, err := db.Exec(
		`INSERT INTO invites (token, target_glob, issued_by_sub, issued_at, max_uses, used_count)
		 VALUES (?, 'carol', 'github:operator', datetime('now'), 1, 0)`, token,
	); err != nil {
		t.Fatal(err)
	}

	d := &dash{db: db, dbRW: db, dbRoutd: db, dbOnbod: db}
	mux := http.NewServeMux()
	d.registerRoutes(mux)

	// The revoke form must address the opaque ref, never the raw bearer token —
	// the token in a URL would leak to request / proxy logs.
	req0 := httptest.NewRequest("GET", "/dash/invites/", nil)
	req0.Header.Set("X-User-Sub", "github:operator")
	w0 := httptest.NewRecorder()
	mux.ServeHTTP(w0, req0)
	ref := inviteRef(token)
	list := w0.Body.String()
	if !strings.Contains(list, `action="/dash/invites/`+ref+`/revoke"`) {
		t.Errorf("revoke form does not use the opaque ref: %q", list)
	}
	if strings.Contains(list, `action="/dash/invites/`+token+`/revoke"`) {
		t.Errorf("raw token leaked into the revoke form action")
	}

	req := httptest.NewRequest("POST", "/dash/invites/"+ref+"/revoke", nil)
	req.Header.Set("X-User-Sub", "github:operator")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("revoke status = %d, body = %q", w.Code, w.Body.String())
	}

	// List should be empty.
	req2 := httptest.NewRequest("GET", "/dash/invites/", nil)
	req2.Header.Set("X-User-Sub", "github:operator")
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req2)
	body := w2.Body.String()
	if strings.Contains(body, token) {
		t.Errorf("revoked token still visible: %q", body)
	}
	if !strings.Contains(body, "No invites") {
		t.Errorf("expected empty-state after revoke: %q", body)
	}
}

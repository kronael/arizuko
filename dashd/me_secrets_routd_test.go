package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// secretsSchema is the minimal secrets table dashd reads/writes (mirrors
// store's secrets migration). Used to stand up an isolated routd.db twin.
const secretsSchema = `CREATE TABLE secrets (
	scope_kind TEXT NOT NULL,
	scope_id   TEXT NOT NULL,
	key        TEXT NOT NULL,
	value      TEXT NOT NULL,
	created_at TEXT NOT NULL,
	PRIMARY KEY (scope_kind, scope_id, key)
);`

// splitSecretsDash wires a dash with DISTINCT messages.db (dbRW) and routd.db
// (dbRoutd) handles, each with its own secrets table, so a write can be proven to
// land in routd.db and NOT in messages.db.
func splitSecretsDash(t *testing.T) (*dash, *sql.DB, *sql.DB) {
	t.Helper()
	msg, err := sql.Open("sqlite", "file:dash_msg?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := msg.Exec(secretsSchema + `CREATE TABLE audit_log (
		id INTEGER PRIMARY KEY AUTOINCREMENT, created_at TEXT, category TEXT, action TEXT,
		actor TEXT, actor_sub TEXT, resource TEXT, scope TEXT, surface TEXT,
		params_summary TEXT, outcome TEXT, error_msg TEXT, duration_ms INTEGER,
		turn_id TEXT, folder TEXT, instance TEXT, request_id TEXT, source_ip TEXT);`); err != nil {
		t.Fatalf("messages.db schema: %v", err)
	}
	routd, err := sql.Open("sqlite", "file:dash_routd?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := routd.Exec(secretsSchema); err != nil {
		t.Fatalf("routd.db schema: %v", err)
	}
	t.Cleanup(func() { msg.Close(); routd.Close() })
	return &dash{db: msg, dbRW: msg, dbRoutd: routd}, msg, routd
}

// TestMeSecrets_WriteTargetsRoutdDB proves /dash/me/secrets POST writes the row
// into routd.db (dbRoutd) and NOT messages.db (dbRW) in the split topology.
func TestMeSecrets_WriteTargetsRoutdDB(t *testing.T) {
	d, msg, routd := splitSecretsDash(t)
	mux := newMux(d)

	req := httptest.NewRequest("POST", "/dash/me/secrets",
		strings.NewReader(`{"key":"GITHUB_TOKEN","value":"ghp_split"}`))
	req.Header.Set("X-User-Sub", "github:alice")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("POST = %d body=%q", w.Code, w.Body.String())
	}

	var n int
	routd.QueryRow(`SELECT COUNT(*) FROM secrets WHERE scope_id='github:alice' AND key='GITHUB_TOKEN'`).Scan(&n)
	if n != 1 {
		t.Errorf("routd.db secret rows = %d, want 1", n)
	}
	n = -1
	msg.QueryRow(`SELECT COUNT(*) FROM secrets WHERE key='GITHUB_TOKEN'`).Scan(&n)
	if n != 0 {
		t.Errorf("messages.db secret rows = %d, want 0 (dashd must not write the monolith)", n)
	}
}

// TestMeSecrets_DeleteTargetsRoutdDB proves the delete path also hits routd.db.
func TestMeSecrets_DeleteTargetsRoutdDB(t *testing.T) {
	d, _, routd := splitSecretsDash(t)
	mux := newMux(d)

	post := httptest.NewRequest("POST", "/dash/me/secrets",
		strings.NewReader(`{"key":"API_KEY","value":"v"}`))
	post.Header.Set("X-User-Sub", "github:bob")
	mux.ServeHTTP(httptest.NewRecorder(), post)

	del := httptest.NewRequest("DELETE", "/dash/me/secrets/API_KEY", nil)
	del.Header.Set("X-User-Sub", "github:bob")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, del)
	if w.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d body=%q", w.Code, w.Body.String())
	}
	var n int
	routd.QueryRow(`SELECT COUNT(*) FROM secrets WHERE scope_id='github:bob'`).Scan(&n)
	if n != 0 {
		t.Errorf("routd.db secret rows after delete = %d, want 0", n)
	}
}

package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// splitSecretsDash wires a dash on routd.db — the only store dashd opens — where
// routd's own chain supplies both `secrets` and the audit_log sink (migration
// 0016), so a sealed write can be proven to land there.
func splitSecretsDash(t *testing.T) (*dash, *sql.DB) {
	t.Helper()
	db := routdDB(t)
	return &dash{dbRoutd: db}, db
}

func TestMeSecrets_WriteTargetsRoutdDB(t *testing.T) {
	d, routd := splitSecretsDash(t)
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
}

// TestMeSecrets_DeleteTargetsRoutdDB proves the delete path also hits routd.db.
func TestMeSecrets_DeleteTargetsRoutdDB(t *testing.T) {
	d, routd := splitSecretsDash(t)
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

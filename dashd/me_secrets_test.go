package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// meSecretsTestDB seeds the secrets table dashd reads/writes. dashd does not
// own migrations; this fixture mirrors store/migrations/0034-secrets.sql post
// 0047-secrets-plaintext.sql rename.
func meSecretsTestDB(t *testing.T) *dash {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`CREATE TABLE secrets (
			scope_kind TEXT NOT NULL,
			scope_id   TEXT NOT NULL,
			key        TEXT NOT NULL,
			value      TEXT NOT NULL,
			created_at TEXT NOT NULL,
			PRIMARY KEY (scope_kind, scope_id, key)
		)`,
	); err != nil {
		t.Fatalf("schema: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return &dash{db: db, dbRW: db}
}

func newMux(d *dash) *http.ServeMux {
	mux := http.NewServeMux()
	d.registerRoutes(mux)
	return mux
}

func TestMeSecrets_ListEmpty(t *testing.T) {
	d := meSecretsTestDB(t)
	mux := newMux(d)
	req := httptest.NewRequest("GET", "/dash/me/secrets", nil)
	req.Header.Set("X-User-Sub", "github:alice")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d, body = %q", w.Code, w.Body.String())
	}
	var resp struct {
		Secrets []map[string]string `json:"secrets"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Secrets) != 0 {
		t.Errorf("want empty, got %v", resp.Secrets)
	}
}

func TestMeSecrets_RequiresAuth(t *testing.T) {
	d := meSecretsTestDB(t)
	mux := newMux(d)
	for _, m := range []string{"GET", "POST", "PATCH", "DELETE"} {
		path := "/dash/me/secrets"
		if m == "PATCH" || m == "DELETE" {
			path += "/GITHUB_TOKEN"
		}
		req := httptest.NewRequest(m, path, strings.NewReader(`{"value":"x"}`))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: status = %d, want 401", m, path, w.Code)
		}
	}
}

func TestMeSecrets_CreateAndList(t *testing.T) {
	d := meSecretsTestDB(t)
	mux := newMux(d)
	req := httptest.NewRequest("POST", "/dash/me/secrets",
		strings.NewReader(`{"key":"GITHUB_TOKEN","value":"ghp_xxx"}`))
	req.Header.Set("X-User-Sub", "github:alice")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("POST status = %d, body = %q", w.Code, w.Body.String())
	}

	req = httptest.NewRequest("GET", "/dash/me/secrets", nil)
	req.Header.Set("X-User-Sub", "github:alice")
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	var resp struct {
		Secrets []struct {
			Key       string `json:"key"`
			CreatedAt string `json:"created_at"`
			Value     string `json:"value"`
		} `json:"secrets"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Secrets) != 1 || resp.Secrets[0].Key != "GITHUB_TOKEN" {
		t.Fatalf("list = %+v", resp.Secrets)
	}
	if resp.Secrets[0].Value != "" {
		t.Errorf("value must not be returned in list, got %q", resp.Secrets[0].Value)
	}
}

func TestMeSecrets_Update(t *testing.T) {
	d := meSecretsTestDB(t)
	mux := newMux(d)
	post := httptest.NewRequest("POST", "/dash/me/secrets",
		strings.NewReader(`{"key":"GITHUB_TOKEN","value":"v1"}`))
	post.Header.Set("X-User-Sub", "github:alice")
	mux.ServeHTTP(httptest.NewRecorder(), post)

	patch := httptest.NewRequest("PATCH", "/dash/me/secrets/GITHUB_TOKEN",
		strings.NewReader(`{"value":"v2"}`))
	patch.Header.Set("X-User-Sub", "github:alice")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, patch)
	if w.Code != http.StatusNoContent {
		t.Fatalf("PATCH status = %d, body = %q", w.Code, w.Body.String())
	}

	var got string
	if err := d.dbRW.QueryRow(
		`SELECT value FROM secrets WHERE scope_kind='user' AND scope_id='github:alice' AND key='GITHUB_TOKEN'`,
	).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "v2" {
		t.Errorf("value = %q, want v2", got)
	}
}

func TestMeSecrets_UpdateMissing(t *testing.T) {
	d := meSecretsTestDB(t)
	mux := newMux(d)
	req := httptest.NewRequest("PATCH", "/dash/me/secrets/NEVER_SET",
		strings.NewReader(`{"value":"v"}`))
	req.Header.Set("X-User-Sub", "github:alice")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestMeSecrets_Delete(t *testing.T) {
	d := meSecretsTestDB(t)
	mux := newMux(d)
	post := httptest.NewRequest("POST", "/dash/me/secrets",
		strings.NewReader(`{"key":"GITHUB_TOKEN","value":"v"}`))
	post.Header.Set("X-User-Sub", "github:alice")
	mux.ServeHTTP(httptest.NewRecorder(), post)

	req := httptest.NewRequest("DELETE", "/dash/me/secrets/GITHUB_TOKEN", nil)
	req.Header.Set("X-User-Sub", "github:alice")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d", w.Code)
	}
	var n int
	d.dbRW.QueryRow(`SELECT COUNT(*) FROM secrets WHERE scope_id='github:alice'`).Scan(&n)
	if n != 0 {
		t.Errorf("rows after delete = %d, want 0", n)
	}
}

func TestMeSecrets_CrossUser_CannotReadOthers(t *testing.T) {
	d := meSecretsTestDB(t)
	mux := newMux(d)

	// Alice writes
	post := httptest.NewRequest("POST", "/dash/me/secrets",
		strings.NewReader(`{"key":"PRIVATE","value":"alice"}`))
	post.Header.Set("X-User-Sub", "github:alice")
	mux.ServeHTTP(httptest.NewRecorder(), post)

	// Bob lists
	req := httptest.NewRequest("GET", "/dash/me/secrets", nil)
	req.Header.Set("X-User-Sub", "github:bob")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if strings.Contains(w.Body.String(), "PRIVATE") {
		t.Errorf("bob saw alice's keys: %s", w.Body.String())
	}

	// Bob cannot update
	patch := httptest.NewRequest("PATCH", "/dash/me/secrets/PRIVATE",
		strings.NewReader(`{"value":"bob_wins"}`))
	patch.Header.Set("X-User-Sub", "github:bob")
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, patch)
	if w.Code != http.StatusNotFound {
		t.Errorf("bob update foreign key: status = %d, want 404", w.Code)
	}

	// Alice's value is unchanged
	var got string
	d.dbRW.QueryRow(
		`SELECT value FROM secrets WHERE scope_kind='user' AND scope_id='github:alice' AND key='PRIVATE'`,
	).Scan(&got)
	if got != "alice" {
		t.Errorf("alice value = %q, want alice", got)
	}
}

func TestMeSecrets_RejectsBadKeys(t *testing.T) {
	d := meSecretsTestDB(t)
	mux := newMux(d)
	cases := []string{
		`{"key":"lowercase","value":"v"}`,
		`{"key":"123LEADING","value":"v"}`,
		`{"key":"WITH-DASH","value":"v"}`,
		`{"key":"WITH SPACE","value":"v"}`,
		`{"key":"","value":"v"}`,
	}
	for _, body := range cases {
		req := httptest.NewRequest("POST", "/dash/me/secrets", strings.NewReader(body))
		req.Header.Set("X-User-Sub", "github:alice")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("body=%s: status = %d, want 400", body, w.Code)
		}
	}
}

func TestMeSecrets_RejectsEmptyValue(t *testing.T) {
	d := meSecretsTestDB(t)
	mux := newMux(d)
	req := httptest.NewRequest("POST", "/dash/me/secrets",
		strings.NewReader(`{"key":"K","value":""}`))
	req.Header.Set("X-User-Sub", "github:alice")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestMeSecrets_CSRF_RejectsCrossOrigin(t *testing.T) {
	d := meSecretsTestDB(t)
	mux := newMux(d)
	req := httptest.NewRequest("POST", "/dash/me/secrets",
		strings.NewReader(`{"key":"K","value":"v"}`))
	req.Host = "dash.example.com"
	req.Header.Set("X-User-Sub", "github:alice")
	req.Header.Set("Origin", "https://evil.example.com")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("cross-origin: status = %d, want 403", w.Code)
	}
}

func TestMeSecrets_CSRF_AllowsSameOrigin(t *testing.T) {
	d := meSecretsTestDB(t)
	mux := newMux(d)
	req := httptest.NewRequest("POST", "/dash/me/secrets",
		strings.NewReader(`{"key":"K","value":"v"}`))
	req.Host = "dash.example.com"
	req.Header.Set("X-User-Sub", "github:alice")
	req.Header.Set("Origin", "https://dash.example.com")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Errorf("same-origin: status = %d, body = %q", w.Code, w.Body.String())
	}
}

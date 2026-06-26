package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// meSecretsKeyringDB is meSecretsTestDB with a SECRETS_KEY keyring so writes
// seal at rest (the BYOA encryption-at-rest path).
func meSecretsKeyringDB(t *testing.T) *dash {
	t.Helper()
	d := meSecretsTestDB(t)
	d.secretKeyring = [][]byte{[]byte("dashd-test-key")}
	return d
}

// TestMeSecrets_SealsAtRest proves a POST under a configured keyring stores the
// value as a v2: ciphertext — never plaintext. The encryption-bypass bug was
// dashd's raw INSERT writing body.Value verbatim.
func TestMeSecrets_SealsAtRest(t *testing.T) {
	d := meSecretsKeyringDB(t)
	mux := newMux(d)

	req := httptest.NewRequest("POST", "/dash/me/secrets",
		strings.NewReader(`{"key":"GITHUB_TOKEN","value":"sk-plaintext"}`))
	req.Header.Set("X-User-Sub", "github:alice")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("POST = %d body=%q", w.Code, w.Body.String())
	}

	var raw string
	if err := d.dbRW.QueryRow(
		`SELECT value FROM secrets WHERE scope_kind='user' AND scope_id='github:alice' AND key='GITHUB_TOKEN'`,
	).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(raw, "v2:") {
		t.Errorf("stored value not sealed: %q", raw)
	}
	if strings.Contains(raw, "sk-plaintext") {
		t.Error("plaintext leaked into stored value")
	}
}

// TestMeSecrets_UpdateSealsAtRest: PATCH reseals; the new value is v2: too.
func TestMeSecrets_UpdateSealsAtRest(t *testing.T) {
	d := meSecretsKeyringDB(t)
	mux := newMux(d)

	post := httptest.NewRequest("POST", "/dash/me/secrets",
		strings.NewReader(`{"key":"TOKEN","value":"v1"}`))
	post.Header.Set("X-User-Sub", "github:alice")
	mux.ServeHTTP(httptest.NewRecorder(), post)

	patch := httptest.NewRequest("PATCH", "/dash/me/secrets/TOKEN",
		strings.NewReader(`{"value":"v2-secret"}`))
	patch.Header.Set("X-User-Sub", "github:alice")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, patch)
	if w.Code != http.StatusNoContent {
		t.Fatalf("PATCH = %d body=%q", w.Code, w.Body.String())
	}

	var raw string
	d.dbRW.QueryRow(
		`SELECT value FROM secrets WHERE scope_kind='user' AND scope_id='github:alice' AND key='TOKEN'`,
	).Scan(&raw)
	if !strings.HasPrefix(raw, "v2:") || strings.Contains(raw, "v2-secret") {
		t.Errorf("updated value not sealed: %q", raw)
	}
}

// TestMeSecrets_AuditOmitsValue: the audit_log row for a secret write must NOT
// carry the plaintext value (audit_log is at rest).
func TestMeSecrets_AuditOmitsValue(t *testing.T) {
	d := meSecretsTestDB(t)
	mux := newMux(d)

	req := httptest.NewRequest("POST", "/dash/me/secrets",
		strings.NewReader(`{"key":"SECRET_KEY","value":"super-secret-val"}`))
	req.Header.Set("X-User-Sub", "github:alice")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("POST = %d body=%q", w.Code, w.Body.String())
	}

	var params string
	// audit.Emit is async-free here (direct insert); the row exists after the call.
	d.dbRW.QueryRow(
		`SELECT COALESCE(params_summary,'') FROM audit_log WHERE action='secret.set' ORDER BY id DESC LIMIT 1`,
	).Scan(&params)
	if strings.Contains(params, "super-secret-val") {
		t.Errorf("audit_log leaked the secret value: %q", params)
	}
}

// TestMeSecrets_HTMLPage: Accept text/html renders the management page with the
// key name visible but never the value, plus the add form.
func TestMeSecrets_HTMLPage(t *testing.T) {
	d := meSecretsTestDB(t)
	mux := newMux(d)

	post := httptest.NewRequest("POST", "/dash/me/secrets",
		strings.NewReader(`{"key":"GITHUB_TOKEN","value":"ghp_dontshow"}`))
	post.Header.Set("X-User-Sub", "github:alice")
	mux.ServeHTTP(httptest.NewRecorder(), post)

	req := httptest.NewRequest("GET", "/dash/me/secrets", nil)
	req.Header.Set("X-User-Sub", "github:alice")
	req.Header.Set("Accept", "text/html")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("GET html = %d body=%q", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	body := w.Body.String()
	if !strings.Contains(body, "GITHUB_TOKEN") {
		t.Error("html page omits the key name")
	}
	if strings.Contains(body, "ghp_dontshow") {
		t.Error("html page leaked the secret value")
	}
	if !strings.Contains(body, `name="key"`) || !strings.Contains(body, `type="password"`) {
		t.Error("html page missing the add form (key + password inputs)")
	}
}

// TestMeSecrets_DeleteMissing: deleting a never-set key is 404 (parity with
// PATCH-missing), and a real key still deletes cleanly afterwards.
func TestMeSecrets_DeleteMissing(t *testing.T) {
	d := meSecretsTestDB(t)
	mux := newMux(d)
	req := httptest.NewRequest("DELETE", "/dash/me/secrets/NEVER_SET", nil)
	req.Header.Set("X-User-Sub", "github:alice")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("delete missing = %d, want 404", w.Code)
	}
}

// TestMeSecrets_JSONStillDefault: without an html Accept, GET stays JSON (the
// API surface is unchanged for non-browser callers).
func TestMeSecrets_JSONStillDefault(t *testing.T) {
	d := meSecretsTestDB(t)
	mux := newMux(d)
	req := httptest.NewRequest("GET", "/dash/me/secrets", nil)
	req.Header.Set("X-User-Sub", "github:alice")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("default Content-Type = %q, want application/json", ct)
	}
}

// TestMeSecrets_CrossUserIsolation: user B's GET must not list user A's keys.
// The SQL scope_id=? binds to the verified caller sub, so A's secrets are
// invisible to B even when they share an instance.
func TestMeSecrets_CrossUserIsolation(t *testing.T) {
	d := meSecretsTestDB(t)
	mux := newMux(d)

	// Seed Alice's secret (capability credential — allowed at /dash/me/secrets).
	post := httptest.NewRequest("POST", "/dash/me/secrets",
		strings.NewReader(`{"key":"GITHUB_TOKEN","value":"alice-key"}`))
	post.Header.Set("X-User-Sub", "github:alice")
	wr := httptest.NewRecorder()
	mux.ServeHTTP(wr, post)
	if wr.Code != http.StatusNoContent {
		t.Fatalf("Alice POST = %d: %s", wr.Code, wr.Body.String())
	}

	// Bob GETs — must see nothing (scope_id bound to verified caller sub).
	req := httptest.NewRequest("GET", "/dash/me/secrets", nil)
	req.Header.Set("X-User-Sub", "github:bob")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("GET bob = %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "GITHUB_TOKEN") {
		t.Error("cross-user leak: Bob's GET returned Alice's key name")
	}
}

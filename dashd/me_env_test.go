package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/store"
)

// /dash/me/env is the env-profile half of the spec 5/14 credential model:
// ANTHROPIC_API_KEY and its three siblings, user scope only. Its twin
// /dash/me/secrets carries capability credentials and rejects these key names;
// this file covers the reverse guard plus the write path itself, which shipped
// untested. It reuses meSecretsTestDB/meSecretsKeyringDB/newMux — one fixture
// for both halves, since they share the secrets table and the same handlers'
// shape.

// envReq issues a JSON request to /dash/me/env as sub and returns the recorder.
// An empty sub omits X-User-Sub (the unauthenticated probe).
func envReq(t *testing.T, mux *http.ServeMux, method, path, sub, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if sub != "" {
		req.Header.Set("X-User-Sub", sub)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

// secretValue returns the stored (at-rest) value of a user secret, or "" when
// no row exists.
func secretValue(t *testing.T, d *dash, sub, key string) string {
	t.Helper()
	var v string
	err := d.dbRoutd.QueryRow(
		`SELECT value FROM secrets WHERE scope_kind='user' AND scope_id=? AND key=?`,
		sub, key).Scan(&v)
	if err != nil && err != sql.ErrNoRows {
		t.Fatal(err)
	}
	return v
}

// lockedBuf is an io.Writer a slog handler can share with the test goroutine.
type lockedBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureSlog redirects the default logger into a buffer for the test, at Debug
// so no level threshold can hide a leaking record.
func captureSlog(t *testing.T) *lockedBuf {
	t.Helper()
	b := &lockedBuf{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(b, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return b
}

// auditDump returns (rowCount, every column of every audit_log row joined). It
// reads columns generically so a value leaking into ANY column — not just
// params_summary — is caught, including a column added later.
func auditDump(t *testing.T, db *sql.DB) (int, string) {
	t.Helper()
	rows, err := db.Query(`SELECT * FROM audit_log ORDER BY id`)
	if err != nil {
		t.Fatalf("read audit_log: %v", err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	n := 0
	for rows.Next() {
		cells := make([]any, len(cols))
		for i := range cells {
			cells[i] = new(sql.NullString)
		}
		if err := rows.Scan(cells...); err != nil {
			t.Fatal(err)
		}
		for _, c := range cells {
			out.WriteString(c.(*sql.NullString).String)
			out.WriteByte('\n')
		}
		n++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return n, out.String()
}

func TestMeEnv_RequiresAuth(t *testing.T) {
	mux := newMux(meSecretsTestDB(t))
	for _, m := range []string{"GET", "POST", "PATCH", "DELETE"} {
		path := "/dash/me/env"
		if m == "PATCH" || m == "DELETE" {
			path += "/ANTHROPIC_API_KEY"
		}
		if w := envReq(t, mux, m, path, "", `{"value":"x"}`); w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: status = %d, want 401", m, path, w.Code)
		}
	}
}

// TestMeEnv_CreateListDelete is the write path end to end: POST stores the key,
// GET lists its name (never its value), DELETE removes it.
func TestMeEnv_CreateListDelete(t *testing.T) {
	d := meSecretsTestDB(t)
	mux := newMux(d)

	if w := envReq(t, mux, "POST", "/dash/me/env", "github:alice",
		`{"key":"ANTHROPIC_API_KEY","value":"sk-ant-aaa"}`); w.Code != http.StatusNoContent {
		t.Fatalf("POST = %d body=%q", w.Code, w.Body.String())
	}

	w := envReq(t, mux, "GET", "/dash/me/env", "github:alice", "")
	if w.Code != 200 {
		t.Fatalf("GET = %d body=%q", w.Code, w.Body.String())
	}
	var resp struct {
		Env []struct {
			Key       string `json:"key"`
			CreatedAt string `json:"created_at"`
			Value     string `json:"value"`
		} `json:"env"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Env) != 1 || resp.Env[0].Key != "ANTHROPIC_API_KEY" {
		t.Fatalf("list = %+v, want the one set key", resp.Env)
	}
	if resp.Env[0].Value != "" || strings.Contains(w.Body.String(), "sk-ant-aaa") {
		t.Errorf("list leaked the value: %s", w.Body.String())
	}
	if resp.Env[0].CreatedAt == "" {
		t.Error("list omits created_at")
	}

	if w := envReq(t, mux, "DELETE", "/dash/me/env/ANTHROPIC_API_KEY", "github:alice", ""); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d body=%q", w.Code, w.Body.String())
	}
	if v := secretValue(t, d, "github:alice", "ANTHROPIC_API_KEY"); v != "" {
		t.Errorf("row survived delete: %q", v)
	}
	if w := envReq(t, mux, "DELETE", "/dash/me/env/ANTHROPIC_API_KEY", "github:alice", ""); w.Code != http.StatusNotFound {
		t.Errorf("second DELETE = %d, want 404", w.Code)
	}
}

// TestMeEnv_UpdateReplacesValue: PATCH is update-only — 404 on a never-set key,
// and it replaces the stored value on a set one.
func TestMeEnv_UpdateReplacesValue(t *testing.T) {
	d := meSecretsTestDB(t)
	mux := newMux(d)

	if w := envReq(t, mux, "PATCH", "/dash/me/env/OPENAI_API_KEY", "github:alice",
		`{"value":"v"}`); w.Code != http.StatusNotFound {
		t.Fatalf("PATCH never-set = %d, want 404", w.Code)
	}
	envReq(t, mux, "POST", "/dash/me/env", "github:alice", `{"key":"OPENAI_API_KEY","value":"v1"}`)
	if w := envReq(t, mux, "PATCH", "/dash/me/env/OPENAI_API_KEY", "github:alice",
		`{"value":"v2"}`); w.Code != http.StatusNoContent {
		t.Fatalf("PATCH = %d body=%q", w.Code, w.Body.String())
	}
	if got := secretValue(t, d, "github:alice", "OPENAI_API_KEY"); got != "v2" {
		t.Errorf("value = %q, want v2", got)
	}
	// A body key that contradicts the path is rejected rather than silently
	// writing to one of the two.
	if w := envReq(t, mux, "PATCH", "/dash/me/env/OPENAI_API_KEY", "github:alice",
		`{"key":"CODEX_API_KEY","value":"v3"}`); w.Code != http.StatusBadRequest {
		t.Errorf("mismatched body key = %d, want 400", w.Code)
	}
}

// TestMeEnv_CrossGuard is the /dash/me/env ↔ /dash/me/secrets guard from spec
// 5/14 "Write paths": each surface rejects the other's key class on every write
// verb, names the right page, and writes nothing.
func TestMeEnv_CrossGuard(t *testing.T) {
	d := meSecretsTestDB(t)
	mux := newMux(d)

	// A capability credential is refused by /dash/me/env on every write verb.
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/dash/me/env", `{"key":"GITHUB_TOKEN","value":"ghp_x"}`},
		{"PATCH", "/dash/me/env/GITHUB_TOKEN", `{"value":"ghp_x"}`},
		{"DELETE", "/dash/me/env/GITHUB_TOKEN", ""},
	} {
		w := envReq(t, mux, c.method, c.path, "github:alice", c.body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s %s = %d, want 400", c.method, c.path, w.Code)
		}
		if !strings.Contains(w.Body.String(), "GITHUB_TOKEN") ||
			!strings.Contains(w.Body.String(), "not an env-profile key") {
			t.Errorf("%s %s: error %q does not name the rejected key", c.method, c.path, w.Body.String())
		}
	}
	// Only POST points the caller at the other page (PATCH/DELETE stop at the key
	// name — BUGS F20); the form only ever POSTs, so that is the reachable message.
	if w := envReq(t, mux, "POST", "/dash/me/env", "github:alice",
		`{"key":"GITHUB_TOKEN","value":"ghp_x"}`); !strings.Contains(w.Body.String(), "/dash/me/secrets") {
		t.Errorf("POST rejection %q does not point at /dash/me/secrets", w.Body.String())
	}
	if v := secretValue(t, d, "github:alice", "GITHUB_TOKEN"); v != "" {
		t.Errorf("rejected env write still stored a capability key: %q", v)
	}

	// Every env-profile key is refused by /dash/me/secrets, pointed at /dash/me/env.
	for key := range store.EnvProfileKeys {
		w := envReq(t, mux, "POST", "/dash/me/secrets", "github:alice",
			`{"key":"`+key+`","value":"sk-x"}`)
		if w.Code != http.StatusBadRequest {
			t.Errorf("POST /dash/me/secrets %s = %d, want 400", key, w.Code)
		}
		if !strings.Contains(w.Body.String(), "/dash/me/env") {
			t.Errorf("%s: error %q does not point at /dash/me/env", key, w.Body.String())
		}
		if v := secretValue(t, d, "github:alice", key); v != "" {
			t.Errorf("rejected secrets write still stored %s: %q", key, v)
		}
	}
}

// TestMeEnv_ScopeValidationRejectsBadScope covers the store-layer enforcement the
// env page's writes go through (spec 5/14: "the enforcement point, not a handler
// convention"). PutSecretRow is the exact call handleMeEnvCreate makes.
func TestMeEnv_ScopeValidationRejectsBadScope(t *testing.T) {
	d := meSecretsTestDB(t)
	ss := d.secretStore()
	cases := []struct {
		name    string
		scope   store.SecretScope
		scopeID string
		key     string
	}{
		{"env-profile key at folder scope", store.ScopeFolder, "team", "ANTHROPIC_API_KEY"},
		{"unknown scope kind", store.SecretScope("group"), "team", "SOME_KEY"},
		{"empty scope id", store.ScopeUser, "", "ANTHROPIC_API_KEY"},
		{"empty key", store.ScopeUser, "github:alice", ""},
	}
	for _, c := range cases {
		if err := ss.PutSecretRow(c.scope, c.scopeID, c.key, "sk-rejected"); err == nil {
			t.Errorf("%s: PutSecretRow accepted a bad scope", c.name)
		}
		if err := ss.DeleteSecretRow(c.scope, c.scopeID, c.key); err == nil {
			t.Errorf("%s: DeleteSecretRow accepted a bad scope", c.name)
		}
	}
	var n int
	if err := d.dbRoutd.QueryRow(`SELECT COUNT(*) FROM secrets`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("rejected writes still landed %d rows", n)
	}
}

// TestMeEnv_NoPlaintextInLogsOrAudit: neither a log line nor an audit_log row
// may carry the credential the write is about. The audit rows are asserted to
// EXIST first — "no canary in audit_log" is worthless if nothing was written.
func TestMeEnv_NoPlaintextInLogsOrAudit(t *testing.T) {
	const canary = "sk-ant-ENV_PLAINTEXT_CANARY_4d21"
	d := meSecretsTestDB(t)
	audit.Init(d.dbRoutd, "test")
	t.Cleanup(func() { audit.Init(nil, "") })
	logs := captureSlog(t)
	mux := newMux(d)

	if w := envReq(t, mux, "POST", "/dash/me/env", "github:alice",
		`{"key":"ANTHROPIC_API_KEY","value":"`+canary+`"}`); w.Code != http.StatusNoContent {
		t.Fatalf("POST = %d body=%q", w.Code, w.Body.String())
	}
	if w := envReq(t, mux, "PATCH", "/dash/me/env/ANTHROPIC_API_KEY", "github:alice",
		`{"value":"`+canary+`-patched"}`); w.Code != http.StatusNoContent {
		t.Fatalf("PATCH = %d body=%q", w.Code, w.Body.String())
	}
	if w := envReq(t, mux, "DELETE", "/dash/me/env/ANTHROPIC_API_KEY", "github:alice",
		""); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d body=%q", w.Code, w.Body.String())
	}

	// Two secret.set rows (create + update) and one secret.delete must exist —
	// otherwise the leak assertion below reads an empty table and proves nothing.
	rows, dump := auditDump(t, d.dbRoutd)
	if rows != 3 {
		t.Fatalf("audit_log rows = %d, want 3 (secret.set x2 + secret.delete):\n%s", rows, dump)
	}
	if !strings.Contains(dump, "secret.set") || !strings.Contains(dump, "secret.delete") {
		t.Fatalf("audit_log missing the env write actions:\n%s", dump)
	}
	if strings.Contains(dump, canary) {
		t.Errorf("audit_log carries the credential value:\n%s", dump)
	}

	// Same for the logs. The key name and sub are fine; the value is not.
	out := logs.String()
	if !strings.Contains(out, "ANTHROPIC_API_KEY") {
		t.Fatalf("no me_env log records captured — the leak assertion would be vacuous:\n%s", out)
	}
	if strings.Contains(out, canary) {
		t.Errorf("log line carries the credential value:\n%s", out)
	}
}

// TestMeEnv_SealsAtRest: under a configured SECRETS_KEY keyring the env write
// lands as a v2: ciphertext, the same encoding routd decrypts at spawn.
func TestMeEnv_SealsAtRest(t *testing.T) {
	d := meSecretsKeyringDB(t)
	mux := newMux(d)
	if w := envReq(t, mux, "POST", "/dash/me/env", "github:alice",
		`{"key":"CLAUDE_CODE_OAUTH_TOKEN","value":"oauth-plaintext"}`); w.Code != http.StatusNoContent {
		t.Fatalf("POST = %d body=%q", w.Code, w.Body.String())
	}
	raw := secretValue(t, d, "github:alice", "CLAUDE_CODE_OAUTH_TOKEN")
	if !strings.HasPrefix(raw, "v2:") || strings.Contains(raw, "oauth-plaintext") {
		t.Errorf("stored value not sealed: %q", raw)
	}
}

// TestMeEnv_CrossUserIsolation: the scope_id binds to the verified caller sub,
// so Bob neither sees nor overwrites Alice's model key.
func TestMeEnv_CrossUserIsolation(t *testing.T) {
	d := meSecretsTestDB(t)
	mux := newMux(d)
	envReq(t, mux, "POST", "/dash/me/env", "github:alice", `{"key":"CODEX_API_KEY","value":"alice"}`)

	w := envReq(t, mux, "GET", "/dash/me/env", "github:bob", "")
	if strings.Contains(w.Body.String(), "CODEX_API_KEY") {
		t.Errorf("bob's list shows alice's key: %s", w.Body.String())
	}
	if w := envReq(t, mux, "PATCH", "/dash/me/env/CODEX_API_KEY", "github:bob",
		`{"value":"bob"}`); w.Code != http.StatusNotFound {
		t.Errorf("bob PATCH of alice's key = %d, want 404", w.Code)
	}
	if w := envReq(t, mux, "DELETE", "/dash/me/env/CODEX_API_KEY", "github:bob",
		""); w.Code != http.StatusNotFound {
		t.Errorf("bob DELETE of alice's key = %d, want 404", w.Code)
	}
	if got := secretValue(t, d, "github:alice", "CODEX_API_KEY"); got != "alice" {
		t.Errorf("alice's value = %q, want alice", got)
	}
}

// TestMeEnv_CSRF: a cross-origin write is rejected; the same-origin one passes.
func TestMeEnv_CSRF(t *testing.T) {
	mux := newMux(meSecretsTestDB(t))
	for _, c := range []struct {
		origin string
		want   int
	}{
		{"https://evil.example.com", http.StatusForbidden},
		{"https://dash.example.com", http.StatusNoContent},
	} {
		req := httptest.NewRequest("POST", "/dash/me/env",
			strings.NewReader(`{"key":"ANTHROPIC_API_KEY","value":"v"}`))
		req.Host = "dash.example.com"
		req.Header.Set("X-User-Sub", "github:alice")
		req.Header.Set("Origin", c.origin)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != c.want {
			t.Errorf("Origin %s: status = %d, want %d (%s)", c.origin, w.Code, c.want, w.Body.String())
		}
	}
}

// TestMeEnv_HTMLPage: the browser surface lists every env-profile key, marks the
// unset ones as falling back to the platform key, and never renders a value.
func TestMeEnv_HTMLPage(t *testing.T) {
	d := meSecretsTestDB(t)
	mux := newMux(d)
	envReq(t, mux, "POST", "/dash/me/env", "github:alice", `{"key":"ANTHROPIC_API_KEY","value":"sk-dontshow"}`)

	req := httptest.NewRequest("GET", "/dash/me/env", nil)
	req.Header.Set("X-User-Sub", "github:alice")
	req.Header.Set("Accept", "text/html")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("GET html = %d body=%q", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for key := range store.EnvProfileKeys {
		if !strings.Contains(body, key) {
			t.Errorf("html page omits %s", key)
		}
	}
	if strings.Contains(body, "sk-dontshow") {
		t.Error("html page rendered the credential value")
	}
	if !strings.Contains(body, "platform key active") {
		t.Error("html page does not mark the unset keys as falling back to the platform key")
	}
}

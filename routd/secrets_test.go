package routd

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// doDelete issues a DELETE against h (no body) and returns the recorder.
func doDelete(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("DELETE", path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestSecretSetEndpoint: POST /v1/secrets (secrets:write) seals + writes a
// folder secret into routd's OWN routd.db, and the connector-injection read
// (FolderSecrets) decrypts it back — proving the write lands where reads look.
func TestSecretSetEndpoint(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "service:dashd", scope: []string{"secrets:write"}})
	db.SetSecretKeys([]byte("endpoint-key")) // seal-on-write + decrypt-on-read

	rec := doJSON(t, h, "POST", "/v1/secrets", "", secretWriteBody{
		Scope: "folder", ScopeID: "main/eng", Key: "GITHUB_TOKEN", Value: "ghp_via_http"})
	if rec.Code != 200 {
		t.Fatalf("POST /v1/secrets = %d want 200 body=%s", rec.Code, rec.Body.String())
	}

	// Read it back the way connector injection does.
	if got := db.FolderSecrets("main/eng")["GITHUB_TOKEN"]; got != "ghp_via_http" {
		t.Errorf("FolderSecrets after POST = %q, want ghp_via_http", got)
	}
	// Sealed at rest (v2:), never plaintext on disk.
	var raw string
	if err := db.SQL().QueryRow(
		`SELECT value FROM secrets WHERE scope_id='main/eng' AND key='GITHUB_TOKEN'`).Scan(&raw); err != nil {
		t.Fatalf("read raw: %v", err)
	}
	if !strings.HasPrefix(raw, "v2:") {
		t.Errorf("stored value not sealed: %q", raw)
	}
}

// TestSecretDeleteEndpoint: DELETE /v1/secrets/{key} removes the row; a second
// delete 404s (no such secret).
func TestSecretDeleteEndpoint(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "service:dashd", scope: []string{"secrets:write"}})
	db.SetSecretKeys([]byte("k"))

	if rec := doJSON(t, h, "POST", "/v1/secrets", "", secretWriteBody{
		Scope: "folder", ScopeID: "main", Key: "API_KEY", Value: "v"}); rec.Code != 200 {
		t.Fatalf("seed POST = %d body=%s", rec.Code, rec.Body.String())
	}

	rec := doDelete(t, h, "/v1/secrets/API_KEY?scope=folder&scope_id=main")
	if rec.Code != 200 {
		t.Fatalf("DELETE = %d want 200 body=%s", rec.Code, rec.Body.String())
	}
	if got := db.FolderSecrets("main"); len(got) != 0 {
		t.Errorf("secret survived delete: %v", got)
	}
	// Second delete → 404.
	if rec := doDelete(t, h, "/v1/secrets/API_KEY?scope=folder&scope_id=main"); rec.Code != 404 {
		t.Fatalf("second DELETE = %d want 404 body=%s", rec.Code, rec.Body.String())
	}
}

// TestSecretSetRequiresWriteScope: a token without secrets:write is 403.
func TestSecretSetRequiresWriteScope(t *testing.T) {
	_, h := authSrv(t, fakeVerifier{sub: "user:u", scope: []string{"routes:read"}})
	rec := doJSON(t, h, "POST", "/v1/secrets", "", secretWriteBody{
		Scope: "folder", ScopeID: "main", Key: "K", Value: "v"})
	if rec.Code != 403 {
		t.Fatalf("POST without secrets:write = %d want 403 body=%s", rec.Code, rec.Body.String())
	}
}

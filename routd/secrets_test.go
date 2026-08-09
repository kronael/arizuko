package routd

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// REST-face tests for the spec 5/16 secrets fold: POST /v1/secrets (create =
// set/seal) and DELETE /v1/secrets/{key} now ride the shared secretsHandler via
// resreg.RegisterREST — a FORWARDER (Store nil) so resreg writes no audit row and
// opens no tx; the existing sealing, audited s.db.SetSecret/DeleteSecret own the
// write. The value never enters any read surface: the *NoLeak tests assert the
// response body AND audit_log never carry the plaintext, on the allow and the
// deny path. The *Containment test is the security tightening (the acl-fold hole,
// closed here): a folder-scoped caller is bound to its own subtree. There is NO
// read/list twin and NO agent MCP tool.

// secretReq is the POST /v1/secrets body after the fold (the flat args the shared
// handler reads). DELETE addresses the row by {key} + ?scope=&scope_id=.
type secretReq struct {
	Scope   string `json:"scope"`
	ScopeID string `json:"scope_id"`
	Key     string `json:"key"`
	Value   string `json:"value"`
}

// doDelete issues a DELETE against h (no body) and returns the recorder.
func doDelete(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("DELETE", path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// auditParamsHasCanary reports whether any audit_log row's params_summary
// mentions the plaintext canary — the leak assertion's read surface.
func auditParamsHasCanary(t *testing.T, db *DB, canary string) bool {
	t.Helper()
	var n int
	if err := db.SQL().QueryRow(
		`SELECT COUNT(*) FROM audit_log WHERE params_summary LIKE '%' || ? || '%'`, canary).Scan(&n); err != nil {
		t.Fatalf("scan audit_log: %v", err)
	}
	return n > 0
}

// TestSecretSetEndpoint: POST /v1/secrets (secrets:write) seals + writes a
// folder secret into routd's OWN routd.db, and the connector-injection read
// (FolderSecrets) decrypts it back — proving the write lands where reads look.
func TestSecretSetEndpoint(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "service:dashd", scope: []string{"secrets:write"}})
	db.SetSecretKeys([]byte("endpoint-key")) // seal-on-write + decrypt-on-read

	rec := doJSON(t, h, "POST", "/v1/secrets", "", secretReq{
		Scope: "folder", ScopeID: "main/eng", Key: "GITHUB_TOKEN", Value: "ghp_via_http"})
	if rec.Code != 200 {
		t.Fatalf("POST /v1/secrets = %d want 200 body=%s", rec.Code, rec.Body.String())
	}

	// Read it back the way connector injection does.
	if got := db.folderSecrets("main/eng")["GITHUB_TOKEN"]; got != "ghp_via_http" {
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

	if rec := doJSON(t, h, "POST", "/v1/secrets", "", secretReq{
		Scope: "folder", ScopeID: "main", Key: "API_KEY", Value: "v"}); rec.Code != 200 {
		t.Fatalf("seed POST = %d body=%s", rec.Code, rec.Body.String())
	}

	rec := doDelete(t, h, "/v1/secrets/API_KEY?scope=folder&scope_id=main")
	if rec.Code != 200 {
		t.Fatalf("DELETE = %d want 200 body=%s", rec.Code, rec.Body.String())
	}
	if got := db.folderSecrets("main"); len(got) != 0 {
		t.Errorf("secret survived delete: %v", got)
	}
	// Second delete → 404.
	if rec := doDelete(t, h, "/v1/secrets/API_KEY?scope=folder&scope_id=main"); rec.Code != 404 {
		t.Fatalf("second DELETE = %d want 404 body=%s", rec.Code, rec.Body.String())
	}
}

// TestSecretSetRequiresWriteScope: a token without secrets:write is 403 (the
// Authz scope check), and nothing is written.
func TestSecretSetRequiresWriteScope(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "user:u", scope: []string{"routes:read"}})
	db.SetSecretKeys([]byte("k"))
	rec := doJSON(t, h, "POST", "/v1/secrets", "", secretReq{
		Scope: "folder", ScopeID: "main", Key: "K", Value: "v"})
	if rec.Code != 403 {
		t.Fatalf("POST without secrets:write = %d want 403 body=%s", rec.Code, rec.Body.String())
	}
	if got := db.folderSecrets("main"); len(got) != 0 {
		t.Errorf("denied POST still wrote a secret: %v", got)
	}
}

// TestSecretEnvProfileKeyAtFolderRejected: an env-profile credential (belongs to
// users only) at folder scope is rejected 400 by store.validateScope, and no row
// lands. The caller is root (empty folder) so it clears Authz — the 400 is the
// store validation, not containment.
func TestSecretEnvProfileKeyAtFolderRejected(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "service:dashd", scope: []string{"secrets:write"}})
	db.SetSecretKeys([]byte("k"))
	rec := doJSON(t, h, "POST", "/v1/secrets", "", secretReq{
		Scope: "folder", ScopeID: "main", Key: "ANTHROPIC_API_KEY", Value: "sk-x"})
	if rec.Code != 400 {
		t.Fatalf("env-profile key at folder scope = %d want 400 body=%s", rec.Code, rec.Body.String())
	}
	if got := db.folderSecrets("main"); len(got) != 0 {
		t.Errorf("rejected env-profile POST still wrote a secret: %v", got)
	}
}

// TestSecretRESTContainmentDenied is the security tightening (spec 5/16): the
// folded Authz binds the target scope to the caller's authority, so a folder-
// scoped caller may write only WITHIN its own subtree. A tier-1 caller ("world/a")
// writes its own descendant (allowed) but is DENIED a cross-world folder scope —
// closing the pre-fold hole where handleSecretSet gated on secrets:write ALONE.
// FAILS (the cross-world POST returns 200 + writes a row) if secretsAuthz's
// ownsFolder check is dropped.
func TestSecretRESTContainmentDenied(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "user:wa", scope: []string{"secrets:write"}, folder: "world/a"})
	db.SetSecretKeys([]byte("k"))

	// Own subtree: allowed, row written.
	if rec := doJSON(t, h, "POST", "/v1/secrets", "", secretReq{
		Scope: "folder", ScopeID: "world/a/sub", Key: "K", Value: "v"}); rec.Code != 200 {
		t.Fatalf("own-subtree POST = %d want 200 body=%s", rec.Code, rec.Body.String())
	}
	if got := db.folderSecrets("world/a/sub")["K"]; got != "v" {
		t.Fatalf("own-subtree POST did not write the secret (got %q)", got)
	}
	// Cross-world: DENIED (403), nothing written.
	if rec := doJSON(t, h, "POST", "/v1/secrets", "", secretReq{
		Scope: "folder", ScopeID: "other/x", Key: "K", Value: "v"}); rec.Code != 403 {
		t.Fatalf("cross-world POST = %d want 403 body=%s", rec.Code, rec.Body.String())
	}
	if got := db.folderSecrets("other/x"); len(got) != 0 {
		t.Fatal("denied cross-world POST still wrote a secret")
	}
	// Cross-world DELETE is denied too (containment on both write verbs).
	if err := db.SetSecret("folder", "other/x", "K", "seed"); err != nil {
		t.Fatal(err)
	}
	if rec := doDelete(t, h, "/v1/secrets/K?scope=folder&scope_id=other/x"); rec.Code != 403 {
		t.Fatalf("cross-world DELETE = %d want 403 body=%s", rec.Code, rec.Body.String())
	}
	if got := db.folderSecrets("other/x")["K"]; got != "seed" {
		t.Fatal("denied cross-world DELETE still dropped the row")
	}
}

// TestSecretRESTNoLeakOnSuccess: a successful POST NEVER echoes the plaintext in
// the response body, and NEVER writes it to audit_log — the sealed row + the
// value-safe secret.set audit row are the only trace (the forwarder guarantee).
func TestSecretRESTNoLeakOnSuccess(t *testing.T) {
	const canary = "PLAINTEXT_LEAK_CANARY_9f3a"
	db, h := authSrv(t, fakeVerifier{sub: "service:dashd", scope: []string{"secrets:write"}})
	db.SetSecretKeys([]byte("k"))

	rec := doJSON(t, h, "POST", "/v1/secrets", "", secretReq{
		Scope: "folder", ScopeID: "main", Key: "GITHUB_TOKEN", Value: canary})
	if rec.Code != 200 {
		t.Fatalf("POST = %d want 200 body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), canary) {
		t.Fatalf("response body leaked the plaintext value: %s", rec.Body.String())
	}
	// Stored sealed, not plaintext.
	var raw string
	if err := db.SQL().QueryRow(
		`SELECT value FROM secrets WHERE scope_id='main' AND key='GITHUB_TOKEN'`).Scan(&raw); err != nil {
		t.Fatalf("read raw: %v", err)
	}
	if strings.Contains(raw, canary) || !strings.HasPrefix(raw, "v2:") {
		t.Fatalf("stored value not sealed / leaked plaintext: %q", raw)
	}
	// audit_log carries the value-safe secret.set row, never the plaintext.
	if auditParamsHasCanary(t, db, canary) {
		t.Fatal("audit_log params_summary leaked the plaintext value")
	}
}

// TestSecretRESTNoLeakOnDeny: even a DENIED POST (wrong scope) must not write the
// plaintext into audit_log — the forwarder short-circuits resreg's audit before
// any row is written, and store.SetSecret is never reached.
func TestSecretRESTNoLeakOnDeny(t *testing.T) {
	const canary = "DENY_LEAK_CANARY_71bc"
	db, h := authSrv(t, fakeVerifier{sub: "user:u", scope: []string{"routes:read"}})
	db.SetSecretKeys([]byte("k"))

	rec := doJSON(t, h, "POST", "/v1/secrets", "", secretReq{
		Scope: "folder", ScopeID: "main", Key: "K", Value: canary})
	if rec.Code != 403 {
		t.Fatalf("denied POST = %d want 403 body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), canary) {
		t.Fatalf("denied response body leaked the plaintext value: %s", rec.Body.String())
	}
	if auditParamsHasCanary(t, db, canary) {
		t.Fatal("denied POST leaked the plaintext value into audit_log")
	}
}

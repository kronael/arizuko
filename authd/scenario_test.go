package main

// Daemon-level + HTTP-integration security scenarios for authd, the system's
// token authority (specs/5/1). These spin a real httptest server on the authd
// mux and verify through the go-oidc RemoteKeySet network path (auth.FetchKeys)
// — the same code every backend runs — so a regression in key serving,
// rotation windows, refresh rotation, or cross-deployment isolation fails here.
//
// The in-process unit coverage lives in authd_test.go; this file owns the
// network-path and end-to-end lifecycle assertions that unit tests cannot make.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
)

// openSecondDB opens a distinct in-memory DB (a second, independent authd
// deployment with its own keypair) — separate cache name so it does not alias
// testDB's shared cache.
func openSecondDB(t *testing.T) (*sql.DB, error) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.Name()+"-B?mode=memory&cache=shared&_pragma=foreign_keys(on)")
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { db.Close() })
	if err := migrate(db); err != nil {
		return nil, err
	}
	return db, nil
}

func newServer(t *testing.T, a *Authd) (*server, *httptest.Server) {
	t.Helper()
	srv := &server{a: a, serviceSecrets: map[string]string{"boot-timed": "service:timed"}}
	ts := httptest.NewServer(srv.mux())
	t.Cleanup(ts.Close)
	return srv, ts
}

// fetchVerify is a cold verifier: a fresh RemoteKeySet over the live /v1/keys,
// exactly what a just-started backend does. Cold each call so we observe the
// CURRENT published key set, not a stale cache.
func fetchVerify(t *testing.T, baseURL, token string) error {
	t.Helper()
	ks, err := auth.FetchKeys(context.Background(), baseURL)
	if err != nil {
		t.Fatalf("fetch keys: %v", err)
	}
	_, err = auth.VerifyToken(token, ks)
	return err
}

func postJSON(t *testing.T, url string, body any) (*http.Response, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if resp.Body != nil {
		_ = json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
	}
	return resp, out
}

// postServiceToken exchanges via the spec 5/1 §435 shape: secret in the
// Authorization header, daemon name in the body.
func postServiceToken(t *testing.T, url, daemon, secret string) (*http.Response, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"daemon": daemon})
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if resp.Body != nil {
		_ = json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
	}
	return resp, out
}

// HTTP-integration: a freshly minted token verifies end-to-end through
// FetchKeys (RemoteKeySet hits /v1/keys over the wire). specs/5/1 "offline
// verification ... pure function over (token, JWKs)".
func TestHTTPFetchKeysVerifiesMintedToken(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	_, ts := newServer(t, a)

	tok, err := a.MintForSubject("user:9", nil, []string{"tasks:read"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := fetchVerify(t, ts.URL, tok); err != nil {
		t.Fatalf("freshly minted token must verify over the network path: %v", err)
	}
}

// HTTP-integration: a token minted by server A must be REJECTED by a verifier
// pointed at server B (a different deployment with different keys). Guards the
// blast-radius boundary — one mint authority does not authorize against
// another. specs/5/1 single-issuer model.
func TestHTTPTokenRejectedByWrongServersKeys(t *testing.T) {
	dbA := testDB(t)
	aA := newTestAuthd(t, dbA)
	_, tsA := newServer(t, aA)

	// Second, independent authd with its own keypair (distinct DB).
	dbB, err := openSecondDB(t)
	if err != nil {
		t.Fatal(err)
	}
	aB := newTestAuthd(t, dbB)
	_, tsB := newServer(t, aB)

	tok, _ := aA.MintForSubject("user:1", nil, []string{"tasks:read"}, "")
	// Sanity: A's token verifies against A.
	if err := fetchVerify(t, tsA.URL, tok); err != nil {
		t.Fatalf("control: A-token must verify against A: %v", err)
	}
	// The property: A's token must NOT verify against B's keys.
	if err := fetchVerify(t, tsB.URL, tok); err == nil {
		t.Fatal("a token from server A must be rejected by server B's key set")
	}
}

// Key lifecycle (network path): rotate, then a token signed by the now-retired
// key still verifies while that key remains in /v1/keys (within the serving
// window). specs/5/1 "Overlap window ... old-kid tokens verify until they'd
// have expired anyway".
func TestHTTPRetiredKeyVerifiesWithinWindow(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db) // maxAccessTTL = 1h, so a just-retired key stays serving
	_, ts := newServer(t, a)

	oldTok, _ := a.MintForSubject("user:1", nil, []string{"a:read"}, "")
	if err := a.Rotate(); err != nil {
		t.Fatal(err)
	}
	// Both kids are published; the old token must still verify over the wire.
	if err := fetchVerify(t, ts.URL, oldTok); err != nil {
		t.Fatalf("token from a recently-retired key must verify within the window: %v", err)
	}
}

// Key lifecycle (network path): once the retired key falls out of its serving
// window it is no longer published, so a cold verifier rejects an old-key
// token. Models the "≤ JWKS cache TTL" purge after which old-kid tokens fail
// (specs/5/1 "Overlap window").
func TestHTTPRetiredKeyFallsOutOfWindow(t *testing.T) {
	db := testDB(t)
	// maxAccessTTL tiny: the key retired by Rotate is instantly past its window.
	a, err := newAuthd(db, time.Minute, time.Hour, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	_, ts := newServer(t, a)

	oldTok, _ := a.MintForSubject("user:1", nil, []string{"a:read"}, "")
	if err := a.Rotate(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if err := a.reload(); err != nil { // drops the out-of-window key from `serving`
		t.Fatal(err)
	}
	if err := fetchVerify(t, ts.URL, oldTok); err == nil {
		t.Fatal("a token whose key fell out of the serving window must not verify on a cold verifier")
	}
}

// Emergency revoke (network path): RevokeAllNow drops the old kid from /v1/keys
// immediately, so a cold verifier rejects every token signed by the retired
// key — the single lever that invalidates everything at once. specs/5/1
// "EMERGENCY revoke ... rotate the signing key".
func TestHTTPRevokeAllNowStopsOldTokens(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	_, ts := newServer(t, a)

	oldTok, _ := a.MintForSubject("user:1", nil, []string{"a:read"}, "")
	if err := fetchVerify(t, ts.URL, oldTok); err != nil {
		t.Fatalf("control: token must verify before revoke: %v", err)
	}
	if err := a.RevokeAllNow(); err != nil {
		t.Fatal(err)
	}
	if err := fetchVerify(t, ts.URL, oldTok); err == nil {
		t.Fatal("RevokeAllNow must immediately stop the old token on a cold verifier")
	}
}

// Refresh (HTTP end-to-end): a successful /v1/refresh returns a rotated refresh
// token AND a fresh, verifiable access token; replaying the SPENT refresh
// token over HTTP triggers family revocation, and the live successor is killed
// too. specs/5/1 "Refresh-token rotation (one-time-use) ... reuse signal ...
// invalidates the entire family".
func TestHTTPRefreshReuseKillsLiveSuccessor(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	_, ts := newServer(t, a)

	r0, err := a.IssueRefresh("user:1", []string{"tasks:read"}, "")
	if err != nil {
		t.Fatal(err)
	}

	// First rotation succeeds and yields a verifiable access + successor refresh.
	resp, out := postJSON(t, ts.URL+"/v1/refresh", map[string]string{"refresh_token": r0})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first refresh status %d", resp.StatusCode)
	}
	r1, _ := out["refresh_token"].(string)
	access, _ := out["access_token"].(string)
	if r1 == "" || r1 == r0 {
		t.Fatalf("refresh must rotate; got r1=%q", r1)
	}
	if err := fetchVerify(t, ts.URL, access); err != nil {
		t.Fatalf("access from refresh must verify: %v", err)
	}

	// Replaying the spent r0 is reuse → 401, and the family is now revoked.
	resp2, _ := postJSON(t, ts.URL+"/v1/refresh", map[string]string{"refresh_token": r0})
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("replay of spent refresh must be 401, got %d", resp2.StatusCode)
	}
	// The live successor r1 must now be dead — family invalidation killed it.
	resp3, _ := postJSON(t, ts.URL+"/v1/refresh", map[string]string{"refresh_token": r1})
	if resp3.StatusCode != http.StatusUnauthorized {
		t.Fatalf("live successor must be revoked after family kill, got %d", resp3.StatusCode)
	}
}

// Refresh (HTTP): an unknown / never-issued refresh token is rejected with 401,
// never minting an access token. specs/5/1 "A missing/expired refresh token
// returns 401".
func TestHTTPRefreshUnknownTokenRejected(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	_, ts := newServer(t, a)

	resp, out := postJSON(t, ts.URL+"/v1/refresh", map[string]string{"refresh_token": "never-issued"})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unknown refresh token must be 401, got %d", resp.StatusCode)
	}
	if _, ok := out["access_token"]; ok {
		t.Fatal("a rejected refresh must not return an access token")
	}
}

// Refresh (HTTP): an EXPIRED refresh token is rejected — an expired session
// cannot silently mint new access. specs/5/1 TTL table "Refresh token 30 days".
func TestHTTPRefreshExpiredRejected(t *testing.T) {
	db := testDB(t)
	// refreshTTL negative → the issued refresh token is already expired.
	a, err := newAuthd(db, 15*time.Minute, -time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	_, ts := newServer(t, a)

	r0, _ := a.IssueRefresh("user:1", []string{"a:read"}, "")
	resp, _ := postJSON(t, ts.URL+"/v1/refresh", map[string]string{"refresh_token": r0})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expired refresh token must be 401, got %d", resp.StatusCode)
	}
}

// Service-token (HTTP, scope bounding): the minted service token carries
// EXACTLY the principal's declared serviceGrants — not the full vocabulary, not
// the union of all daemons. A leaked boot secret buys only that one daemon's
// scope. specs/5/1 "the daemon's declared service capability set".
func TestHTTPServiceTokenBoundToDeclaredGrants(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	_, ts := newServer(t, a)

	resp, out := postServiceToken(t, ts.URL+"/v1/service-token", "timed", "boot-timed")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("service-token status %d", resp.StatusCode)
	}
	access, _ := out["token"].(string)
	ks := auth.NewKeySet(a.PublicKeys())
	sub, err := auth.VerifyToken(access, ks)
	if err != nil {
		t.Fatalf("service token invalid: %v", err)
	}
	// timed's declared grants are exactly {messages:write, tasks:read}.
	if !auth.HasScope(sub.Scope, "messages", "write") || !auth.HasScope(sub.Scope, "tasks", "read") {
		t.Fatalf("service:timed must hold its declared grants, got %v", sub.Scope)
	}
	// It must NOT hold another principal's scope (e.g. onbod's groups:write).
	if auth.HasScope(sub.Scope, "groups", "write") {
		t.Fatalf("service:timed must not gain onbod's groups:write, got %v", sub.Scope)
	}
	if sub.Sub != "service:timed" {
		t.Fatalf("service token sub = %q", sub.Sub)
	}
}

// Scope bounding (issuer mint): MintForSubject is bounded by the TARGET's
// grants, not the request — asking for more than the target is granted silently
// drops the extra (delegation, never escalation). specs/5/1 "POST /v1/tokens"
// issuer mint: "requested scope ⊆ that snapshot".
func TestMintForSubjectDropsScopeBeyondGrants(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)

	// Target granted only tasks:read; request tasks:read + secrets:read + admin:*.
	tok, err := a.MintForSubject("user:1",
		[]string{"tasks:read", "secrets:read", "admin:write"},
		[]string{"tasks:read"}, "")
	if err != nil {
		t.Fatal(err)
	}
	ks := auth.NewKeySet(a.PublicKeys())
	sub, _ := auth.VerifyToken(tok, ks)
	if !auth.HasScope(sub.Scope, "tasks", "read") {
		t.Fatalf("granted tasks:read must survive, got %v", sub.Scope)
	}
	if auth.HasScope(sub.Scope, "secrets", "read") || auth.HasScope(sub.Scope, "admin", "write") {
		t.Fatalf("scope beyond the target's grants must be dropped, got %v", sub.Scope)
	}
}

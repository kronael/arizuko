package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
	_ "modernc.org/sqlite"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared&_pragma=foreign_keys(on)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func newTestAuthd(t *testing.T, db *sql.DB) *Authd {
	t.Helper()
	a, err := newAuthd(db, 15*time.Minute, time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestBootGeneratesActiveKey(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	if k, err := a.activeKey(); err != nil || k == nil {
		t.Fatalf("expected an active key, got %v %v", k, err)
	}
	if len(a.PublicKeys()) != 1 {
		t.Fatalf("expected 1 serving key, got %d", len(a.PublicKeys()))
	}
}

func TestMintVerifyThroughAuthd(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	tok, err := a.MintForSubject("service:timed", nil, []string{"tasks:read"}, "")
	if err != nil {
		t.Fatal(err)
	}
	ks := auth.NewKeySet(a.PublicKeys())
	sub, err := auth.VerifyToken(tok, ks)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if sub.Sub != "service:timed" || !auth.HasScope(sub.Scope, "tasks", "read") {
		t.Fatalf("bad subject %+v", sub)
	}
}

func TestAuthdMintNarrowerDownscope(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	// Agent parent token holds tasks:read+write; downscope to read-only.
	tok, err := a.MintNarrower("agent:sub", []string{"tasks:read", "tasks:write"}, []string{"tasks:read"}, "")
	if err != nil {
		t.Fatal(err)
	}
	ks := auth.NewKeySet(a.PublicKeys())
	sub, _ := auth.VerifyToken(tok, ks)
	if !auth.HasScope(sub.Scope, "tasks", "read") || auth.HasScope(sub.Scope, "tasks", "write") {
		t.Fatalf("downscoped token scope wrong: %v", sub.Scope)
	}
	// Asking for more than the parent has must fail.
	if _, err := a.MintNarrower("agent:sub", []string{"tasks:read"}, []string{"secrets:read"}, ""); err == nil {
		t.Fatal("escalation beyond parent scope must error")
	}
}

func TestRotationKeepsOldKeyServingWithinWindow(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db) // maxAccessTTL = 1h
	oldTok, _ := a.MintForSubject("user:1", nil, []string{"a:read"}, "")

	if err := a.Rotate(); err != nil {
		t.Fatal(err)
	}
	// Both the old (recently retired) and new (active) keys must be serving.
	if len(a.PublicKeys()) != 2 {
		t.Fatalf("expected 2 serving keys after rotation, got %d", len(a.PublicKeys()))
	}
	// A token signed by the now-retired key still verifies.
	ks := auth.NewKeySet(a.PublicKeys())
	if _, err := auth.VerifyToken(oldTok, ks); err != nil {
		t.Fatalf("old token should still verify within window: %v", err)
	}
}

func TestRetiredKeyDropsAfterWindow(t *testing.T) {
	db := testDB(t)
	// maxAccessTTL tiny so the retired key falls out of the serving window
	// immediately after rotation.
	a, err := newAuthd(db, time.Minute, time.Hour, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Rotate(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if err := a.reload(); err != nil {
		t.Fatal(err)
	}
	if len(a.PublicKeys()) != 1 {
		t.Fatalf("expected only the active key serving, got %d", len(a.PublicKeys()))
	}
}

func TestRevokeAllNowClosesWindow(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	oldTok, _ := a.MintForSubject("user:1", nil, []string{"a:read"}, "")
	if err := a.RevokeAllNow(); err != nil {
		t.Fatal(err)
	}
	// Emergency revoke backdates the old key past the window: only the fresh
	// active key serves, and the old token no longer verifies.
	if len(a.PublicKeys()) != 1 {
		t.Fatalf("expected 1 serving key after revoke, got %d", len(a.PublicKeys()))
	}
	ks := auth.NewKeySet(a.PublicKeys())
	if _, err := auth.VerifyToken(oldTok, ks); err == nil {
		t.Fatal("token signed by revoked key must not verify")
	}
}

func TestKeyPersistsAcrossReopen(t *testing.T) {
	db := testDB(t)
	a1 := newTestAuthd(t, db)
	k1, _ := a1.activeKey()
	a2 := newTestAuthd(t, db) // same DB, no new key generated
	k2, _ := a2.activeKey()
	if k1.Kid != k2.Kid {
		t.Fatalf("kid changed across reopen: %s != %s", k1.Kid, k2.Kid)
	}
}

func TestRefreshRotation(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	r0, err := a.IssueRefresh("user:1", []string{"tasks:read"}, "")
	if err != nil {
		t.Fatal(err)
	}
	access, r1, err := a.Refresh(r0)
	if err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	if r1 == r0 {
		t.Fatal("refresh token must rotate")
	}
	ks := auth.NewKeySet(a.PublicKeys())
	sub, err := auth.VerifyToken(access, ks)
	if err != nil {
		t.Fatalf("minted access invalid: %v", err)
	}
	if sub.Sub != "user:1" || !auth.HasScope(sub.Scope, "tasks", "read") {
		t.Fatalf("bad refreshed access %+v", sub)
	}
	// The successor still rotates.
	if _, _, err := a.Refresh(r1); err != nil {
		t.Fatalf("second refresh: %v", err)
	}
}

func TestRefreshReuseRevokesFamily(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	r0, _ := a.IssueRefresh("user:1", []string{"a:read"}, "")
	_, r1, err := a.Refresh(r0)
	if err != nil {
		t.Fatal(err)
	}
	// Replaying the spent r0 is a reuse attack.
	if _, _, err := a.Refresh(r0); err != errReuse {
		t.Fatalf("expected errReuse on replay, got %v", err)
	}
	// The whole family is now revoked: the live successor r1 is dead too.
	if _, _, err := a.Refresh(r1); err == nil {
		t.Fatal("successor token must be revoked after family kill")
	}
}

func TestRefreshUnknownTokenRejected(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	if _, _, err := a.Refresh("not-a-real-token"); err != auth.ErrInvalidToken {
		t.Fatalf("expected ErrInvalidToken, got %v", err)
	}
}

func TestOAuthUserUpsertUniqueProvider(t *testing.T) {
	db := testDB(t)
	if err := upsertOAuthUser(db, "user:abc", "Alice", "github", "12345"); err != nil {
		t.Fatal(err)
	}
	if !userExists(db, "user:abc") {
		t.Fatal("user should exist")
	}
	// Re-linking the same provider updates provider_sub, does not duplicate
	// (UNIQUE(user_id, provider)).
	if err := upsertOAuthUser(db, "user:abc", "Alice", "github", "12345"); err != nil {
		t.Fatal(err)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM oauth_identities WHERE user_id = ? AND provider = ?`,
		"user:abc", "github").Scan(&n)
	if n != 1 {
		t.Fatalf("expected 1 identity row, got %d", n)
	}
	// sub is stored bare (no "user:" prefix is added by storage).
	var stored string
	db.QueryRow(`SELECT user_id FROM auth_users WHERE user_id = ?`, "user:abc").Scan(&stored)
	if stored != "user:abc" {
		t.Fatalf("user_id stored as %q", stored)
	}
}

func TestServiceTokenEndpoint(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	srv := &server{a: a, serviceSecrets: map[string]string{"boot-secret-timed": "service:timed"}}
	ts := httptest.NewServer(srv.mux())
	defer ts.Close()

	body, _ := json.Marshal(map[string]string{"key": "boot-secret-timed"})
	resp, err := http.Post(ts.URL+"/v1/service-token", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	json.NewDecoder(resp.Body).Decode(&out)

	ks := auth.NewKeySet(a.PublicKeys())
	sub, err := auth.VerifyToken(out.AccessToken, ks)
	if err != nil {
		t.Fatalf("service token invalid: %v", err)
	}
	if sub.Sub != "service:timed" {
		t.Fatalf("sub = %q", sub.Sub)
	}
	// Scope is the declared service grant for timed.
	if !auth.HasScope(sub.Scope, "messages", "write") || !auth.HasScope(sub.Scope, "tasks", "read") {
		t.Fatalf("service:timed grants wrong: %v", sub.Scope)
	}
}

func TestServiceTokenBadSecretRejected(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	srv := &server{a: a, serviceSecrets: map[string]string{"boot": "service:timed"}}
	ts := httptest.NewServer(srv.mux())
	defer ts.Close()

	body, _ := json.Marshal(map[string]string{"key": "wrong"})
	resp, err := http.Post(ts.URL+"/v1/service-token", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestKeysEndpointServesJWKS(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	srv := &server{a: a}
	ts := httptest.NewServer(srv.mux())
	defer ts.Close()

	// FetchKeys hits /v1/keys and a token from the active key verifies.
	ks, err := auth.FetchKeys(ts.URL)
	if err != nil {
		t.Fatalf("fetch keys: %v", err)
	}
	tok, _ := a.MintForSubject("user:9", nil, []string{"x:read"}, "")
	if _, err := auth.VerifyToken(tok, ks); err != nil {
		t.Fatalf("verify against fetched jwks: %v", err)
	}
}

func TestRefreshEndpointRotates(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	srv := &server{a: a}
	ts := httptest.NewServer(srv.mux())
	defer ts.Close()

	r0, _ := a.IssueRefresh("user:1", []string{"a:read"}, "")
	body, _ := json.Marshal(map[string]string{"refresh_token": r0})
	resp, err := http.Post(ts.URL+"/v1/refresh", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	if out.RefreshToken == "" || out.RefreshToken == r0 {
		t.Fatal("refresh endpoint must return a rotated token")
	}
	ks := auth.NewKeySet(a.PublicKeys())
	if _, err := auth.VerifyToken(out.AccessToken, ks); err != nil {
		t.Fatalf("access from refresh endpoint invalid: %v", err)
	}
}

func TestLoadServiceSecretsParsing(t *testing.T) {
	t.Setenv("AUTHD_SERVICE_KEYS", "service:timed=aaa, service:onbod=bbb ,bad,=ccc,ddd=")
	t.Setenv("AUTHD_SERVICE_KEY", "self-secret")
	got := loadServiceSecrets()
	if got["aaa"] != "service:timed" || got["bbb"] != "service:onbod" {
		t.Fatalf("parse failed: %v", got)
	}
	if got["self-secret"] != "service:authd" {
		t.Fatal("self bootstrap should map to service:authd")
	}
	// Malformed entries are skipped.
	if _, ok := got["ccc"]; ok {
		t.Fatal("entry with empty principal must be skipped")
	}
	if strings.Contains(strings.Join(keysOf(got), ","), "ddd") {
		t.Fatal("entry with empty secret must be skipped")
	}
}

func keysOf(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

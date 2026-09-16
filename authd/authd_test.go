package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/authd/authstore"
	"github.com/kronael/arizuko/objstore/dynstore"
	"github.com/kronael/arizuko/objstore/dynstore/dyntest"
)

func TestMain(m *testing.M) { dyntest.Main(m) }

func testDB(t *testing.T) *dynstore.Store {
	t.Helper()
	db := dyntest.Store(t)
	if err := ensureStorage(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func userExists(t *testing.T, db *dynstore.Store, userID string) bool {
	t.Helper()
	_, ok := entityItem(t, db, authstore.AuthUsers, userID)
	return ok
}

func newTestAuthd(t *testing.T, db *dynstore.Store) *Authd {
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
	tok, err := a.MintForSubject("service:timed", "service", nil, []string{"tasks:read"}, "")
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

func TestRotationKeepsOldKeyServingWithinWindow(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db) // maxAccessTTL = 1h
	oldTok, _ := a.MintForSubject("user:1", "user", nil, []string{"a:read"}, "")

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
	oldTok, _ := a.MintForSubject("user:1", "user", nil, []string{"a:read"}, "")
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
	// The refresh row stores the BARE sub (spec 5/1 "sub prefix rule"); the
	// user: prefix is added only when the access JWT is minted.
	r0, err := a.IssueRefresh("1", []string{"tasks:read"}, "")
	if err != nil {
		t.Fatal(err)
	}
	access, r1, err := a.Refresh(context.Background(), r0)
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
	if _, _, err := a.Refresh(context.Background(), r1); err != nil {
		t.Fatalf("second refresh: %v", err)
	}
}

func TestRefreshReuseRevokesFamily(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	r0, _ := a.IssueRefresh("user:1", []string{"a:read"}, "")
	_, r1, err := a.Refresh(context.Background(), r0)
	if err != nil {
		t.Fatal(err)
	}
	// Replaying the spent r0 is a reuse attack.
	if _, _, err := a.Refresh(context.Background(), r0); err != errReuse {
		t.Fatalf("expected errReuse on replay, got %v", err)
	}
	// The whole family is now revoked: the live successor r1 is dead too.
	if _, _, err := a.Refresh(context.Background(), r1); err == nil {
		t.Fatal("successor token must be revoked after family kill")
	}
}

func TestRefreshUnknownTokenRejected(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	if _, _, err := a.Refresh(context.Background(), "not-a-real-token"); err != auth.ErrInvalidToken {
		t.Fatalf("expected ErrInvalidToken, got %v", err)
	}
}

func TestOAuthUserUpsertUniqueProvider(t *testing.T) {
	db := testDB(t)
	st := newTestStore(db)
	if err := st.LinkOAuthIdentity("user:abc", "Alice", "github", "12345"); err != nil {
		t.Fatal(err)
	}
	if !userExists(t, db, "user:abc") {
		t.Fatal("user should exist")
	}
	// Re-linking the same provider re-links the same identity; it does not add
	// a second one, and it does not add a second account.
	if err := st.LinkOAuthIdentity("user:abc", "Alice", "github", "12345"); err != nil {
		t.Fatal(err)
	}
	if n := entityCount(t, db, authstore.OAuthIdentities); n != 1 {
		t.Fatalf("expected 1 identity, got %d", n)
	}
	// One identity per (account, provider): a different github id for the same
	// account DISPLACES the first, which is what the UNIQUE(user_id, provider)
	// upsert did. The old sub must stop resolving.
	if err := st.LinkOAuthIdentity("user:abc", "Alice", "github", "67890"); err != nil {
		t.Fatal(err)
	}
	if n := entityCount(t, db, authstore.OAuthIdentities); n != 1 {
		t.Fatalf("a second github id left %d identities on one account, want 1", n)
	}
	if _, _, _, _, ok := st.OAuthIdentityForSub("github:12345"); ok {
		t.Fatal("the displaced identity still resolves")
	}
	owner, _, _, subs, ok := st.OAuthIdentityForSub("github:67890")
	if !ok || owner != "user:abc" {
		t.Fatalf("the new identity resolves to %q (ok=%v), want user:abc", owner, ok)
	}
	if len(subs) != 1 || subs[0] != "github:67890" {
		t.Fatalf("the account lists %v, want only github:67890", subs)
	}
	// The account's sub is stored bare (no "user:" prefix is added by storage).
	if _, ok := entityItem(t, db, authstore.AuthUsers, "user:abc"); !ok {
		t.Fatal("the account is not stored under its own sub")
	}
}

// New /v1/service-token shape (spec 5/1 §435): secret in the Authorization
// header, daemon name in the body. The body carries NO secret.
func TestServiceTokenEndpoint(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	srv := &server{a: a, serviceSecrets: map[string]string{"boot-secret-timed": "service:timed"}}
	ts := httptest.NewServer(srv.mux(nil))
	defer ts.Close()

	resp, out := postSvcToken(t, ts.URL, "timed", "boot-secret-timed")
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	ks := auth.NewKeySet(a.PublicKeys())
	sub, err := auth.VerifyToken(out.Token, ks)
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
	// The response advertises the scope; the field name is "token", not the old
	// "access_token".
	if len(out.Scope) == 0 {
		t.Fatal("service-token response must echo scope")
	}
}

// The secret rides the Authorization header; a secret in the JSON body (the old
// shape) is ignored — the body carries only the daemon name now.
func TestServiceTokenSecretMustBeInHeaderNotBody(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	srv := &server{a: a, serviceSecrets: map[string]string{"boot-secret-timed": "service:timed"}}
	ts := httptest.NewServer(srv.mux(nil))
	defer ts.Close()

	// Secret only in the body (old shape) + no Authorization header → rejected.
	body, _ := json.Marshal(map[string]string{"daemon": "timed", "key": "boot-secret-timed", "secret": "boot-secret-timed"})
	resp, err := http.Post(ts.URL+"/v1/service-token", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatal("a secret carried only in the body must NOT authenticate")
	}
}

func TestServiceTokenBadSecretRejected(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	srv := &server{a: a, serviceSecrets: map[string]string{"boot": "service:timed"}}
	ts := httptest.NewServer(srv.mux(nil))
	defer ts.Close()

	resp, _ := postSvcToken(t, ts.URL, "timed", "wrong")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

// Wrong daemon name with a valid secret is also rejected (the name must match
// the principal the secret is bound to).
func TestServiceTokenWrongDaemonRejected(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	srv := &server{a: a, serviceSecrets: map[string]string{"boot": "service:timed"}}
	ts := httptest.NewServer(srv.mux(nil))
	defer ts.Close()

	resp, _ := postSvcToken(t, ts.URL, "onbod", "boot") // boot is timed's secret
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for mismatched daemon, got %d", resp.StatusCode)
	}
}

type svcTokenResp struct {
	Token string   `json:"token"`
	Scope []string `json:"scope"`
}

func postSvcToken(t *testing.T, base, daemon, secret string) (*http.Response, svcTokenResp) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"daemon": daemon})
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/service-token", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var out svcTokenResp
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func TestKeysEndpointServesJWKS(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	srv := &server{a: a}
	ts := httptest.NewServer(srv.mux(nil))
	defer ts.Close()

	// FetchKeys (RemoteKeySet) hits /v1/keys and a token from the active key
	// verifies through the network path.
	ks, err := auth.FetchKeys(context.Background(), ts.URL)
	if err != nil {
		t.Fatalf("fetch keys: %v", err)
	}
	tok, _ := a.MintForSubject("user:9", "user", nil, []string{"x:read"}, "")
	if _, err := auth.VerifyToken(tok, ks); err != nil {
		t.Fatalf("verify against fetched jwks: %v", err)
	}
}

func TestRefreshEndpointRotates(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	srv := &server{a: a}
	ts := httptest.NewServer(srv.mux(nil))
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
		Token        string `json:"token"`
		ExpiresAt    string `json:"expires_at"`
		RefreshToken string `json:"refresh_token"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	if out.RefreshToken == "" || out.RefreshToken == r0 {
		t.Fatal("refresh endpoint must return a rotated token")
	}
	if out.ExpiresAt == "" {
		t.Fatal("refresh response must carry expires_at (spec 5/1 shape)")
	}
	ks := auth.NewKeySet(a.PublicKeys())
	if _, err := auth.VerifyToken(out.Token, ks); err != nil {
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

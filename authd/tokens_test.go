package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
	_ "modernc.org/sqlite"
)

// postTokens calls POST /v1/tokens with an optional bearer.
func postTokens(t *testing.T, base, bearer string, body any) (*http.Response, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/tokens", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	return resp, out
}

// callerToken mints a parent token with the given scope + folder, the way a
// real bearer arrives at /v1/tokens.
func callerToken(t *testing.T, a *Authd, sub string, scope []string, folder string) string {
	t.Helper()
	k, err := a.activeKey()
	if err != nil {
		t.Fatal(err)
	}
	c := auth.TokenClaims{Sub: sub, Scope: scope}
	if folder != "" {
		c.Extra = map[string]string{"arz/folder": folder}
	}
	tok, err := k.Sign(c, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// Downscope round-trip: an agent narrows its own token; the minted token holds
// the subset scope, the same sub, and parent_jti = the caller's jti. This is
// the path runed/broker.go drives.
func TestTokensDownscopeRoundTrip(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	srv := &server{a: a}
	ts := httptest.NewServer(srv.mux())
	defer ts.Close()

	parent := callerToken(t, a, "user:1", []string{"tasks:read", "tasks:write"}, "atlas/main")
	parentSub, _ := auth.VerifyToken(parent, a.LocalKeySet())

	resp, out := postTokens(t, ts.URL, parent, map[string]any{
		"typ": "downscoped", "scope": []string{"tasks:read"},
		"folder": "atlas/main/sub", "ttl_seconds": 300,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("downscope status %d: %v", resp.StatusCode, out)
	}
	tok, _ := out["token"].(string)
	if tok == "" || out["jti"] == "" {
		t.Fatalf("response missing token/jti: %v", out)
	}
	sub, err := auth.VerifyToken(tok, a.LocalKeySet())
	if err != nil {
		t.Fatalf("downscoped token invalid: %v", err)
	}
	if sub.Sub != "user:1" {
		t.Fatalf("downscoped sub = %q want user:1 (forced to caller)", sub.Sub)
	}
	if !auth.HasScope(sub.Scope, "tasks", "read") || auth.HasScope(sub.Scope, "tasks", "write") {
		t.Fatalf("downscoped scope wrong: %v", sub.Scope)
	}
	if sub.ParentJTI != parentSub.JTI {
		t.Fatalf("parent_jti = %q want caller jti %q", sub.ParentJTI, parentSub.JTI)
	}
	if sub.Extra["arz/folder"] != "atlas/main/sub" {
		t.Fatalf("folder not narrowed: %v", sub.Extra)
	}
}

// Over-broad downscope: requesting a scope the parent lacks → 403
// scope_exceeds_parent.
func TestTokensDownscopeOverBroadRejected(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	srv := &server{a: a}
	ts := httptest.NewServer(srv.mux())
	defer ts.Close()

	parent := callerToken(t, a, "user:1", []string{"tasks:read"}, "")
	resp, out := postTokens(t, ts.URL, parent, map[string]any{
		"typ": "downscoped", "scope": []string{"secrets:read"},
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
	if out["error"] != "scope_exceeds_parent" {
		t.Fatalf("error = %v want scope_exceeds_parent", out["error"])
	}
}

// Downscoping into a folder OUTSIDE the parent's subtree is rejected.
func TestTokensDownscopeFolderEscapeRejected(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	srv := &server{a: a}
	ts := httptest.NewServer(srv.mux())
	defer ts.Close()

	parent := callerToken(t, a, "user:1", []string{"tasks:read"}, "atlas/main")
	resp, _ := postTokens(t, ts.URL, parent, map[string]any{
		"typ": "downscoped", "scope": []string{"tasks:read"}, "folder": "other/root",
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("folder escape must be 403, got %d", resp.StatusCode)
	}
}

// Unauthenticated /v1/tokens → 401.
func TestTokensUnauthenticatedRejected(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	srv := &server{a: a}
	ts := httptest.NewServer(srv.mux())
	defer ts.Close()

	resp, _ := postTokens(t, ts.URL, "", map[string]any{"typ": "downscoped", "scope": []string{"tasks:read"}})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
	// A garbage bearer is also rejected.
	resp2, _ := postTokens(t, ts.URL, "not.a.jwt", map[string]any{"typ": "downscoped"})
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("garbage bearer must be 401, got %d", resp2.StatusCode)
	}
}

// The global *:* scope is never mintable.
func TestTokensRejectsGlobalWildcard(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	srv := &server{a: a}
	ts := httptest.NewServer(srv.mux())
	defer ts.Close()

	parent := callerToken(t, a, "user:1", []string{"tasks:read"}, "")
	resp, out := postTokens(t, ts.URL, parent, map[string]any{"typ": "downscoped", "scope": []string{"*:*"}})
	if resp.StatusCode != http.StatusBadRequest || out["error"] != "invalid_scope" {
		t.Fatalf("global *:* must be 400 invalid_scope, got %d %v", resp.StatusCode, out)
	}
}

// Issuer mint with NO grants fetcher wired fails closed (503 grants_unavailable),
// NOT a silent caller-scope fallback. authd cannot evaluate the target's ceiling
// without the grants snapshot (spec 5/1 issuer mint is bounded by the target,
// not the caller).
func TestTokensIssuerMintFailsClosedWithoutGrants(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	srv := &server{a: a} // grants == nil
	ts := httptest.NewServer(srv.mux())
	defer ts.Close()

	// Caller holds tokens:mint and targets a DIFFERENT sub → issuer mode.
	minter := callerToken(t, a, "service:onbod", []string{"tokens:mint"}, "")
	resp, out := postTokens(t, ts.URL, minter, map[string]any{
		"typ": "user", "sub": "user:new", "scope": []string{"tasks:read"},
	})
	if resp.StatusCode != http.StatusServiceUnavailable || out["error"] != "grants_unavailable" {
		t.Fatalf("issuer mint without grants must be 503 grants_unavailable, got %d %v", resp.StatusCode, out)
	}
}

// With a grants fetcher wired, issuer mint succeeds within the TARGET's grants —
// the minter's own scope is irrelevant (the minter holds only tokens:mint, not
// tasks:read). The minted token carries the target's sub + folder.
func TestTokensIssuerMintWithinTargetGrants(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	srv := &server{a: a, grants: fakeGrants{
		"new": {Scope: []string{"tasks:read", "tasks:write"}, Folder: "atlas/main"},
	}}
	ts := httptest.NewServer(srv.mux())
	defer ts.Close()

	minter := callerToken(t, a, "service:onbod", []string{"tokens:mint"}, "")
	resp, out := postTokens(t, ts.URL, minter, map[string]any{
		"typ": "user", "sub": "user:new", "scope": []string{"tasks:read"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("issuer mint status %d: %v", resp.StatusCode, out)
	}
	sub, err := auth.VerifyToken(out["token"].(string), a.LocalKeySet())
	if err != nil {
		t.Fatalf("minted token invalid: %v", err)
	}
	if sub.Sub != "user:new" {
		t.Fatalf("issuer mint sub = %q want user:new", sub.Sub)
	}
	if !auth.HasScope(sub.Scope, "tasks", "read") {
		t.Fatalf("issuer scope must hold the requested tasks:read, got %v", sub.Scope)
	}
	if sub.Extra["arz/folder"] != "atlas/main" {
		t.Fatalf("issuer folder = %v want target's atlas/main", sub.Extra)
	}
}

// Issuer mint requesting MORE than the target's grants is a violation →
// 403 scope_exceeds_minter (spec 5/1: requested ⊆ target snapshot).
func TestTokensIssuerMintBeyondTargetGrantsRejected(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	srv := &server{a: a, grants: fakeGrants{
		"new": {Scope: []string{"tasks:read"}, Folder: "atlas/main"},
	}}
	ts := httptest.NewServer(srv.mux())
	defer ts.Close()

	minter := callerToken(t, a, "service:onbod", []string{"tokens:mint"}, "")
	resp, out := postTokens(t, ts.URL, minter, map[string]any{
		"typ": "user", "sub": "user:new", "scope": []string{"tasks:read", "tasks:write"},
	})
	if resp.StatusCode != http.StatusForbidden || out["error"] != "scope_exceeds_minter" {
		t.Fatalf("over-grant issuer mint must be 403 scope_exceeds_minter, got %d %v", resp.StatusCode, out)
	}
}

// Issuer mint targeting a sub with NO grant rows → 403 (cannot mint a session
// for an ungranted account via the minter path).
func TestTokensIssuerMintNoGrantsRejected(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	srv := &server{a: a, grants: fakeGrants{}} // every sub → ErrNoGrants
	ts := httptest.NewServer(srv.mux())
	defer ts.Close()

	minter := callerToken(t, a, "service:onbod", []string{"tokens:mint"}, "")
	resp, out := postTokens(t, ts.URL, minter, map[string]any{
		"typ": "user", "sub": "user:ungranted", "scope": []string{"tasks:read"},
	})
	if resp.StatusCode != http.StatusForbidden || out["error"] != "scope_exceeds_minter" {
		t.Fatalf("issuer mint of ungranted sub must be 403, got %d %v", resp.StatusCode, out)
	}
}

// A downscope (typ=downscoped) NEVER mints a token bearing another sub, even
// when the body requests one. The minted sub is forced to the caller's — a
// service principal cannot impersonate a user through the downscope path (the
// requested sub is ignored). Spec 5/1: "the sub field is forced to the
// caller's".
func TestTokensDownscopeForcesCallerSubNoImpersonation(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	srv := &server{a: a}
	ts := httptest.NewServer(srv.mux())
	defer ts.Close()

	// Caller is a service principal that also happens to hold tokens:mint; the
	// explicit typ=downscoped still pins the minted sub to the caller.
	caller := callerToken(t, a, "service:runed", []string{"tasks:read", "tokens:mint"}, "")
	resp, out := postTokens(t, ts.URL, caller, map[string]any{
		"typ": "downscoped", "sub": "user:victim", "scope": []string{"tasks:read"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("downscope status %d: %v", resp.StatusCode, out)
	}
	sub, err := auth.VerifyToken(out["token"].(string), a.LocalKeySet())
	if err != nil {
		t.Fatal(err)
	}
	if sub.Sub != "service:runed" {
		t.Fatalf("downscope must force caller sub, got %q (impersonation hole)", sub.Sub)
	}
}

// fakeGrants is an in-memory GrantsFetcher keyed by BARE sub; absent → no grants.
type fakeGrants map[string]GrantsSnapshot

func (f fakeGrants) FetchGrants(_ context.Context, bareSub string) (GrantsSnapshot, error) {
	if snap, ok := f[bareSub]; ok {
		return snap, nil
	}
	return GrantsSnapshot{}, ErrNoGrants
}

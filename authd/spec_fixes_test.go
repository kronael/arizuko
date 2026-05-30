package main

// Spec-conformance + security tests for specs/5/1: the typ claim per mint mode,
// the /v1/refresh response shape + cookie channel, the JWKS Cache-Control TTL,
// and refresh-time grant re-snapshotting.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
)

// decodeTyp lifts the "typ" payload claim out of a minted JWS (distinct from the
// JWS header "typ":"JWT").
func decodeTyp(t *testing.T, tok string) string {
	t.Helper()
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("not a compact JWS: %q", tok)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var c auth.TokenClaims
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	return c.Typ
}

// Each authd mint mode stamps the spec typ claim: a service-token exchange →
// "service", an OAuth session → "user", a downscope → "downscoped".
func TestMintModesStampTyp(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)

	// service
	svc, err := a.MintForSubject("service:timed", "service", nil, []string{"tasks:read"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeTyp(t, svc); got != "service" {
		t.Fatalf("service mint typ = %q want service", got)
	}

	// user (OAuth session path is signMinted with Typ:"user")
	o := newOAuth(t, a, nil, nil)
	req := httptest.NewRequest("GET", "/auth/google/callback", nil)
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	o.dispatch(rec, req, "google", "g-typ", "U", auth.StateIntent{})
	if got := decodeTyp(t, jsonField(t, rec.Body.Bytes(), "token")); got != "user" {
		t.Fatalf("OAuth session typ = %q want user", got)
	}

	// downscoped
	parent := callerToken(t, a, "user:1", []string{"tasks:read", "tasks:write"}, "atlas/main")
	psub, err := auth.VerifyToken(parent, a.LocalKeySet())
	if err != nil {
		t.Fatal(err)
	}
	m, err := a.Downscope(psub, []string{"tasks:read"}, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeTyp(t, m.token); got != "downscoped" {
		t.Fatalf("downscope typ = %q want downscoped", got)
	}
}

// Issuer mint stamps the requested typ (here "service") on the target token.
func TestIssuerMintStampsRequestedTyp(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	srv := &server{a: a, grants: fakeGrants{
		"svc": {Scope: []string{"messages:write"}},
	}}
	ts := httptest.NewServer(srv.mux())
	defer ts.Close()

	minter := callerToken(t, a, "service:onbod", []string{"tokens:mint"}, "")
	resp, out := postTokens(t, ts.URL, minter, map[string]any{
		"typ": "service", "sub": "service:svc", "scope": []string{"messages:write"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("issuer mint status %d: %v", resp.StatusCode, out)
	}
	if got := decodeTyp(t, out["token"].(string)); got != "service" {
		t.Fatalf("issuer mint typ = %q want service", got)
	}
}

// Refresh rows store the BARE canonical sub (spec 5/1 "sub prefix rule": the
// user:/service: prefix lives ONLY in the JWT sub claim); the minted access
// token re-adds the prefix. A full OAuth login → refresh keeps the access sub
// at "user:<bare>", and the stored refresh row carries the bare form.
func TestRefreshRowStoresBareSubMintAddsPrefix(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	o := newOAuth(t, a, nil, nil)

	req := httptest.NewRequest("GET", "/auth/google/callback", nil)
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	o.dispatch(rec, req, "google", "g-bare", "U", auth.StateIntent{})

	// The minted access token carries the user:-prefixed sub.
	access := jsonField(t, rec.Body.Bytes(), "token")
	sub, err := auth.VerifyToken(access, a.LocalKeySet())
	if err != nil {
		t.Fatal(err)
	}
	if sub.Sub != "user:google:g-bare" {
		t.Fatalf("minted access sub = %q want user:google:g-bare", sub.Sub)
	}

	// The stored refresh row holds the BARE sub (no user: prefix).
	refresh := jsonField(t, rec.Body.Bytes(), "refresh_token")
	row, ok := lookupRefresh(a.db, refresh)
	if !ok {
		t.Fatal("refresh row must exist")
	}
	if row.sub != "google:g-bare" {
		t.Fatalf("stored refresh sub = %q want bare google:g-bare (no user: prefix)", row.sub)
	}

	// Refreshing re-adds the prefix at mint: the access sub is unchanged.
	access2, _, err := a.Refresh(context.Background(), refresh)
	if err != nil {
		t.Fatal(err)
	}
	sub2, err := auth.VerifyToken(access2, a.LocalKeySet())
	if err != nil {
		t.Fatal(err)
	}
	if sub2.Sub != "user:google:g-bare" {
		t.Fatalf("refreshed access sub = %q want user:google:g-bare", sub2.Sub)
	}
}

// The HTTP grants fetcher, when GRANTS_URL is set, authenticates with authd's
// self-minted service:authd token (scope grants:read) and pulls the bare sub's
// scope + folder at login AND re-snapshots them at refresh (spec 5/1
// § Login-time scope snapshot). With GRANTS_URL unset the fetcher is nil and
// behavior is unchanged (empty-scope sessions).
func TestHTTPGrantsFetcherWiredAndUnwired(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)

	// Unwired: an empty GRANTS_URL leaves grants nil (current behavior).
	if g := newHTTPGrants(a, ""); g != nil {
		t.Fatal("empty GRANTS_URL must leave the grants fetcher nil (unchanged behavior)")
	}

	// Stub grants backend: verifies the caller's bearer is a valid service:authd
	// token carrying grants:read, then returns the requested sub's scope+folder.
	var gotSub string
	gs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearerTok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		sub, err := auth.VerifyToken(bearerTok, a.LocalKeySet())
		if err != nil || sub.Sub != "service:authd" || !auth.HasScope(sub.Scope, "grants", "read") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		// /v1/users/{bareSub}/scopes — the bare sub is the last path segment.
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		gotSub = parts[len(parts)-2]
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"scope": []string{"tasks:read", "tasks:write"}, "folder": "atlas/main",
		})
	}))
	defer gs.Close()

	g := newHTTPGrants(a, gs.URL)
	if g == nil {
		t.Fatal("a set GRANTS_URL must build a fetcher")
	}
	a.grants = g

	// Login snapshot: dispatch pulls the target's scope+folder through the fetcher.
	o := newOAuth(t, a, nil, g)
	req := httptest.NewRequest("GET", "/auth/google/callback", nil)
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	o.dispatch(rec, req, "google", "g-http", "U", auth.StateIntent{})
	if rec.Code != http.StatusOK {
		t.Fatalf("login with grants fetcher status %d: %s", rec.Code, rec.Body.String())
	}
	if gotSub != "google:g-http" {
		t.Fatalf("grants lookup used sub %q want bare google:g-http", gotSub)
	}
	sub, err := auth.VerifyToken(jsonField(t, rec.Body.Bytes(), "token"), a.LocalKeySet())
	if err != nil {
		t.Fatal(err)
	}
	if !auth.HasScope(sub.Scope, "tasks", "write") || sub.Extra["arz/folder"] != "atlas/main" {
		t.Fatalf("login snapshot must stamp fetched scope+folder, got %v / %v", sub.Scope, sub.Extra)
	}

	// Refresh re-snapshot: the refreshed access reflects the fetcher's scopes.
	refreshSub, _, err := a.Refresh(context.Background(), jsonField(t, rec.Body.Bytes(), "refresh_token"))
	if err != nil {
		t.Fatal(err)
	}
	rs, err := auth.VerifyToken(refreshSub, a.LocalKeySet())
	if err != nil {
		t.Fatal(err)
	}
	if !auth.HasScope(rs.Scope, "tasks", "write") {
		t.Fatalf("refresh must re-snapshot grants, got %v", rs.Scope)
	}
}

// /v1/refresh accepts the refresh token from a COOKIE (browser sends only a
// cookie) and returns the spec shape: a "token" + "expires_at"; the successor
// refresh rides Set-Cookie, not the JSON body.
func TestRefreshFromCookieOnly(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	srv := &server{a: a}
	ts := httptest.NewServer(srv.mux())
	defer ts.Close()

	r0, _ := a.IssueRefresh("user:1", []string{"tasks:read"}, "")
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/refresh", nil)
	req.AddCookie(&http.Cookie{Name: "refresh_token", Value: r0})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cookie-only refresh status %d", resp.StatusCode)
	}
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	if _, ok := out["token"].(string); !ok || out["token"] == "" {
		t.Fatalf("refresh response must carry 'token', got %v", out)
	}
	if _, ok := out["expires_at"].(string); !ok || out["expires_at"] == "" {
		t.Fatalf("refresh response must carry 'expires_at', got %v", out)
	}
	// Browser channel: successor refresh is in Set-Cookie, NOT the JSON body.
	if _, leaked := out["refresh_token"]; leaked {
		t.Fatal("browser refresh must not return the successor in the body (HttpOnly only)")
	}
	var rotated bool
	for _, c := range resp.Cookies() {
		if c.Name == "refresh_token" && c.Value != "" && c.Value != r0 {
			rotated = true
			if !c.HttpOnly {
				t.Fatal("rotated refresh cookie must be HttpOnly")
			}
		}
	}
	if !rotated {
		t.Fatal("cookie-channel refresh must rotate the cookie via Set-Cookie")
	}
	// The minted access verifies.
	if _, err := auth.VerifyToken(out["token"].(string), a.LocalKeySet()); err != nil {
		t.Fatalf("access from cookie refresh invalid: %v", err)
	}
}

// /v1/keys advertises the spec JWKS cache TTL: Cache-Control max-age=3600.
func TestKeysCacheControlMaxAge(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	srv := &server{a: a}
	ts := httptest.NewServer(srv.mux())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/v1/keys")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "max-age=3600") {
		t.Fatalf("Cache-Control = %q want max-age=3600 (spec 5/1 JWKS TTL)", cc)
	}
}

// mutableGrants is a GrantsFetcher whose snapshot can change between refreshes.
type mutableGrants struct{ snap GrantsSnapshot }

func (m *mutableGrants) FetchGrants(context.Context, string) (GrantsSnapshot, error) {
	return m.snap, nil
}

// Refresh re-snapshots grants when a fetcher is wired on Authd: a grant change
// takes effect at the next refresh, so a narrowed user does not retain stale
// scope up to the refresh TTL (spec 5/1 § Login-time scope snapshot, refresh).
func TestRefreshReSnapshotsGrants(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	g := &mutableGrants{snap: GrantsSnapshot{Scope: []string{"tasks:read", "tasks:write"}}}
	a.grants = g

	r0, _ := a.IssueRefresh("user:1", []string{"tasks:read", "tasks:write"}, "")

	// Narrow the user's grants, then refresh — the new access must reflect it.
	g.snap = GrantsSnapshot{Scope: []string{"tasks:read"}}
	access, _, err := a.Refresh(context.Background(), r0)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := auth.VerifyToken(access, a.LocalKeySet())
	if err != nil {
		t.Fatal(err)
	}
	if !auth.HasScope(sub.Scope, "tasks", "read") {
		t.Fatalf("refreshed token must keep the still-granted tasks:read, got %v", sub.Scope)
	}
	if auth.HasScope(sub.Scope, "tasks", "write") {
		t.Fatalf("refreshed token must drop the revoked tasks:write, got %v", sub.Scope)
	}
}

// With no fetcher wired, Refresh reuses the stored row scope (the additive
// default — no global is fake-wired).
func TestRefreshReusesStoredScopeWhenUnwired(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db) // grants nil
	r0, _ := a.IssueRefresh("user:1", []string{"tasks:read", "tasks:write"}, "")
	access, _, err := a.Refresh(context.Background(), r0)
	if err != nil {
		t.Fatal(err)
	}
	sub, _ := auth.VerifyToken(access, a.LocalKeySet())
	if !auth.HasScope(sub.Scope, "tasks", "write") {
		t.Fatalf("unwired refresh must reuse stored scope, got %v", sub.Scope)
	}
}

// A grants backend that is DOWN fails the refresh CLOSED — no token minted with
// stale scope (spec 5/1 § refresh fail-closed).
func TestRefreshFailsClosedWhenGrantsDown(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	a.grants = downGrants{}
	r0, _ := a.IssueRefresh("user:1", []string{"tasks:read"}, "")
	if _, _, err := a.Refresh(context.Background(), r0); err == nil {
		t.Fatal("refresh with grants backend down must fail closed (no token)")
	}
}

// blockingGrants blocks until ctx is cancelled, then returns the ctx error —
// it stands in for a hung grants backend so the test can prove Refresh honours
// the caller's ctx (a cancelled request aborts the fetch instead of hanging).
type blockingGrants struct{}

func (blockingGrants) FetchGrants(ctx context.Context, _ string) (GrantsSnapshot, error) {
	<-ctx.Done()
	return GrantsSnapshot{}, ctx.Err()
}

// TestRefreshHonoursCtxCancel: a cancelled request ctx aborts the grants fetch
// promptly (fail-closed) instead of holding the goroutine for the backend
// timeout — before the fix Refresh used context.Background() so the abandoned
// fetch ran to completion regardless of the request.
func TestRefreshHonoursCtxCancel(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	a.grants = blockingGrants{}
	r0, _ := a.IssueRefresh("user:1", []string{"tasks:read"}, "")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, _, err := a.Refresh(ctx, r0)
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled refresh must fail closed (no token), got nil error")
		}
	case <-time.After(time.Second):
		t.Fatal("Refresh did not honour ctx cancel (still blocked on grants fetch)")
	}
}

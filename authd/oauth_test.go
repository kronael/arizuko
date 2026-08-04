package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	_ "modernc.org/sqlite"
)

func newOAuth(t *testing.T, a *Authd, cfg *core.Config, g GrantsFetcher) *oauth {
	t.Helper()
	if cfg == nil {
		cfg = &core.Config{AuthSecret: "csrf-key", AuthBaseURL: "https://auth.example"}
	}
	return &oauth{a: a, cfg: cfg, state: []byte(cfg.AuthSecret), grants: g}
}

// A login (here: dispatch of a resolved provider identity) creates the
// canonical authd user, links the provider, and issues an ES256 access token
// that VerifyToken accepts — the core spec assertion for the OAuth move
// (issuance switched HS256→ES256, store switched to authd's tables).
func TestOAuthDispatchMintsES256AndCreatesUser(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	o := newOAuth(t, a, nil, nil) // no grants fetcher → empty-scope session

	req := httptest.NewRequest("GET", "/auth/google/callback", nil)
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	o.dispatch(rec, req, "google", "g-123", "Alice", auth.StateIntent{})

	if rec.Code != http.StatusOK {
		t.Fatalf("dispatch status %d: %s", rec.Code, rec.Body.String())
	}
	// Canonical user created + provider linked in authd's own tables.
	if !userExists(db, "google:g-123") {
		t.Fatal("canonical user not created in auth_users")
	}
	// The JSON response carries an ES256 access token that verifies.
	tok := jsonField(t, rec.Body.Bytes(), "token")
	sub, err := auth.VerifyToken(tok, a.LocalKeySet())
	if err != nil {
		t.Fatalf("OAuth-minted access token must verify: %v", err)
	}
	if sub.Sub != "user:google:g-123" {
		t.Fatalf("minted sub = %q want user:google:g-123", sub.Sub)
	}
	// The initial refresh token rotates through authd's own store.
	refresh := jsonField(t, rec.Body.Bytes(), "refresh_token")
	if refresh == "" {
		t.Fatal("JSON login must return an initial refresh token")
	}
	if _, newR, err := a.Refresh(context.Background(), refresh); err != nil || newR == refresh {
		t.Fatalf("OAuth-issued refresh must rotate: newR=%q err=%v", newR, err)
	}
}

// Regression (oracle finding 3): the access-token subject must be identical
// across login and refresh. The refresh row stores the BARE sub (spec 5/1 "sub
// prefix rule"); Refresh re-adds the user: prefix at mint, so the access claim
// keeps the prefix consistently rather than dropping it or double-prefixing.
func TestOAuthRefreshSubMatchesLogin(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	o := newOAuth(t, a, nil, nil)

	req := httptest.NewRequest("GET", "/auth/google/callback", nil)
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	o.dispatch(rec, req, "google", "g-123", "Alice", auth.StateIntent{})

	loginSub, err := auth.VerifyToken(jsonField(t, rec.Body.Bytes(), "token"), a.LocalKeySet())
	if err != nil {
		t.Fatalf("login token must verify: %v", err)
	}
	access, _, err := a.Refresh(context.Background(), jsonField(t, rec.Body.Bytes(), "refresh_token"))
	if err != nil {
		t.Fatalf("refresh failed: %v", err)
	}
	refreshSub, err := auth.VerifyToken(access, a.LocalKeySet())
	if err != nil {
		t.Fatalf("refreshed token must verify: %v", err)
	}
	if refreshSub.Sub != loginSub.Sub {
		t.Fatalf("subject drifted across refresh: login=%q refresh=%q", loginSub.Sub, refreshSub.Sub)
	}
	if refreshSub.Sub != "user:google:g-123" {
		t.Fatalf("refreshed sub = %q want user:google:g-123", refreshSub.Sub)
	}
}

// Browser login (Accept: text/html) delivers the access token via the
// localStorage bootstrap + an HttpOnly refresh cookie, not a JSON body.
func TestOAuthBrowserDeliveryUsesCookieAndBootstrap(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	o := newOAuth(t, a, nil, nil)

	req := httptest.NewRequest("GET", "/auth/github/callback", nil) // no JSON Accept
	rec := httptest.NewRecorder()
	o.dispatch(rec, req, "github", "gh-7", "Bob", auth.StateIntent{})

	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("browser login must return HTML, got %q", ct)
	}
	if !strings.Contains(rec.Body.String(), "localStorage.setItem('jwt'") {
		t.Fatal("browser login must bootstrap the jwt into localStorage")
	}
	var refreshCookie bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == "refresh_token" {
			refreshCookie = true
			if !c.HttpOnly {
				t.Fatal("refresh cookie must be HttpOnly")
			}
		}
	}
	if !refreshCookie {
		t.Fatal("browser login must set the refresh_token cookie")
	}
	// Empty-scope session (no grants fetcher) routes a browser to /onboard.
	if !strings.Contains(rec.Body.String(), "/onboard") {
		t.Fatal("empty-scope session must send the browser to /onboard")
	}
}

// /auth/me returns the verified caller identity from the bearer.
func TestOAuthMeReturnsIdentity(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	o := newOAuth(t, a, nil, nil)

	tok := callerToken(t, a, "user:42", []string{"tasks:read"}, "atlas/main")
	req := httptest.NewRequest("GET", "/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	o.me(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("me status %d", rec.Code)
	}
	if jsonField(t, rec.Body.Bytes(), "sub") != "user:42" {
		t.Fatalf("me sub wrong: %s", rec.Body.String())
	}
	if jsonField(t, rec.Body.Bytes(), "folder") != "atlas/main" {
		t.Fatalf("me folder wrong: %s", rec.Body.String())
	}
	// No bearer → 401.
	rec2 := httptest.NewRecorder()
	o.me(rec2, httptest.NewRequest("GET", "/auth/me", nil))
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("me without bearer must be 401, got %d", rec2.Code)
	}
}

// When a grants fetcher is wired, the OAuth session stamps the target's scope +
// folder into the access JWT.
func TestOAuthSessionSnapshotsGrants(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	o := newOAuth(t, a, nil, fakeGrants{
		"google:g-9": {Scope: []string{"tasks:read"}, Folder: "atlas/main"},
	})

	req := httptest.NewRequest("GET", "/auth/google/callback", nil)
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	o.dispatch(rec, req, "google", "g-9", "Carol", auth.StateIntent{})
	if rec.Code != http.StatusOK {
		t.Fatalf("dispatch status %d", rec.Code)
	}
	sub, err := auth.VerifyToken(jsonField(t, rec.Body.Bytes(), "token"), a.LocalKeySet())
	if err != nil {
		t.Fatal(err)
	}
	if !auth.HasScope(sub.Scope, "tasks", "read") {
		t.Fatalf("session scope must snapshot grants, got %v", sub.Scope)
	}
	if sub.Extra["arz/folder"] != "atlas/main" {
		t.Fatalf("session folder must snapshot grants, got %v", sub.Extra)
	}
}

// A grants backend that is DOWN (not "no grants") fails the login closed: no
// token is minted with a masked empty scope (spec 5/1 § Login-time scope
// snapshot, "fail closed" on 5xx).
func TestOAuthSessionGrantsDownFailsClosed(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	o := newOAuth(t, a, nil, downGrants{})

	req := httptest.NewRequest("GET", "/auth/google/callback", nil)
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	o.dispatch(rec, req, "google", "g-down", "Dan", auth.StateIntent{})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("grants-down login must fail closed (503), got %d", rec.Code)
	}
}

// The CSRF state cookie round-trips: redirect writes a signed state + PKCE
// challenge cookie that the callback prologue accepts.
func TestOAuthRedirectStateRoundTrips(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	cfg := &core.Config{AuthSecret: "csrf-key", AuthBaseURL: "https://auth.example", GoogleClientID: "gid"}
	o := newOAuth(t, a, cfg, nil)

	rec := httptest.NewRecorder()
	o.redirect("google")(rec, httptest.NewRequest("GET", "/auth/google", nil))
	if rec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("redirect status %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "accounts.google.com") || !strings.Contains(loc, "code_challenge_method=S256") {
		t.Fatalf("redirect URL missing provider/PKCE: %s", loc)
	}
	// Re-present the state cookie + state param to the callback prologue.
	state := cookieValue(rec.Result().Cookies(), "oauth_state")
	cb := httptest.NewRequest("GET", "/auth/google/callback?code=abc&state="+state, nil)
	for _, c := range rec.Result().Cookies() {
		cb.AddCookie(c)
	}
	_, _, _, ok := o.callbackCode(httptest.NewRecorder(), cb)
	if !ok {
		t.Fatal("callback must accept the state written by redirect")
	}
	// A tampered state is rejected.
	bad := httptest.NewRequest("GET", "/auth/google/callback?code=abc&state=forged", nil)
	bad.AddCookie(&http.Cookie{Name: "oauth_state", Value: "forged"})
	if _, _, _, ok := o.callbackCode(httptest.NewRecorder(), bad); ok {
		t.Fatal("forged state must be rejected")
	}
}

// ?intent=link must carry the BARE current sub. It used to carry the raw JWT
// claim ("user:google:g-1"), which dispatch wrote straight into
// auth_users.user_id — forking a second account keyed "user:google:g-1" instead
// of linking to the first, and double-prefixing the next mint to
// "user:user:google:g-1".
func TestOAuthLinkIntentCarriesBareSub(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	cfg := &core.Config{AuthSecret: "csrf-key", AuthBaseURL: "https://auth.example", GitHubClientID: "gid"}
	o := newOAuth(t, a, cfg, nil)

	// First login establishes the account.
	first := httptest.NewRequest("GET", "/auth/google/callback", nil)
	first.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	o.dispatch(rec, first, "google", "g-1", "Alice", auth.StateIntent{})
	token := jsonField(t, rec.Body.Bytes(), "token")

	// Alice, signed in, starts a GitHub link.
	link := httptest.NewRequest("GET", "/auth/github?intent=link", nil)
	link.Header.Set("Authorization", "Bearer "+token)
	linkRec := httptest.NewRecorder()
	o.redirect("github")(linkRec, link)

	state := cookieValue(linkRec.Result().Cookies(), "oauth_state")
	cb := httptest.NewRequest("GET", "/auth/github/callback?code=abc&state="+state, nil)
	for _, c := range linkRec.Result().Cookies() {
		cb.AddCookie(c)
	}
	_, _, intent, ok := o.callbackCode(httptest.NewRecorder(), cb)
	if !ok {
		t.Fatal("callback rejected the state redirect wrote")
	}
	if intent.LinkFrom != "google:g-1" {
		t.Fatalf("LinkFrom = %q, want bare google:g-1", intent.LinkFrom)
	}

	// Completing the link attaches to the SAME account, never a second one.
	cb.Header.Set("Accept", "application/json")
	linkDone := httptest.NewRecorder()
	o.dispatch(linkDone, cb, "github", "gh-1", "Alice", intent)
	if linkDone.Code != http.StatusOK {
		t.Fatalf("link dispatch status %d: %s", linkDone.Code, linkDone.Body.String())
	}
	var users int
	if err := db.QueryRow(`SELECT COUNT(*) FROM auth_users`).Scan(&users); err != nil {
		t.Fatal(err)
	}
	if users != 1 {
		t.Fatalf("linking forked the account: %d auth_users rows, want 1", users)
	}
	if !userExists(db, "google:g-1") {
		t.Fatal("canonical account google:g-1 must survive the link")
	}
	sub, err := auth.VerifyToken(jsonField(t, linkDone.Body.Bytes(), "token"), a.LocalKeySet())
	if err != nil {
		t.Fatal(err)
	}
	if sub.Sub != "user:google:g-1" {
		t.Fatalf("post-link sub = %q, want user:google:g-1 (no double prefix)", sub.Sub)
	}
}

// The point of the whole exercise: a LINKED ALIAS logging in must mint the
// ACCOUNT's canonical sub, so it lands on the account's acl rows. Before this,
// linking a second provider produced a login alias with none of the account's
// authority (BUGS N1). fakeGrants is keyed by the sub authd looks up, so a
// grant present only for the canonical sub proves the resolution happened.
func TestOAuthAliasLoginResolvesToCanonicalSub(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	// Only the canonical sub holds authority. github:gh-1 holds none.
	o := newOAuth(t, a, nil, fakeGrants{
		"google:g-1": {Scope: []string{"tasks:read"}, Folder: "atlas/main"},
	})

	// Alice's account, then a GitHub identity linked to it.
	o.dispatch(httptest.NewRecorder(), jsonReq(), "google", "g-1", "Alice", auth.StateIntent{})
	o.dispatch(httptest.NewRecorder(), jsonReq(), "github", "gh-1", "Alice",
		auth.StateIntent{Intent: "link", LinkFrom: "google:g-1"})

	// A LATER, plain login through the alias — no link intent this time.
	rec := httptest.NewRecorder()
	o.dispatch(rec, jsonReq(), "github", "gh-1", "Alice", auth.StateIntent{})
	if rec.Code != http.StatusOK {
		t.Fatalf("alias login status %d: %s", rec.Code, rec.Body.String())
	}
	sub, err := auth.VerifyToken(jsonField(t, rec.Body.Bytes(), "token"), a.LocalKeySet())
	if err != nil {
		t.Fatal(err)
	}
	if sub.Sub != "user:google:g-1" {
		t.Fatalf("alias login sub = %q, want the canonical user:google:g-1", sub.Sub)
	}
	// ...and it therefore carries the account's authority.
	if !auth.HasScope(sub.Scope, "tasks", "read") {
		t.Fatalf("alias login must see the account's grants, got scope %v", sub.Scope)
	}
	if sub.Extra["arz/folder"] != "atlas/main" {
		t.Fatalf("alias login folder = %q, want atlas/main", sub.Extra["arz/folder"])
	}
}

// The canonical login is unaffected: it resolves to itself, and resolution must
// not disturb the account row or spawn a second one.
func TestOAuthCanonicalLoginUnaffectedByResolution(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	o := newOAuth(t, a, nil, fakeGrants{
		"google:g-1": {Scope: []string{"tasks:read"}, Folder: "atlas/main"},
	})

	o.dispatch(httptest.NewRecorder(), jsonReq(), "google", "g-1", "Alice", auth.StateIntent{})
	o.dispatch(httptest.NewRecorder(), jsonReq(), "github", "gh-1", "Alice",
		auth.StateIntent{Intent: "link", LinkFrom: "google:g-1"})

	rec := httptest.NewRecorder()
	o.dispatch(rec, jsonReq(), "google", "g-1", "Alice", auth.StateIntent{})
	sub, err := auth.VerifyToken(jsonField(t, rec.Body.Bytes(), "token"), a.LocalKeySet())
	if err != nil {
		t.Fatal(err)
	}
	if sub.Sub != "user:google:g-1" {
		t.Fatalf("canonical login sub = %q, want user:google:g-1", sub.Sub)
	}
	if !auth.HasScope(sub.Scope, "tasks", "read") {
		t.Fatalf("canonical login lost its grants: %v", sub.Scope)
	}
	var users int
	if err := db.QueryRow(`SELECT COUNT(*) FROM auth_users`).Scan(&users); err != nil {
		t.Fatal(err)
	}
	if users != 1 {
		t.Fatalf("%d auth_users rows, want 1", users)
	}
}

// The security half: an UNLINKED provider sub must resolve to ITSELF, never to
// somebody else's account. A resolver that fell back to a partial match, or
// keyed on provider alone, would hand Mallory Alice's grants.
func TestOAuthUnlinkedSubDoesNotResolveToAnotherAccount(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	o := newOAuth(t, a, nil, fakeGrants{
		"google:g-1": {Scope: []string{"tasks:read"}, Folder: "atlas/main"},
	})

	o.dispatch(httptest.NewRecorder(), jsonReq(), "google", "g-1", "Alice", auth.StateIntent{})

	// Mallory: same provider, different provider_sub, linked to nobody.
	rec := httptest.NewRecorder()
	o.dispatch(rec, jsonReq(), "google", "g-666", "Mallory", auth.StateIntent{})
	sub, err := auth.VerifyToken(jsonField(t, rec.Body.Bytes(), "token"), a.LocalKeySet())
	if err != nil {
		t.Fatal(err)
	}
	if sub.Sub != "user:google:g-666" {
		t.Fatalf("unlinked sub resolved to %q — must stay its own principal", sub.Sub)
	}
	if len(sub.Scope) != 0 {
		t.Fatalf("unlinked sub picked up grants it does not hold: %v", sub.Scope)
	}
	// A *different provider* with an identical provider_sub is likewise separate.
	rec2 := httptest.NewRecorder()
	o.dispatch(rec2, jsonReq(), "github", "g-1", "Trudy", auth.StateIntent{})
	sub2, err := auth.VerifyToken(jsonField(t, rec2.Body.Bytes(), "token"), a.LocalKeySet())
	if err != nil {
		t.Fatal(err)
	}
	if sub2.Sub != "user:github:g-1" {
		t.Fatalf("github:g-1 resolved to %q — provider must be part of the key", sub2.Sub)
	}
	if len(sub2.Scope) != 0 {
		t.Fatalf("github:g-1 picked up google:g-1's grants: %v", sub2.Scope)
	}
}

// Resolving the alias at mint means the token subject no longer says WHICH
// login was presented — and nothing downstream (proxyd's X-User-Sub, the resreg
// audit actor derived from it) can recover that. The login audit row is where
// the fact survives: actor = the account, resource = the credential presented.
func TestOAuthLoginAuditRecordsTheActualLogin(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	o := newOAuth(t, a, nil, nil)
	audit.Init(db, "test")
	t.Cleanup(func() { audit.Init(nil, "") })

	o.dispatch(httptest.NewRecorder(), jsonReq(), "google", "g-1", "Alice", auth.StateIntent{})
	o.dispatch(httptest.NewRecorder(), jsonReq(), "github", "gh-1", "Alice",
		auth.StateIntent{Intent: "link", LinkFrom: "google:g-1"})

	rec := httptest.NewRecorder()
	o.dispatch(rec, jsonReq(), "github", "gh-1", "Alice", auth.StateIntent{})
	if rec.Code != http.StatusOK {
		t.Fatalf("alias login status %d", rec.Code)
	}

	var actor, resource string
	err := db.QueryRow(
		`SELECT actor, resource FROM audit_log
		  WHERE category = ? AND action = 'login' ORDER BY id DESC LIMIT 1`,
		audit.CategoryAuthN).Scan(&actor, &resource)
	if err != nil {
		t.Fatalf("no login audit row: %v", err)
	}
	if actor != "user:google:g-1" {
		t.Fatalf("audit actor = %q, want the canonical user:google:g-1", actor)
	}
	if resource != "github:gh-1" {
		t.Fatalf("audit resource = %q, want the presented login github:gh-1", resource)
	}
}

// jsonReq is a callback request that asks for the JSON login delivery.
func jsonReq() *http.Request {
	r := httptest.NewRequest("GET", "/auth/callback", nil)
	r.Header.Set("Accept", "application/json")
	return r
}

// downGrants always reports the backend is unavailable (a non-ErrNoGrants error).
type downGrants struct{}

func (downGrants) FetchGrants(context.Context, string) (GrantsSnapshot, error) {
	return GrantsSnapshot{}, errGrantsUnavailable
}

func cookieValue(cs []*http.Cookie, name string) string {
	for _, c := range cs {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}

func jsonField(t *testing.T, body []byte, key string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("decode json: %v (body=%s)", err, body)
	}
	s, _ := m[key].(string)
	return s
}

// The post-login return path rides the SIGNED state, sourced from the
// auth_return cookie proxyd drops when it bounces an unauthenticated caller
// here. Without this the cookie was written and never read, so a bounced caller
// landed on / — spec 5/31 needs the pairing URL to survive the round-trip.
func TestOAuthRedirectCarriesAuthReturnCookie(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	cfg := &core.Config{AuthSecret: "csrf-key", AuthBaseURL: "https://auth.example", GoogleClientID: "gid"}
	o := newOAuth(t, a, cfg, nil)

	cases := []struct {
		name   string
		cookie string
		query  string
		want   string
	}{
		{"cookie", "/pair/abc123", "", "/pair/abc123"},
		{"query wins", "/pair/abc123", "?return=%2Fdash%2Fstatus", "/dash/status"},
		{"absolute rejected", "https://evil.test/x", "", ""},
		{"scheme-relative rejected", "//evil.test/x", "", ""},
		{"none", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/auth/google"+c.query, nil)
			if c.cookie != "" {
				req.AddCookie(&http.Cookie{Name: "auth_return", Value: c.cookie})
			}
			rec := httptest.NewRecorder()
			o.redirect("google")(rec, req)

			state := cookieValue(rec.Result().Cookies(), "oauth_state")
			cb := httptest.NewRequest("GET", "/auth/google/callback?code=abc&state="+state, nil)
			for _, ck := range rec.Result().Cookies() {
				cb.AddCookie(ck)
			}
			_, _, intent, ok := o.callbackCode(httptest.NewRecorder(), cb)
			if !ok {
				t.Fatal("callback rejected the state redirect wrote")
			}
			if intent.Return != c.want {
				t.Errorf("intent.Return = %q, want %q", intent.Return, c.want)
			}
		})
	}
}

// The cookie is cleared on consume so it cannot steer a later, unrelated login.
func TestOAuthRedirectClearsAuthReturnCookie(t *testing.T) {
	db := testDB(t)
	a := newTestAuthd(t, db)
	cfg := &core.Config{AuthSecret: "csrf-key", AuthBaseURL: "https://auth.example", GoogleClientID: "gid"}
	o := newOAuth(t, a, cfg, nil)

	req := httptest.NewRequest("GET", "/auth/google", nil)
	req.AddCookie(&http.Cookie{Name: "auth_return", Value: "/pair/abc123"})
	rec := httptest.NewRecorder()
	o.redirect("google")(rec, req)

	for _, c := range rec.Result().Cookies() {
		if c.Name == "auth_return" {
			if c.MaxAge >= 0 {
				t.Fatalf("auth_return not cleared: MaxAge=%d value=%q", c.MaxAge, c.Value)
			}
			return
		}
	}
	t.Fatal("redirect did not clear the auth_return cookie")
}

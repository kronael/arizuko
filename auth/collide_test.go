package auth

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/kronael/arizuko/store"
)

// dispatchOAuth has seven branches. Each test seeds the store, builds
// a request reflecting "is the caller already logged in?" and asserts
// the response (collision page vs. session JWT).

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// reqWithSession returns a request whose Bearer JWT carries `sub` so
// currentSub() resolves to it (mimics a logged-in user).
func reqWithSession(t *testing.T, s *store.Store, secret []byte, sub string) *http.Request {
	t.Helper()
	jwt := mintJWT(secret, sub, "n", nil, jwtTTL)
	r := httptest.NewRequest("GET", "/auth/x/callback", nil)
	r.Header.Set("Authorization", "Bearer "+jwt)
	return r
}

func reqNoSession() *http.Request {
	return httptest.NewRequest("GET", "/auth/x/callback", nil)
}

// jwtFromBody extracts the minted JWT out of issueSession's HTML envelope.
func jwtFromBody(t *testing.T, body string) string {
	t.Helper()
	i := strings.Index(body, `'jwt',"`)
	if i < 0 {
		t.Fatalf("no jwt in body: %s", body)
	}
	rest := body[i+len(`'jwt',"`):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		t.Fatalf("malformed jwt envelope: %s", body)
	}
	return rest[:j]
}

func mustClaims(t *testing.T, secret []byte, body string) Claims {
	t.Helper()
	c, err := VerifyJWT(secret, jwtFromBody(t, body))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	return c
}

// Case 1: link intent, sub already linked to LinkFrom → no-op refresh.
func TestDispatch_LinkAlreadyLinked(t *testing.T) {
	s := newTestStore(t)
	secret := testSecret
	if err := s.CreateAuthUser("google:a", "a", "", "A"); err != nil {
		t.Fatal(err)
	}
	if err := s.LinkSubToCanonical("github:b", "B", "google:a"); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	dispatchOAuth(w, reqWithSession(t, s, secret, "google:a"), s, secret,
		"github:b", "B", stateIntent{Intent: "link", LinkFrom: "google:a"}, false)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 (refresh), got %d body=%s", w.Code, w.Body.String())
	}
	c := mustClaims(t, secret, w.Body.String())
	if c.Sub != "google:a" {
		t.Fatalf("want canonical google:a, got %q", c.Sub)
	}
}

// Case 2: link intent, sub canonical for a *different* user → collision.
func TestDispatch_LinkConflict(t *testing.T) {
	s := newTestStore(t)
	secret := testSecret
	if err := s.CreateAuthUser("google:a", "a", "", "A"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAuthUser("github:c", "c", "", "C"); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	dispatchOAuth(w, reqWithSession(t, s, secret, "google:a"), s, secret,
		"github:c", "C", stateIntent{Intent: "link", LinkFrom: "google:a"}, false)
	if w.Code != http.StatusOK {
		t.Fatalf("expected collision page (200 HTML), got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Account collision") {
		t.Fatalf("expected collision page, got: %s", w.Body.String())
	}
}

// Case 3: link intent, sub is new → write the link, refresh.
func TestDispatch_LinkNew(t *testing.T) {
	s := newTestStore(t)
	secret := testSecret
	if err := s.CreateAuthUser("google:a", "a", "", "A"); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	dispatchOAuth(w, reqWithSession(t, s, secret, "google:a"), s, secret,
		"github:b", "B", stateIntent{Intent: "link", LinkFrom: "google:a"}, false)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 (refresh), got %d body=%s", w.Code, w.Body.String())
	}
	c := mustClaims(t, secret, w.Body.String())
	if c.Sub != "google:a" {
		t.Fatalf("want canonical google:a, got %q", c.Sub)
	}
	if got := s.CanonicalSub("github:b"); got != "google:a" {
		t.Fatalf("github:b should resolve to google:a, got %q", got)
	}
}

// Case 4: no intent, session active, sub new → collision.
func TestDispatch_PassiveCollideNewSub(t *testing.T) {
	s := newTestStore(t)
	secret := testSecret
	if err := s.CreateAuthUser("google:a", "a", "", "A"); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	dispatchOAuth(w, reqWithSession(t, s, secret, "google:a"), s, secret,
		"github:fresh", "Fresh", stateIntent{}, false)
	if !strings.Contains(w.Body.String(), "Account collision") {
		t.Fatalf("expected collision page: %s", w.Body.String())
	}
}

// Case 5: no intent, session active, sub canonical for different user → collision.
func TestDispatch_PassiveCollideExistingDifferentUser(t *testing.T) {
	s := newTestStore(t)
	secret := testSecret
	if err := s.CreateAuthUser("google:a", "a", "", "A"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAuthUser("github:c", "c", "", "C"); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	dispatchOAuth(w, reqWithSession(t, s, secret, "google:a"), s, secret,
		"github:c", "C", stateIntent{}, false)
	if !strings.Contains(w.Body.String(), "Account collision") {
		t.Fatalf("expected collision page: %s", w.Body.String())
	}
}

// Case 6: no intent, no session, sub new → create + log in.
func TestDispatch_NewLogin(t *testing.T) {
	s := newTestStore(t)
	secret := testSecret
	w := httptest.NewRecorder()
	dispatchOAuth(w, reqNoSession(), s, secret,
		"google:newbie", "Newbie", stateIntent{}, false)
	c := mustClaims(t, secret, w.Body.String())
	if c.Sub != "google:newbie" {
		t.Fatalf("got %q, want google:newbie", c.Sub)
	}
	if _, ok := s.AuthUserBySub("google:newbie"); !ok {
		t.Fatal("expected newbie persisted")
	}
}

// Case 7: no intent, no session, sub exists (linked) → log in via canonical.
func TestDispatch_LinkedSubLogsInAsCanonical(t *testing.T) {
	s := newTestStore(t)
	secret := testSecret
	if err := s.CreateAuthUser("google:a", "a", "", "A"); err != nil {
		t.Fatal(err)
	}
	if err := s.LinkSubToCanonical("github:b", "B", "google:a"); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	dispatchOAuth(w, reqNoSession(), s, secret,
		"github:b", "B", stateIntent{}, false)
	c := mustClaims(t, secret, w.Body.String())
	if c.Sub != "google:a" {
		t.Fatalf("got %q, want google:a (canonical)", c.Sub)
	}
}

// Same-canonical refresh (passive intent, session matches sub's canonical).
func TestDispatch_SameCanonicalRefresh(t *testing.T) {
	s := newTestStore(t)
	secret := testSecret
	if err := s.CreateAuthUser("google:a", "a", "", "A"); err != nil {
		t.Fatal(err)
	}
	if err := s.LinkSubToCanonical("github:b", "B", "google:a"); err != nil {
		t.Fatal(err)
	}
	// Logged in as google:a, callback returns github:b which resolves to google:a.
	w := httptest.NewRecorder()
	dispatchOAuth(w, reqWithSession(t, s, secret, "google:a"), s, secret,
		"github:b", "B", stateIntent{}, false)
	c := mustClaims(t, secret, w.Body.String())
	if c.Sub != "google:a" {
		t.Fatalf("got %q, want google:a", c.Sub)
	}
}

// Collision-page POST: link choice writes the link + issues new session.
func TestCollideChoice_Link(t *testing.T) {
	s := newTestStore(t)
	secret := testSecret
	if err := s.CreateAuthUser("google:a", "a", "", "A"); err != nil {
		t.Fatal(err)
	}
	tok := signCollide(secret, collideToken{
		NewSub: "github:b", NewName: "B", CurrentSub: "google:a",
	})
	form := url.Values{"token": {tok}, "choice": {"link"}}
	r := httptest.NewRequest("POST", "/auth/collide", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handleCollideChoice(s, secret, false).ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", w.Code, w.Body.String())
	}
	if got := s.CanonicalSub("github:b"); got != "google:a" {
		t.Fatalf("link not persisted: %q", got)
	}
	c := mustClaims(t, secret, w.Body.String())
	if c.Sub != "google:a" {
		t.Fatalf("session not on canonical: %q", c.Sub)
	}
}

// Collision-page POST: logout choice clears refresh cookie + logs in as new sub.
func TestCollideChoice_Logout(t *testing.T) {
	s := newTestStore(t)
	secret := testSecret
	if err := s.CreateAuthUser("google:a", "a", "", "A"); err != nil {
		t.Fatal(err)
	}
	tok := signCollide(secret, collideToken{
		NewSub: "github:fresh", NewName: "Fresh", CurrentSub: "google:a",
	})
	form := url.Values{"token": {tok}, "choice": {"logout"}}
	r := httptest.NewRequest("POST", "/auth/collide", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handleCollideChoice(s, secret, false).ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", w.Code, w.Body.String())
	}
	c := mustClaims(t, secret, w.Body.String())
	if c.Sub != "github:fresh" {
		t.Fatalf("expected logged in as github:fresh, got %q", c.Sub)
	}
	if _, ok := s.AuthUserBySub("github:fresh"); !ok {
		t.Fatal("expected github:fresh row created")
	}
}

// Collision-page POST: rejects expired/invalid tokens.
func TestCollideChoice_BadToken(t *testing.T) {
	s := newTestStore(t)
	secret := testSecret
	form := url.Values{"token": {"garbage"}, "choice": {"link"}}
	r := httptest.NewRequest("POST", "/auth/collide", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handleCollideChoice(s, secret, false).ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", w.Code)
	}
}

// Collision-page POST: link choice between two existing canonical users
// is refused (we don't merge accounts via this UI).
func TestCollideChoice_RefuseAccountMerge(t *testing.T) {
	s := newTestStore(t)
	secret := testSecret
	if err := s.CreateAuthUser("google:a", "a", "", "A"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAuthUser("github:c", "c", "", "C"); err != nil {
		t.Fatal(err)
	}
	tok := signCollide(secret, collideToken{
		NewSub: "github:c", NewName: "C", NewCanonical: "github:c",
		CurrentSub: "google:a",
	})
	form := url.Values{"token": {tok}, "choice": {"link"}}
	r := httptest.NewRequest("POST", "/auth/collide", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handleCollideChoice(s, secret, false).ServeHTTP(w, r)
	if w.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d body=%s", w.Code, w.Body.String())
	}
}

// State token round-trip with intent payload.
func TestStateIntentRoundTrip(t *testing.T) {
	secret := testSecret
	want := stateIntent{Intent: "link", LinkFrom: "google:a", Return: "/dash/profile"}
	state := signStateP(secret, want)
	r := httptest.NewRequest("GET", "/cb?state="+url.QueryEscape(state), nil)
	r.AddCookie(&http.Cookie{Name: "oauth_state", Value: state})
	got, ok := verifyState(secret, r)
	if !ok {
		t.Fatal("verifyState failed")
	}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// State token without payload still verifies (back-compat: 3 parts).
func TestStateIntentEmpty(t *testing.T) {
	secret := testSecret
	state := signState(secret)
	r := httptest.NewRequest("GET", "/cb?state="+url.QueryEscape(state), nil)
	r.AddCookie(&http.Cookie{Name: "oauth_state", Value: state})
	got, ok := verifyState(secret, r)
	if !ok {
		t.Fatal("verifyState failed")
	}
	if got != (stateIntent{}) {
		t.Fatalf("expected empty intent, got %+v", got)
	}
}

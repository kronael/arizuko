package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// matchEmailAllowlist — who is allowed to sign in at all.

func TestMatchEmailAllowlistFailsClosed(t *testing.T) {
	cases := []struct {
		name             string
		email, allowlist string
		want             bool
	}{
		{"exact address", "alice@example.com", "alice@example.com", true},
		{"one of several", "bob@example.com", "alice@example.com,bob@example.com", true},
		{"a domain wildcard", "alice@example.com", "*@example.com", true},
		{"whitespace around a pattern is trimmed", "alice@example.com", " *@example.com , x@y.z", true},

		{"a different domain", "alice@evil.example", "*@example.com", false},
		{"the domain as a prefix of a longer one", "alice@example.com.evil.example", "*@example.com", false},
		{"the address as a prefix", "alice@example.commercial", "alice@example.com", false},

		// A missing or blank allowlist must admit nobody. "Unset means open"
		// is how a config typo becomes a public login page.
		{"an empty allowlist", "alice@example.com", "", false},
		{"an allowlist of separators", "alice@example.com", " , , ", false},
		{"an empty email", "", "*@example.com", false},

		// filepath.Match is case-sensitive, so a capitalised address is
		// refused rather than admitted. Fail-closed is the safe direction,
		// but an operator hitting it needs the behaviour written down.
		{"a capitalised address against a lowercase pattern", "Alice@Example.com", "*@example.com", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := matchEmailAllowlist(c.email, c.allowlist); got != c.want {
				t.Errorf("matchEmailAllowlist(%q, %q) = %v, want %v", c.email, c.allowlist, got, c.want)
			}
		})
	}
}

// A bare "*" admits every address. That is a legitimate operator choice, but it
// must be a choice — it is asserted here so nobody "simplifies" a blank
// allowlist into the same thing.
func TestMatchEmailAllowlistStarAdmitsEveryone(t *testing.T) {
	if !matchEmailAllowlist("anyone@anywhere.example", "*") {
		t.Error(`"*" must admit every address`)
	}
	if matchEmailAllowlist("anyone@anywhere.example", "") {
		t.Error(`"" must admit nobody; it is not a synonym for "*"`)
	}
}

// checkGitHubOrgMember — the other admission gate.

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func withHTTPClient(t *testing.T, rt roundTripFunc) {
	t.Helper()
	prev := httpClient
	httpClient = &http.Client{Transport: rt}
	t.Cleanup(func() { httpClient = prev })
}

func statusResponse(code int) *http.Response {
	return &http.Response{
		StatusCode: code,
		Body:       io.NopCloser(strings.NewReader("")),
		Header:     http.Header{},
	}
}

// GitHub answers 204 for "is a member", 302 for "requester is a member but the
// target is not visible", and 404 otherwise. Only 204 is membership; treating
// any 2xx or any non-404 as a yes admits non-members.
func TestCheckGitHubOrgMemberAcceptsOnly204(t *testing.T) {
	for _, code := range []int{200, 201, 204, 302, 304, 401, 403, 404, 500} {
		withHTTPClient(t, func(*http.Request) (*http.Response, error) {
			return statusResponse(code), nil
		})
		got := checkGitHubOrgMember(context.Background(), "tok", "acme", "octocat")
		if want := code == 204; got != want {
			t.Errorf("status %d: membership = %v, want %v", code, got, want)
		}
	}
}

func TestCheckGitHubOrgMemberFailsClosedOnATransportError(t *testing.T) {
	withHTTPClient(t, func(*http.Request) (*http.Response, error) {
		return nil, io.ErrUnexpectedEOF
	})
	if checkGitHubOrgMember(context.Background(), "tok", "acme", "octocat") {
		t.Error("a failed org check must read as 'not a member', never as a pass")
	}
}

// The org and username come from configuration and from the provider, and both
// land in a URL path. Without escaping, a username of "../../user" would turn
// the membership probe into a different endpoint that answers 200 for anyone.
func TestCheckGitHubOrgMemberEscapesThePathSegments(t *testing.T) {
	var got string
	withHTTPClient(t, func(r *http.Request) (*http.Response, error) {
		got = r.URL.String()
		return statusResponse(404), nil
	})
	checkGitHubOrgMember(context.Background(), "tok", "acme/../..", "octocat/../../user")

	if strings.Contains(got, "..") && !strings.Contains(got, "%2F") {
		t.Errorf("path traversal reached the URL unescaped: %s", got)
	}
	if !strings.HasPrefix(got, "https://api.github.com/orgs/") {
		t.Errorf("the probe left the members endpoint: %s", got)
	}
	if u, err := url.Parse(got); err != nil {
		t.Errorf("unparseable URL: %v", err)
	} else if n := len(strings.Split(strings.Trim(u.EscapedPath(), "/"), "/")); n != 4 {
		t.Errorf("want /orgs/<org>/members/<user> (4 segments), got %d in %s", n, u.EscapedPath())
	}
}

func TestCheckGitHubOrgMemberSendsTheToken(t *testing.T) {
	var auth, accept string
	withHTTPClient(t, func(r *http.Request) (*http.Response, error) {
		auth, accept = r.Header.Get("Authorization"), r.Header.Get("Accept")
		return statusResponse(204), nil
	})
	checkGitHubOrgMember(context.Background(), "gho_secret", "acme", "octocat")
	if auth != "Bearer gho_secret" {
		t.Errorf("Authorization = %q; an unauthenticated probe 404s for every private org", auth)
	}
	if accept != "application/vnd.github+json" {
		t.Errorf("Accept = %q, want the versioned GitHub media type", accept)
	}
}

// SignState / verifyState — the OAuth round-trip CSRF token.

func callbackReq(state, cookie string) *http.Request {
	r := httptest.NewRequest("GET", "https://host/auth/callback?code=abc&state="+url.QueryEscape(state), nil)
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: "oauth_state", Value: cookie})
	}
	return r
}

func TestStateRoundTripsWithAndWithoutAPayload(t *testing.T) {
	secret := []byte("csrf-secret")

	plain := signStateP(secret, stateIntent{})
	if got, ok := verifyState(secret, callbackReq(plain, plain)); !ok {
		t.Fatal("a freshly signed state failed verification; every login would fail")
	} else if got.Intent != "" || got.LinkFrom != "" || got.Return != "" {
		t.Errorf("a payload-free state produced %+v, want the zero intent", got)
	}

	want := NewLinkIntent("google:123", "/dash/me/connections")
	signed := SignState(secret, want)
	got, ok := VerifyState(secret, callbackReq(signed, signed))
	if !ok {
		t.Fatal("a state carrying a link intent failed verification")
	}
	if got != want {
		t.Errorf("intent round trip: got %+v, want %+v", got, want)
	}
}

func TestVerifyStateRejectsEveryTamperedForm(t *testing.T) {
	secret := []byte("csrf-secret")
	good := signStateP(secret, NewLinkIntent("google:123", "/dash/"))
	parts := strings.Split(good, ".")

	tamper := func(i int, v string) string {
		p := append([]string(nil), parts...)
		p[i] = v
		return strings.Join(p, ".")
	}
	evilPayload := base64.RawURLEncoding.EncodeToString(
		[]byte(`{"i":"link","f":"google:999","r":"https://evil.example/"}`))

	cases := []struct{ name, state string }{
		{"a flipped signature", tamper(3, strings.Repeat("0", len(parts[3])))},
		{"a swapped payload", tamper(2, evilPayload)},
		{"a rewritten timestamp", tamper(0, strconv.FormatInt(time.Now().Unix()-1, 10))},
		{"a replaced nonce", tamper(1, "aaaaaaaaaaaaaaaaaaaaaa")},
		{"a truncated state", parts[0] + "." + parts[3]},
		{"an over-long state", good + ".extra"},
		{"an empty state", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, ok := verifyState(secret, callbackReq(c.state, c.state)); ok {
				t.Errorf("verifyState accepted %s", c.name)
			}
		})
	}

	if _, ok := verifyState([]byte("a-different-secret"), callbackReq(good, good)); ok {
		t.Error("a state signed under another secret verified")
	}
}

// The state is a double submit: the query parameter must equal the cookie. An
// attacker can put anything in the query of the URL they send the victim, but
// cannot write the victim's cookie for our origin.
func TestVerifyStateRequiresTheCookieToMatchTheQuery(t *testing.T) {
	secret := []byte("csrf-secret")
	mine := signStateP(secret, stateIntent{})
	attackers := signStateP(secret, stateIntent{})

	if _, ok := verifyState(secret, callbackReq(attackers, mine)); ok {
		t.Error("a validly-signed state from another session was accepted against my cookie")
	}
	if _, ok := verifyState(secret, callbackReq(mine, "")); ok {
		t.Error("a callback with no oauth_state cookie was accepted")
	}
	if _, ok := verifyState(secret, callbackReq("", mine)); ok {
		t.Error("a callback with no state query parameter was accepted")
	}
}

// stateTTL bounds how long a captured authorize URL stays usable.
func TestVerifyStateRejectsStaleAndFutureTimestamps(t *testing.T) {
	secret := []byte("csrf-secret")

	forge := func(at time.Time) string {
		signed := strconv.FormatInt(at.Unix(), 10) + ".nonce123"
		mac := hmac.New(sha256.New, secret)
		mac.Write([]byte(signed))
		return signed + "." + hex.EncodeToString(mac.Sum(nil))
	}

	fresh := forge(time.Now())
	if _, ok := verifyState(secret, callbackReq(fresh, fresh)); !ok {
		t.Fatal("the hand-built control state failed; the rest of this test proves nothing")
	}

	stale := forge(time.Now().Add(-stateTTL - time.Minute))
	if _, ok := verifyState(secret, callbackReq(stale, stale)); ok {
		t.Errorf("a state older than stateTTL (%s) was accepted", stateTTL)
	}

	future := forge(time.Now().Add(time.Hour))
	if _, ok := verifyState(secret, callbackReq(future, future)); ok {
		t.Error("a state timestamped in the future was accepted; it would outlive stateTTL")
	}
}

func TestSignStateIsUnpredictable(t *testing.T) {
	secret := []byte("csrf-secret")
	seen := map[string]bool{}
	for i := 0; i < 32; i++ {
		s := signStateP(secret, stateIntent{})
		if seen[s] {
			t.Fatalf("signStateP repeated %q; two sessions would share a CSRF token", s)
		}
		seen[s] = true
	}
}

// PKCE — the verifier must be single-use and must actually bind the challenge.

func TestWritePKCEChallengeIsTheS256OfTheStoredVerifier(t *testing.T) {
	w := httptest.NewRecorder()
	challenge, err := WritePKCE(w, true)
	if err != nil {
		t.Fatal(err)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "oauth_pkce" {
		t.Fatalf("want one oauth_pkce cookie, got %+v", cookies)
	}
	c := cookies[0]
	if !c.HttpOnly {
		t.Error("the verifier must be HttpOnly — a script that can read it defeats PKCE")
	}
	if !c.Secure {
		t.Error("secure=true was requested but the verifier cookie is not Secure")
	}
	sum := sha256.Sum256([]byte(c.Value))
	if want := base64.RawURLEncoding.EncodeToString(sum[:]); challenge != want {
		t.Errorf("challenge %q is not S256(verifier) %q; the provider would reject every exchange", challenge, want)
	}
}

func TestConsumePKCEReturnsTheVerifierAndClearsIt(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "https://host/auth/callback", nil)
	r.AddCookie(&http.Cookie{Name: "oauth_pkce", Value: "the-verifier"})

	if got := consumePKCE(w, r, true); got != "the-verifier" {
		t.Fatalf("consumePKCE = %q, want the stored verifier", got)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("want a clearing Set-Cookie, got %d", len(cookies))
	}
	if c := cookies[0]; c.Value != "" || c.MaxAge >= 0 {
		t.Errorf("verifier not cleared (value=%q MaxAge=%d); a replayed callback would reuse it", c.Value, c.MaxAge)
	}
}

func TestConsumePKCEIsEmptyWithNoCookie(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "https://host/auth/callback", nil)
	if got := consumePKCE(w, r, true); got != "" {
		t.Errorf("consumePKCE with no cookie = %q, want empty", got)
	}

	w2 := httptest.NewRecorder()
	r2 := httptest.NewRequest("GET", "https://host/auth/callback", nil)
	r2.AddCookie(&http.Cookie{Name: "oauth_pkce", Value: ""})
	if got := consumePKCE(w2, r2, true); got != "" {
		t.Errorf("consumePKCE with an empty cookie = %q, want empty", got)
	}
}

func TestWriteStateCookieIsHttpOnly(t *testing.T) {
	w := httptest.NewRecorder()
	WriteStateCookie(w, "the-state", true)
	c := w.Result().Cookies()[0]
	if c.Name != "oauth_state" {
		t.Fatalf("cookie name = %q", c.Name)
	}
	if !c.HttpOnly || !c.Secure {
		t.Errorf("state cookie must be HttpOnly+Secure, got HttpOnly=%v Secure=%v", c.HttpOnly, c.Secure)
	}
}

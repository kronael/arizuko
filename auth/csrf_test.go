package auth

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// Double-submit CSRF guards every cookie-authenticated HTML form in the
// platform — onbod's admission dashboard, webd's pairing confirm. The browser
// attaches the session cookie to a cross-site POST by itself, so the token
// echoed in the form body is the ONLY thing an attacker's page cannot supply.
// All three functions had no test.

func TestSecureRequestSeesTLSDirectlyAndThroughAProxy(t *testing.T) {
	cases := []struct {
		name  string
		build func() *http.Request
		want  bool
	}{
		{"plain http", func() *http.Request {
			return httptest.NewRequest("POST", "http://host/x", nil)
		}, false},
		{"direct TLS", func() *http.Request {
			r := httptest.NewRequest("POST", "https://host/x", nil)
			return r
		}, true},
		// proxyd terminates TLS and forwards plain http, so the header is the
		// only signal a backend has. It is a client-supplied header, which is
		// safe here only because proxyd strips inbound X-* before stamping its
		// own (SECURITY.md "Identity header trust") — and because getting it
		// wrong fails closed: a Secure cookie on a plain connection is simply
		// never sent back.
		{"behind a TLS-terminating proxy", func() *http.Request {
			r := httptest.NewRequest("POST", "http://host/x", nil)
			r.Header.Set("X-Forwarded-Proto", "https")
			return r
		}, true},
		{"proxy reporting plain http", func() *http.Request {
			r := httptest.NewRequest("POST", "http://host/x", nil)
			r.Header.Set("X-Forwarded-Proto", "http")
			return r
		}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := SecureRequest(c.build()); got != c.want {
				t.Errorf("SecureRequest = %v, want %v", got, c.want)
			}
		})
	}
}

// The minted cookie must be readable by the page (the form has to echo it),
// but SameSite=Strict is what stops it riding along on the attacker's POST.
// Losing either attribute silently disarms the whole scheme.
func TestEnsureCSRFMintsAReadableStrictCookie(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "https://host/form", nil)

	tok := EnsureCSRF(w, r, "onbod_csrf", true)
	if tok == "" {
		t.Fatal("EnsureCSRF returned an empty token")
	}

	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("want exactly one Set-Cookie, got %d", len(cookies))
	}
	c := cookies[0]
	switch {
	case c.Name != "onbod_csrf":
		t.Errorf("cookie name = %q, want the name the caller asked for", c.Name)
	case c.Value != tok:
		t.Errorf("cookie value %q != returned token %q; the double submit can never match", c.Value, tok)
	case c.HttpOnly:
		t.Error("cookie is HttpOnly — the page must be able to read it to echo it in the form")
	case !c.Secure:
		t.Error("secure=true was requested but the cookie is not Secure")
	case c.SameSite != http.SameSiteStrictMode:
		t.Errorf("SameSite = %v, want Strict — Lax still rides a top-level cross-site POST", c.SameSite)
	case c.Path != "/":
		t.Errorf("Path = %q, want / so the form and its target share the cookie", c.Path)
	}
}

func TestEnsureCSRFOmitsSecureOnPlainHTTP(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http://host/form", nil)
	EnsureCSRF(w, r, "onbod_csrf", false)
	if c := w.Result().Cookies()[0]; c.Secure {
		t.Error("Secure must follow the caller's flag; a Secure cookie on plain http never reaches the server")
	}
}

// Re-rendering the form (a validation error, a back button) must keep the
// token the browser already holds. Minting a fresh one each render would
// invalidate any form the user has open in another tab.
func TestEnsureCSRFReusesAnExistingCookie(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "https://host/form", nil)
	r.AddCookie(&http.Cookie{Name: "onbod_csrf", Value: "already-minted"})

	if got := EnsureCSRF(w, r, "onbod_csrf", true); got != "already-minted" {
		t.Errorf("EnsureCSRF = %q, want the cookie the browser already sent", got)
	}
	if n := len(w.Result().Cookies()); n != 0 {
		t.Errorf("re-minted the cookie (%d Set-Cookie headers); an open form in another tab would break", n)
	}
}

func TestEnsureCSRFReplacesAnEmptyCookie(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "https://host/form", nil)
	r.AddCookie(&http.Cookie{Name: "onbod_csrf", Value: ""})

	tok := EnsureCSRF(w, r, "onbod_csrf", true)
	if tok == "" {
		t.Fatal("an empty cookie must be replaced, not returned")
	}
	if n := len(w.Result().Cookies()); n != 1 {
		t.Fatalf("want a fresh Set-Cookie, got %d", n)
	}
}

func TestEnsureCSRFMintsADistinctTokenPerBrowser(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 32; i++ {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "https://host/form", nil)
		tok := EnsureCSRF(w, r, "onbod_csrf", true)
		if seen[tok] {
			t.Fatalf("EnsureCSRF repeated token %q; one user's token would authorise another's form", tok)
		}
		seen[tok] = true
	}
}

func postWith(cookie, field string) *http.Request {
	form := url.Values{}
	if field != "" {
		form.Set(CSRFField, field)
	}
	r := httptest.NewRequest("POST", "https://host/confirm", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: "onbod_csrf", Value: cookie})
	}
	return r
}

// Every failure mode has to be a reject. A "missing means skip" branch in any
// of them is a complete CSRF bypass, because the attacker's form is exactly
// the request that carries no token.
func TestCheckCSRFAcceptsOnlyAMatchingPair(t *testing.T) {
	cases := []struct {
		name          string
		cookie, field string
		want          bool
	}{
		{"matching pair", "abc123", "abc123", true},
		{"no cookie, no field — the attacker's form", "", "", false},
		{"cookie but no field", "abc123", "", false},
		{"field but no cookie", "", "abc123", false},
		{"mismatched pair", "abc123", "abc124", false},
		{"empty cookie with an empty field", "", "", false},
		{"field is a prefix of the cookie", "abc123", "abc", false},
		{"cookie is a prefix of the field", "abc", "abc123", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := CheckCSRF(postWith(c.cookie, c.field), "onbod_csrf"); got != c.want {
				t.Errorf("CheckCSRF = %v, want %v", got, c.want)
			}
		})
	}
}

func TestCheckCSRFIgnoresAnotherDaemonsCookie(t *testing.T) {
	r := postWith("abc123", "abc123")
	if CheckCSRF(r, "webd_csrf") {
		t.Error("a token minted under onbod_csrf must not satisfy webd_csrf")
	}
}

// The whole loop: render the form, echo the token back with the cookie.
func TestCSRFRoundTrip(t *testing.T) {
	w := httptest.NewRecorder()
	tok := EnsureCSRF(w, httptest.NewRequest("GET", "https://host/form", nil), "onbod_csrf", true)

	submit := postWith(tok, tok)
	if !CheckCSRF(submit, "onbod_csrf") {
		t.Fatal("the token EnsureCSRF minted did not pass CheckCSRF; the form can never be submitted")
	}
}

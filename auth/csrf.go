package auth

// Double-submit CSRF for cookie-authenticated HTML forms. The browser sends the
// auth credential automatically on a cross-site POST, so a state-changing form
// must additionally echo a value only a same-origin page could have read.
//
// One implementation for every daemon that renders such a form (onbod's
// admission dashboard, webd's pairing confirm) — a second one would drift.

import (
	"crypto/subtle"
	"net/http"

	"github.com/kronael/arizuko/core"
)

// CSRFField is the form field name the token is echoed in.
const CSRFField = "csrf"

// SecureRequest reports whether r reached us over TLS, directly or through a
// terminating proxy. Cookies set on such a request get Secure.
func SecureRequest(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

// EnsureCSRF returns the double-submit token for this request, minting and
// setting the cookie named `name` when the request carries none. The caller
// MUST embed the returned value in the form as a hidden CSRFField input —
// CheckCSRF compares the two. Not HttpOnly: the page is meant to read it.
func EnsureCSRF(w http.ResponseWriter, r *http.Request, name string, secure bool) string {
	if c, err := r.Cookie(name); err == nil && c.Value != "" {
		return c.Value
	}
	token := core.GenHexToken()
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: token, Path: "/",
		MaxAge: 86400, HttpOnly: false, Secure: secure, SameSite: http.SameSiteStrictMode,
	})
	return token
}

// CheckCSRF reports whether the request's CSRFField form value matches its
// `name` cookie. A missing cookie or a missing field is a failure.
func CheckCSRF(r *http.Request, name string) bool {
	c, err := r.Cookie(name)
	if err != nil || c.Value == "" {
		return false
	}
	got := r.FormValue(CSRFField)
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(c.Value)) == 1
}

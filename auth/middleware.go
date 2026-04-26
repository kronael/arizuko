package auth

import (
	"log/slog"
	"net/http"
)

// identityHeaders are the proxyd-issued identity headers. These get
// stripped together when a forged X-User-Sub is detected.
var identityHeaders = []string{
	"X-User-Sub", "X-User-Name", "X-User-Groups", "X-User-Sig",
}

// RequireSigned wraps a handler to enforce that requests carry valid
// proxyd-signed identity headers. Use on backends where every route
// must be authenticated. Failed verification → strip identity headers
// → 303 redirect to /auth/login.
func RequireSigned(secret string) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !VerifyUserSig(secret, r) {
				slog.Warn("auth: user sig verify failed",
					"path", r.URL.Path,
					"attempted_sub", r.Header.Get("X-User-Sub"),
					"remote", r.Header.Get("X-Forwarded-For"))
				stripIdentityHeaders(r)
				http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
				return
			}
			next(w, r)
		}
	}
}

// StripUnsigned wraps a handler so that any request claiming an
// X-User-Sub but lacking a valid signature has identity headers
// stripped before the handler runs. Use on backends where some
// routes are public (token landings, invites) and some require auth
// — the handler itself decides what to do with empty userSub. This
// is defense-in-depth: a misconfigured network exposing the backend
// directly cannot let a client forge identity headers.
func StripUnsigned(secret string) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-User-Sub") != "" && !VerifyUserSig(secret, r) {
				slog.Warn("auth: user sig verify failed (stripped)",
					"path", r.URL.Path,
					"attempted_sub", r.Header.Get("X-User-Sub"),
					"remote", r.Header.Get("X-Forwarded-For"))
				stripIdentityHeaders(r)
			}
			next(w, r)
		}
	}
}

func stripIdentityHeaders(r *http.Request) {
	for _, h := range identityHeaders {
		r.Header.Del(h)
	}
}

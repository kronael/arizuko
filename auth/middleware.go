package auth

import (
	"log/slog"
	"net/http"
)

var identityHeaders = []string{
	"X-User-Sub", "X-User-Name", "X-User-Groups", "X-User-Sig",
}

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

func StripUnsigned(secret string) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-User-Sub") != "" && !VerifyUserSig(secret, r) {
				slog.Warn("auth: unsigned identity stripped",
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

package main

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/kronael/arizuko/auth"
)

func (s *server) handleForwardAuth(w http.ResponseWriter, r *http.Request) {

	orig := r.Header.Get("X-Forwarded-Uri")
	if orig == "" {
		orig = r.URL.Query().Get("route")
	}

	authed := s.decideForwardAuth(r)
	if authed == nil && s.forwardRouteIsOptional(orig) {

		// An `optional` route admits an anonymous caller and still carries proxyd's
		// transit proof — the route-token surface, where the token IS the credential
		// and a stamped sub only decides attribution (6/22 §"Why `optional` attaches
		// the transit proof on both paths").
		//
		// Without this the hop 303s every anonymous /chat/ and /hook/ visitor to
		// /auth/login, because the decision above knows nothing about the route class
		// while the in-proxyd path switches on it (main.go `case
		// "optional"`). The Caddy renderer sends `optional` through the hop
		// (proxycfg/caddy.go `authenticated`), so the two paths disagreed about the
		// one class that exists to be anonymous (BUGS `L405`).
		authed = s.attachTransit(r)
	}
	if authed == nil {
		slog.Warn("forward auth denied", "reason", "no_valid_credential",
			"route", orig, "remote", clientIP(r))
		if orig != "" && orig != "/" && strings.HasPrefix(orig, "/") &&
			!strings.HasPrefix(orig, "/auth/") {
			http.SetCookie(w, &http.Cookie{
				Name: "auth_return", Value: orig, Path: "/",
				MaxAge: 600, HttpOnly: true, Secure: auth.SecureRequest(r),
				SameSite: http.SameSiteLaxMode,
			})
		}
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
		return
	}
	for _, h := range auth.TransitHeaders {
		if v := authed.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.WriteHeader(http.StatusOK)
}

// decideForwardAuth is requireAuth's decision without its response: the
// identified request on success, nil on refusal. Both callers run the same two
// attempts in the same order, so the endpoint cannot answer differently from
// the in-process gate.
func (s *server) decideForwardAuth(r *http.Request) *http.Request {
	if s.cfg.authSecret == "" {
		return nil
	}
	if a := s.tryAuth(r); a != nil {
		return a
	}

	return nil
}

func (s *server) forwardRouteIsOptional(uri string) bool {
	if uri == "" || !strings.HasPrefix(uri, "/") {
		return false
	}
	if i := strings.IndexAny(uri, "?#"); i >= 0 {
		uri = uri[:i]
	}
	routes, err := s.routes()
	if err != nil {
		return false
	}
	rt := MatchRoute(routes, uri)
	return rt != nil && rt.Auth == "optional"
}

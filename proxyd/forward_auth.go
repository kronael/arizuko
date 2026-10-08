package main

// forward_auth.go — spec 6/22 step 3. The endpoint that lets an off-the-shelf
// proxy front arizuko without arizuko losing the one thing it cannot delegate.
//
// A reverse proxy can route. It cannot mint a `service:proxyd` ES256 bearer,
// and backends trust `X-User-*` only when that proof holds (SECURITY.md
// §"Identity header trust"). So the split is: the proxy asks, arizuko answers.
//
// Two constraints the spec fixes, and both are load-bearing:
//
//   - The proof rides the response, never the proxy's config. It is the same
//     `service:proxyd` bearer proxyd attaches to a proxied request: exchanged
//     from AUTHD_SERVICE_KEY and re-exchanged a minute before its 15-minute
//     expiry (auth/service.go), so a proxy host that reads one response can
//     replay it until that bearer expires. A token in the proxy's config would
//     instead last as long as the file, so the response is where it belongs.
//   - The decision is never cached. One subrequest per request, always; a
//     cached authz answer is a revocation that does not take effect.

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/kronael/arizuko/auth"
)

// handleForwardAuth answers a proxy's auth subrequest: 200 with the identity
// headers and the transit proof, or the same 303-to-login a direct request
// would have received.
//
// It reuses requireAuth's decision rather than re-deriving it. A second
// authentication path beside the one proxyd already applies is exactly the
// drift this spec exists to remove — two gates that agree today and diverge on
// the next change.
func (s *server) handleForwardAuth(w http.ResponseWriter, r *http.Request) {
	// The proxy sends its own URI; the caller's is in X-Forwarded-Uri. Judge
	// the request the USER made, so the login return lands where they asked
	// and the audit line names the route rather than /auth/forward.
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
		// while the in-proxyd path has always switched on it (main.go `case
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
	// A refresh needs a ResponseWriter to re-set the cookie. On the forward-auth
	// path there is no upstream response to attach it to, so a caller whose
	// access token expired is sent through the login flow instead — correct, and
	// one round trip more than the in-process gate costs.
	return nil
}

// forwardRouteIsOptional answers whether the route the CALLER asked for is an
// `optional` one. It reads the same table and the same longest-prefix matcher the
// forwarding path uses, so a class cannot mean one thing to the hop and another to
// the proxy.
//
// An unmatched URI is not optional: the fallback is the stricter answer, so a path
// proxyd does not serve cannot talk its way past the gate.
func (s *server) forwardRouteIsOptional(uri string) bool {
	if uri == "" || !strings.HasPrefix(uri, "/") {
		return false
	}
	if i := strings.IndexAny(uri, "?#"); i >= 0 {
		uri = uri[:i]
	}
	rt := MatchRoute(s.routes(), uri)
	return rt != nil && rt.Auth == "optional"
}

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
//   - The transit proof is minted PER REQUEST, in this response. Handing the
//     proxy a long-lived token to keep in its config would be a downgrade — a
//     file on the proxy host would then be enough to impersonate proxyd.
//   - The decision is never cached. One subrequest per request, always; a
//     cached authz answer is a revocation that does not take effect.

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/kronael/arizuko/auth"
)

// forwardAuthHeaders are what the proxy copies onto the upstream request on a
// 200. Authorization carries the transit proof, so it is not optional: without
// it every backend reads the identity headers as untrusted and refuses.
var forwardAuthHeaders = []string{"Authorization", "X-User-Sub", "X-User-Name", "X-User-Groups"}

// handleForwardAuth answers a proxy's auth subrequest: 200 with the identity
// headers and a fresh transit proof, or the same 303-to-login a direct request
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
	for _, h := range forwardAuthHeaders {
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

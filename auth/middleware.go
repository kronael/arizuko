package auth

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kronael/arizuko/audit"
)

var identityHeaders = []string{
	"X-User-Sub", "X-User-Name", "X-User-Groups", "X-User-Sig",
}

// Grants resolves a verified bare sub to its full allow-scope set (the
// store.UserScopes contract). The post-flip ES256-direct path injects this as
// X-User-Groups; nil falls back to the token's narrow arz/folder claim.
type Grants func(bareSub string) []string

// stampES256Identity rewrites r's identity headers from a verified ES256
// Subject so handlers that read X-User-Sub / -Name / -Groups work unchanged
// after the HMAC→ES256 cutover. The BARE canonical sub (the JWT `user:` prefix
// stripped — spec 5/1 § "sub prefix rule") goes to X-User-Sub so onbod gate
// matching (github:/google:) and DB-keyed grant lookups agree. X-User-Groups
// is the user's FULL grant set from grants(bareSub) — an operator's `**` and a
// multi-folder user's whole set survive, instead of collapsing to the token's
// single arz/folder claim. With grants nil (generic / standalone soak), the
// arz/folder claim is the lone fallback entry. X-User-Sig is NOT stamped — the
// bearer already authenticated; downstream backends re-verify the bearer, not
// the HMAC sig.
func stampES256Identity(r *http.Request, sub Subject, grants Grants) {
	bare := strings.TrimPrefix(sub.Sub, "user:")
	r.Header.Set("X-User-Sub", bare)
	if name := sub.Extra["name"]; name != "" {
		r.Header.Set("X-User-Name", name)
	} else {
		r.Header.Del("X-User-Name")
	}
	var groups []string
	if grants != nil {
		groups = grants(bare)
	} else if f := sub.Extra["arz/folder"]; f != "" {
		groups = []string{f}
	}
	if groups == nil {
		groups = []string{}
	}
	if b, err := json.Marshal(groups); err == nil {
		r.Header.Set("X-User-Groups", string(b))
	}
}

// tryES256 verifies an ES256 bearer against ks (nil = no ES256 path) and, on
// success, stamps r's identity headers. Returns true when the request now
// carries a verified ES256 identity. Env-gated: callers pass a nil ks when
// AUTHD_URL is unset, so the live HMAC-only behavior is exactly preserved.
func tryES256(ks *KeySet, grants Grants, r *http.Request) bool {
	if ks == nil {
		return false
	}
	sub, err := VerifyHTTP(r, ks)
	if err != nil {
		return false
	}
	stampES256Identity(r, sub, grants)
	return true
}

// RequireSignedOrBearer is RequireSigned plus an additive ES256 bearer path
// (spec 5/1 § cutover). It accepts EITHER a valid HMAC X-User-Sig OR, when ks
// is non-nil, a valid authd-minted ES256 bearer. A nil ks reduces this to
// RequireSigned exactly — the live HMAC-only path is unchanged when AUTHD_URL
// is unset. The HMAC path is tried first so existing callers never change
// behavior; the bearer path is the soak-time addition. grants resolves the
// bearer's full grant set (X-User-Groups) post-flip; nil = arz/folder fallback.
func RequireSignedOrBearer(secret string, ks *KeySet, grants Grants) func(http.HandlerFunc) http.HandlerFunc {
	hmacGuard := RequireSigned(secret)
	return func(next http.HandlerFunc) http.HandlerFunc {
		bearerNext := func(w http.ResponseWriter, r *http.Request) {
			if tryES256(ks, grants, r) {
				next(w, r)
				return
			}
			hmacGuard(next)(w, r)
		}
		return func(w http.ResponseWriter, r *http.Request) {
			if VerifyUserSig(secret, r) {
				next(w, r)
				return
			}
			bearerNext(w, r)
		}
	}
}

// StripUnsignedOrBearer is StripUnsigned plus an additive ES256 bearer path
// (spec 5/1 § cutover). An unsigned X-User-Sub is still stripped; a valid HMAC
// sig still passes; additionally, when ks is non-nil, a valid ES256 bearer
// stamps a verified identity. A nil ks reduces this to StripUnsigned exactly.
// grants resolves the bearer's full grant set; nil = arz/folder fallback.
func StripUnsignedOrBearer(secret string, ks *KeySet, grants Grants) func(http.HandlerFunc) http.HandlerFunc {
	hmacGuard := StripUnsigned(secret)
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			// HMAC-signed identity wins (live behavior).
			if VerifyUserSig(secret, r) {
				next(w, r)
				return
			}
			// Else try the ES256 bearer; on success it stamps identity headers.
			if tryES256(ks, grants, r) {
				next(w, r)
				return
			}
			// Neither: fall through to StripUnsigned, which strips any
			// unsigned X-User-Sub and continues (public flow).
			hmacGuard(next)(w, r)
		}
	}
}

func RequireSigned(secret string) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !VerifyUserSig(secret, r) {
				attempted := r.Header.Get("X-User-Sub")
				slog.Warn("auth: user sig verify failed",
					"path", r.URL.Path,
					"attempted_sub", attempted,
					"remote", r.Header.Get("X-Forwarded-For"))
				audit.Emit(context.Background(), audit.Event{
					Category: audit.CategoryAuthZ,
					Action:   "authz.deny",
					Actor:    "anon",
					ActorSub: attempted,
					Surface:  audit.SurfaceREST,
					Resource: r.URL.Path,
					Outcome:  audit.OutcomeDenied,
					ErrorMsg: "sig_verify_failed",
					SourceIP: r.Header.Get("X-Forwarded-For"),
				})
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
				attempted := r.Header.Get("X-User-Sub")
				slog.Warn("auth: unsigned identity stripped",
					"path", r.URL.Path,
					"attempted_sub", attempted,
					"remote", r.Header.Get("X-Forwarded-For"))
				audit.Emit(context.Background(), audit.Event{
					Category: audit.CategoryAuthZ,
					Action:   "authz.deny",
					Actor:    "anon",
					ActorSub: attempted,
					Surface:  audit.SurfaceREST,
					Resource: r.URL.Path,
					Outcome:  audit.OutcomeDenied,
					ErrorMsg: "unsigned_identity",
					SourceIP: r.Header.Get("X-Forwarded-For"),
				})
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

package auth

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/kronael/arizuko/audit"
)

var identityHeaders = []string{
	"X-User-Sub", "X-User-Name", "X-User-Groups", "X-User-Sig",
}

// stampES256Identity rewrites r's identity headers from a verified ES256
// Subject so HMAC-era handlers (which read X-User-Sub / -Name / -Groups) work
// unchanged during the cutover soak. The bare canonical sub goes to
// X-User-Sub; the arz/folder claim becomes the sole X-User-Groups grant entry
// (MatchGroups treats it as a folder prefix). X-User-Sig is NOT stamped — the
// bearer already authenticated; leaving it unset is harmless to the
// dual-accept guards (which fall through to the bearer path) and avoids
// re-signing without the HMAC secret in scope here.
func stampES256Identity(r *http.Request, sub Subject) {
	r.Header.Set("X-User-Sub", sub.Sub)
	if name := sub.Extra["name"]; name != "" {
		r.Header.Set("X-User-Name", name)
	} else {
		r.Header.Del("X-User-Name")
	}
	groups := []string{}
	if f := sub.Extra["arz/folder"]; f != "" {
		groups = append(groups, f)
	}
	if b, err := json.Marshal(groups); err == nil {
		r.Header.Set("X-User-Groups", string(b))
	}
}

// tryES256 verifies an ES256 bearer against ks (nil = no ES256 path) and, on
// success, stamps r's identity headers. Returns true when the request now
// carries a verified ES256 identity. Env-gated: callers pass a nil ks when
// AUTHD_URL is unset, so the live HMAC-only behavior is exactly preserved.
func tryES256(ks *KeySet, r *http.Request) bool {
	if ks == nil {
		return false
	}
	sub, err := VerifyHTTP(r, ks)
	if err != nil {
		return false
	}
	stampES256Identity(r, sub)
	return true
}

// RequireSignedOrBearer is RequireSigned plus an additive ES256 bearer path
// (spec 5/1 § cutover). It accepts EITHER a valid HMAC X-User-Sig OR, when ks
// is non-nil, a valid authd-minted ES256 bearer. A nil ks reduces this to
// RequireSigned exactly — the live HMAC-only path is unchanged when AUTHD_URL
// is unset. The HMAC path is tried first so existing callers never change
// behavior; the bearer path is the soak-time addition.
func RequireSignedOrBearer(secret string, ks *KeySet) func(http.HandlerFunc) http.HandlerFunc {
	hmacGuard := RequireSigned(secret)
	return func(next http.HandlerFunc) http.HandlerFunc {
		bearerNext := func(w http.ResponseWriter, r *http.Request) {
			if tryES256(ks, r) {
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
func StripUnsignedOrBearer(secret string, ks *KeySet) func(http.HandlerFunc) http.HandlerFunc {
	hmacGuard := StripUnsigned(secret)
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			// HMAC-signed identity wins (live behavior).
			if VerifyUserSig(secret, r) {
				next(w, r)
				return
			}
			// Else try the ES256 bearer; on success it stamps identity headers.
			if tryES256(ks, r) {
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

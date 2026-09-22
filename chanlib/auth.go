package chanlib

// Adapter-side auth gate for the routd→adapter path: routd presents its
// service:routd ES256 JWT, Auth verifies it offline against authd's JWKS and
// pins the caller to service:routd — the only principal that drives an
// adapter. With AUTHD_URL unset (local dev) the gate is open.

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"sync"

	"github.com/kronael/arizuko/auth"
)

// CallerRoutd is the service principal the adapter gate admits — the sole
// driver of an adapter's egress.
const CallerRoutd = "service:routd"

// AuthdKeys lazily builds this process's JWKS-backed verifier. One keyset
// answers BOTH questions: is this caller routd's service token (Auth), and did
// proxyd stamp these X-User-* headers (auth.RequireOperator). nil → AUTHD_URL
// unset → every gate that reads it falls open.
var (
	ksOnce sync.Once
	ksVal  *auth.KeySet
)

func AuthdKeys() *auth.KeySet {
	ksOnce.Do(func() {
		authdURL := os.Getenv("AUTHD_URL")
		if authdURL == "" {
			return // local dev: ksVal stays nil → gate open
		}
		// AUTHD_URL set ⇒ ES256 is mandatory; a build failure fails CLOSED.
		// FetchKeys errors only on a malformed URL, so this is a config fault.
		ks, err := auth.FetchKeys(context.Background(), authdURL)
		if err != nil {
			slog.Error("adapter auth: fetch authd keys (AUTHD_URL set ⇒ ES256 required)", "authd", authdURL, "err", err)
			os.Exit(1)
		}
		ksVal = ks
		slog.Info("adapter auth: ES256 service-token verify enabled", "authd", authdURL, "caller", CallerRoutd)
	})
	return ksVal
}

// Auth gates a handler on the routd→adapter call: a valid service:routd ES256
// token, or open with no verifier (local dev).
func Auth(next http.HandlerFunc) http.HandlerFunc {
	return authGate(AuthdKeys(), next)
}

// authGate is Auth with an explicit keyset, injected for tests.
func authGate(ks *auth.KeySet, next http.HandlerFunc) http.HandlerFunc {
	if ks == nil {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		sub, err := auth.VerifyHTTP(r, ks)
		if err != nil || sub.Typ != "service" || sub.Sub != CallerRoutd {
			WriteErr(w, 401, "invalid service token")
			return
		}
		next(w, r)
	}
}

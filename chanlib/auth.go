package chanlib

// Adapter-side auth gate for the routd→adapter path (HMAC→ES256 retire).
//
// routd calls the adapter's /send, /send-file, /send-voice, /files, /v1/pane,
// /v1/history, … presenting its service:routd ES256 JWT (auth.ServiceToken).
// Auth verifies that token offline against authd's JWKS and pins the caller to
// service:routd — routd is the only principal that drives an adapter. When
// AUTHD_URL is unset (local dev) there is no JWKS to verify against, so the gate
// is open — no shared secret remains.

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"sync"

	"github.com/kronael/arizuko/auth"
)

// CallerRoutd is the service principal routd exchanges its AUTHD_SERVICE_KEY
// for (compose sets AUTHD_SERVICE_NAME=routd → service:routd). The adapter gate
// admits only this caller — routd is the sole driver of an adapter's egress.
const CallerRoutd = "service:routd"

// adapterKeySet lazily builds (once) the JWKS-backed verifier the adapter uses
// to check routd's service token. nil keyset → AUTHD_URL unset → local dev (gate
// open). Built lazily so adapter daemons need no wiring change: the first
// Auth-gated request constructs it from the process env.
var (
	ksOnce sync.Once
	ksVal  *auth.KeySet
)

func adapterKeySet() *auth.KeySet {
	ksOnce.Do(func() {
		authdURL := os.Getenv("AUTHD_URL")
		if authdURL == "" {
			return // local dev: ksVal stays nil → gate open
		}
		// AUTHD_URL set ⇒ ES256 is mandatory; a build failure must fail CLOSED.
		// FetchKeys is lazy (no network here) and only errors on a malformed URL,
		// so this is a config fault — exit so it surfaces at boot, like chanlib.Run
		// does for the service-token source.
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

// Auth gates a handler on the routd→adapter call. With a JWKS verifier wired
// (AUTHD_URL set) it admits only a valid service:routd ES256 token; with no
// verifier (local dev) the handler is open — no shared secret remains.
func Auth(next http.HandlerFunc) http.HandlerFunc {
	return authGate(adapterKeySet(), next)
}

// authGate is Auth with an explicit keyset (injected for tests; production
// passes the lazy env-built adapterKeySet). ks!=nil → ES256 service:routd pin;
// ks==nil → open (local dev).
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

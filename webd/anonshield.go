package main

import (
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// The anonymous DoS shield for the route-token surface (`/chat/`, `/hook/`).
// It lived in proxyd's dispatchRouteToken until specs/6/22 step 1 moved the
// surface here; the algorithm is unchanged.

// clientIP is the caller's address as proxyd resolved it. proxyd rewrites
// X-Forwarded-For to exactly ONE value — the trusted edge's client, or the
// connection peer when the edge is untrusted (proxyd.fixForwardedFor) — so the
// left-most token is authoritative here and r.RemoteAddr is not: behind proxyd
// every request shares proxyd's own address, which would put the whole world in
// one bucket. That is the bug BUGS L40 fixed in proxyd; keying on RemoteAddr
// after the move would reintroduce it.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if left := strings.TrimSpace(strings.SplitN(xff, ",", 2)[0]); left != "" {
			return left
		}
	}
	peer, _, _ := net.SplitHostPort(r.RemoteAddr)
	return peer
}

// sweepEvery bounds the full stale-bucket sweep to once per N allow() calls.
// Sweeping every call made allow() O(all-keys): a distinct-IP flood turned
// each anon request into a walk of every live bucket. Amortizing to 1/N keeps
// the hot path O(1) while still reclaiming dead keys often enough to bound the
// map under sustained flood.
const sweepEvery = 256

// anonShield is a sliding-window counter over an UNBOUNDED key space (client
// IPs), which is why it sweeps and the per-JID token bucket in ratelimit.go
// does not: JIDs are bounded by the group table, addresses are not.
type anonShield struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	buckets map[string][]time.Time
	calls   int
}

func newAnonShield(limit int, window time.Duration) *anonShield {
	return &anonShield{limit: limit, window: window, buckets: make(map[string][]time.Time)}
}

// allow consumes one hit for key, returning false when the window is full.
// A non-positive limit disables the shield, matching the per-token ceiling in
// ratelimit.go — two rate limits in one package must not read 0 two ways. In
// proxyd a 0 denied every anonymous request, so a mistyped CHAT_ANON_DOS_RPM
// took the whole public chat surface down.
func (rl *anonShield) allow(key string) bool {
	if rl.limit <= 0 {
		return true
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-rl.window)

	// Reclaim stale buckets once per sweepEvery calls, not every call.
	rl.calls++
	if rl.calls >= sweepEvery {
		rl.calls = 0
		for k, hits := range rl.buckets {
			if len(hits) == 0 || hits[len(hits)-1].Before(cutoff) {
				delete(rl.buckets, k)
			}
		}
	}

	hits := rl.buckets[key]
	n := 0
	for _, t := range hits {
		if t.After(cutoff) {
			hits[n] = t
			n++
		}
	}
	hits = hits[:n]
	if len(hits) >= rl.limit {
		rl.buckets[key] = hits
		return false
	}
	rl.buckets[key] = append(hits, now)
	return true
}

// shieldAnon throttles unauthenticated callers of the route-token surface by
// client IP. An identified caller skips it entirely — proxyd's `optional` gate
// only stamps X-User-* on a verified bearer, so identified() is true for
// exactly the callers proxyd's tryAuth used to admit past the shield.
//
// This is a DoS shield, not metering: spend is governed by cost caps
// (spec 6/39-gateway-owns-spend). The per-token ceiling in ratelimit.go is the orthogonal
// it bounds a single chat's traffic, this bounds a single address's.
func (s *server) shieldAnon(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.identified(r) && !s.anonDOS.allow(clientIP(r)) {
			slog.Warn("anon route-token shield", "path", r.URL.Path, "ip", clientIP(r))
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

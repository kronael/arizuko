package main

import (
	"strings"
	"sync"
	"time"
)

// Per-token in-memory rate limiter (spec 5/W § "Rate limits, body limits").
// Refill-based token bucket keyed by the resolved JID. Capacity equals the
// per-minute ceiling; tokens refill continuously at rate/minute so a steady
// caller at or below the ceiling never trips. Ceiling is chosen by JID prefix:
// hook: (machine ingest) gets a higher ceiling than web: (human widget).

type bucket struct {
	tokens float64
	last   time.Time
}

type rateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	hookCap float64 // tokens per minute for hook: JIDs
	webCap  float64 // tokens per minute for web: JIDs
}

func newRateLimiter(hookPerMin, webPerMin int) *rateLimiter {
	return &rateLimiter{
		buckets: map[string]*bucket{},
		hookCap: float64(hookPerMin),
		webCap:  float64(webPerMin),
	}
}

// ceiling returns the per-minute capacity for a JID by its prefix.
func (rl *rateLimiter) ceiling(jid string) float64 {
	if strings.HasPrefix(jid, "hook:") {
		return rl.hookCap
	}
	return rl.webCap
}

// allow consumes one token for jid, returning false when the bucket is empty.
// Concurrency-safe. A non-positive ceiling disables limiting (always allows).
func (rl *rateLimiter) allow(jid string) bool {
	cap := rl.ceiling(jid)
	if cap <= 0 {
		return true
	}
	refill := cap / 60.0 // tokens per second

	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	b := rl.buckets[jid]
	if b == nil {
		b = &bucket{tokens: cap, last: now}
		rl.buckets[jid] = b
	} else {
		b.tokens += now.Sub(b.last).Seconds() * refill
		if b.tokens > cap {
			b.tokens = cap
		}
		b.last = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

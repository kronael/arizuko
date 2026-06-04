package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Unit: N requests under the ceiling pass; the next is limited. A freshly
// created bucket starts full at capacity.
func TestRateLimitBucket(t *testing.T) {
	rl := newRateLimiter(60, 20)

	// web: ceiling is 20 — first 20 pass, 21st trips.
	jid := "web:atlas"
	for i := 0; i < 20; i++ {
		if !rl.allow(jid) {
			t.Fatalf("web req %d denied, want allowed", i+1)
		}
	}
	if rl.allow(jid) {
		t.Fatalf("web req 21 allowed, want denied")
	}

	// A different JID has its own independent bucket.
	if !rl.allow("web:other") {
		t.Fatalf("independent JID denied")
	}
}

// Unit: ceiling is chosen by JID prefix — hook: strictly higher than web:.
func TestRateLimitCeilingByPrefix(t *testing.T) {
	rl := newRateLimiter(60, 20)
	hook := rl.ceiling("hook:atlas/telegram")
	web := rl.ceiling("web:atlas")
	if hook <= web {
		t.Fatalf("hook ceiling %v not > web ceiling %v", hook, web)
	}
	if hook != 60 || web != 20 {
		t.Fatalf("ceilings hook=%v web=%v, want 60/20", hook, web)
	}
	// Non-web/hook prefixes fall back to the web ceiling.
	if rl.ceiling("other:x") != web {
		t.Fatalf("unknown prefix ceiling = %v, want web %v", rl.ceiling("other:x"), web)
	}
}

// Unit: a non-positive ceiling disables limiting (always allows).
func TestRateLimitDisabled(t *testing.T) {
	rl := newRateLimiter(0, 0)
	for i := 0; i < 1000; i++ {
		if !rl.allow("web:atlas") {
			t.Fatalf("disabled limiter denied at req %d", i+1)
		}
	}
}

// Handler-level: a flood of POSTs to one chat token eventually returns 429,
// and the over-limit response carries the terse rate-limit body.
func TestRateLimitHandlerFlood(t *testing.T) {
	srv, _, st := newTestServer(t)
	srv.limiter = newRateLimiter(60, 5) // tight web: ceiling for the test
	seedGroup(t, st, "atlas", "Atlas")
	tok := seedChatToken(t, st, "atlas")

	var got429 bool
	var bodyOf429 string
	for i := 0; i < 12; i++ {
		r := httptest.NewRequest("POST", "/chat/"+tok, strings.NewReader("content=hi&topic=t1"))
		r.SetPathValue("token", tok)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		srv.handleChatTokenPost(w, r)
		if w.Code == http.StatusTooManyRequests {
			got429 = true
			bodyOf429 = w.Body.String()
			break
		}
	}
	if !got429 {
		t.Fatalf("flood of 12 posts never returned 429 (ceiling 5/min)")
	}
	if !strings.Contains(bodyOf429, "rate limit exceeded") {
		t.Errorf("429 body = %q, want rate-limit message", bodyOf429)
	}
}

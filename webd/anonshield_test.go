package main

import (
	"crypto/ecdsa"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/objstore/dynstore/dyntest"
	"github.com/kronael/arizuko/tests/testutils"
)

func TestMain(m *testing.M) { dyntest.Main(m) }

// The shield and these tests moved here from proxyd with the route-token
// surface (specs/6/22 step 1). The properties they pin are unchanged.

func TestAnonShieldBoundary(t *testing.T) {
	rl := newAnonShield(2, time.Minute)
	if !rl.allow("1.2.3.4") {
		t.Error("first request should be allowed")
	}
	if !rl.allow("1.2.3.4") {
		t.Error("second request should be allowed")
	}
	if rl.allow("1.2.3.4") {
		t.Error("third request should be denied")
	}
}

func TestAnonShieldDifferentKeys(t *testing.T) {
	rl := newAnonShield(1, time.Minute)
	if !rl.allow("a") {
		t.Error("a should be allowed")
	}
	if !rl.allow("b") {
		t.Error("b should be allowed (different key)")
	}
	if rl.allow("a") {
		t.Error("a second request should be denied")
	}
}

// TestAnonShieldSweepReclaims drives a distinct-IP flood past the amortized
// sweep boundary and asserts stale buckets are reclaimed (map stays bounded).
func TestAnonShieldSweepReclaims(t *testing.T) {
	rl := newAnonShield(1, time.Nanosecond) // window so short every hit is instantly stale
	for i := range sweepEvery + 10 {
		rl.allow(fmt.Sprintf("ip-%d", i))
	}
	// The sweep fires at call sweepEvery; afterwards only buckets added since
	// remain, so the map is far smaller than the number of distinct keys seen.
	rl.mu.Lock()
	n := len(rl.buckets)
	rl.mu.Unlock()
	if n > 16 {
		t.Errorf("buckets = %d after flood, sweep did not reclaim (want small)", n)
	}
}

// shieldServer builds a webd whose anon shield admits `limit` requests per
// client per minute, with one chat token seeded.
func shieldServer(t *testing.T, limit int, ks *auth.KeySet) (*httptest.Server, string) {
	t.Helper()
	st := testutils.NewRoutdStore(t)
	seedGroup(t, st, "acme", "Acme")
	tok := seedChatToken(t, st, "acme")

	rc := chanlib.NewRouterClient("http://127.0.0.1:1")
	rc.SetToken("test-token")
	s := newServer(config{assistantName: "assistant"}, st, newHub(), rc, ks, nil)
	s.anonDOS = newAnonShield(limit, time.Minute)
	srv := httptest.NewServer(s.handler())
	t.Cleanup(srv.Close)
	return srv, tok
}

// Exactly N requests per client in the window are admitted; the N+1th is 429.
func TestAnonShield_ChatSurfaceBoundary(t *testing.T) {
	srv, tok := shieldServer(t, 2, nil)
	get := func() int {
		req, _ := http.NewRequest("GET", srv.URL+"/chat/"+tok+"/config", nil)
		req.Header.Set("X-Forwarded-For", "7.7.7.7")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := get(); c != 200 {
		t.Fatalf("first = %d, want 200", c)
	}
	if c := get(); c != 200 {
		t.Fatalf("second = %d, want 200", c)
	}
	if c := get(); c != http.StatusTooManyRequests {
		t.Errorf("third = %d, want 429", c)
	}
}

// webd's peer is ALWAYS proxyd, so a shield keyed on the connection address
// would put every anonymous visitor in one bucket: the first reader exhausts it
// and the second is refused. That was BUGS L40 in proxyd, where the peer was
// the shared nginx hop; behind proxyd the collapse is total. Key on the
// X-Forwarded-For client proxyd resolved instead.
func TestAnonShield_PerClientNotPerPeer(t *testing.T) {
	srv, tok := shieldServer(t, 1, nil)
	visit := func(clientIP string) int {
		// Same TCP peer for both — that is what webd always sees.
		req, _ := http.NewRequest("GET", srv.URL+"/chat/"+tok+"/config", nil)
		req.Header.Set("X-Forwarded-For", clientIP)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := visit("203.0.113.7"); c == http.StatusTooManyRequests {
		t.Fatalf("first visitor refused with an empty bucket: %d", c)
	}
	if c := visit("203.0.113.8"); c == http.StatusTooManyRequests {
		t.Error("second visitor refused: shield keyed on the peer, not the client")
	}
}

// An identified caller skips the shield entirely, exactly as proxyd's
// dispatchRouteToken skipped it when tryAuth returned a request.
func TestAnonShield_IdentifiedCallerSkips(t *testing.T) {
	k, err := auth.NewSigningKey("k1")
	if err != nil {
		t.Fatal(err)
	}
	transit, err := k.Sign(auth.TokenClaims{Sub: "service:proxyd", Typ: "service"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ks := auth.NewKeySet(map[string]*ecdsa.PublicKey{"k1": &k.Priv.PublicKey})

	srv, tok := shieldServer(t, 1, ks)
	get := func() int {
		req, _ := http.NewRequest("GET", srv.URL+"/chat/"+tok+"/config", nil)
		req.Header.Set("X-Forwarded-For", "7.7.7.7")
		// What proxyd's `optional` gate stamps for an authenticated caller.
		req.Header.Set("X-User-Sub", "user:google:123")
		req.Header.Set("Authorization", "Bearer "+transit)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	for i := range 4 {
		if c := get(); c != 200 {
			t.Fatalf("identified request %d = %d, want 200 (shield must not apply)", i, c)
		}
	}
}

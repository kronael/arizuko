package auth

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// fakeAuthd is a stub /v1/service-token endpoint matching the spec 5/1 §435
// contract: secret in the Authorization header, daemon in the body. It signs a
// real ES256 token so the helper can read the exp claim for refresh scheduling.
func fakeAuthd(t *testing.T, key *SigningKey, ttl time.Duration, calls *int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(calls, 1)
		if r.Header.Get("Authorization") != "Bearer good-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body struct {
			Daemon string `json:"daemon"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Daemon != "timed" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		tok, _ := key.Sign(TokenClaims{Sub: "service:timed", Typ: "service", Scope: []string{"tasks:read"}}, ttl)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      tok,
			"expires_at": time.Now().Add(ttl).UTC().Format(time.RFC3339),
		})
	}))
}

// The helper exchanges the bootstrap secret for a service JWT and that token
// verifies as service:timed.
func TestServiceTokenExchangeVerifies(t *testing.T) {
	key, _ := NewSigningKey("k1")
	var calls int32
	ts := fakeAuthd(t, key, time.Hour, &calls)
	defer ts.Close()

	src, err := ServiceToken(ts.URL, "timed", "good-secret")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	sub, err := VerifyToken(tok, keySetFor(key))
	if err != nil {
		t.Fatalf("service token invalid: %v", err)
	}
	if sub.Sub != "service:timed" || !HasScope(sub.Scope, "tasks", "read") {
		t.Fatalf("bad service subject: %+v", sub)
	}
}

// keySetFor builds a local KeySet trusting a single signing key (test verify).
func keySetFor(k *SigningKey) *KeySet {
	return NewKeySet(map[string]*ecdsa.PublicKey{k.Kid: &k.Priv.PublicKey})
}

// A cached, still-fresh token is reused — no second exchange (no per-request hop).
func TestServiceTokenCachesUntilNearExpiry(t *testing.T) {
	key, _ := NewSigningKey("k1")
	var calls int32
	ts := fakeAuthd(t, key, time.Hour, &calls)
	defer ts.Close()

	src, _ := ServiceToken(ts.URL, "timed", "good-secret")
	for i := 0; i < 5; i++ {
		if _, err := src.Token(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("cached token must not re-exchange: got %d calls, want 1", got)
	}
}

// Within refreshLead of expiry, Token() re-exchanges (auto-refresh before
// expiry, the way RemoteKeySet refreshes). Driven by an injected clock.
func TestServiceTokenRefreshesBeforeExpiry(t *testing.T) {
	key, _ := NewSigningKey("k1")
	var calls int32
	// Mint short-lived tokens so the clock-advance crosses the refresh lead.
	ts := fakeAuthd(t, key, 90*time.Second, &calls)
	defer ts.Close()

	src, _ := ServiceToken(ts.URL, "timed", "good-secret")
	base := time.Now()
	src.now = func() time.Time { return base }
	if _, err := src.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Advance the clock to inside refreshLead (60s) of the ~90s expiry.
	src.now = func() time.Time { return base.Add(40 * time.Second) }
	if _, err := src.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("near-expiry Token() must re-exchange: got %d calls, want 2", got)
	}
}

// A bad bootstrap secret surfaces as an error, not a silent empty token.
func TestServiceTokenBadSecretErrors(t *testing.T) {
	key, _ := NewSigningKey("k1")
	var calls int32
	ts := fakeAuthd(t, key, time.Hour, &calls)
	defer ts.Close()

	src, _ := ServiceToken(ts.URL, "timed", "wrong-secret")
	if _, err := src.Token(context.Background()); err == nil {
		t.Fatal("a bad bootstrap secret must error")
	}
}

func TestServiceTokenRequiresAllArgs(t *testing.T) {
	if _, err := ServiceToken("", "timed", "k"); err == nil {
		t.Fatal("empty authdURL must error")
	}
	if _, err := ServiceToken("http://x", "", "k"); err == nil {
		t.Fatal("empty daemon must error")
	}
	if _, err := ServiceToken("http://x", "timed", ""); err == nil {
		t.Fatal("empty key must error")
	}
}

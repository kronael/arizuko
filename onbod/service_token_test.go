package main

// onbod posts the onboarding greeting to routd's JWT-gated /v1/outbound. In the
// split it presents a service:onbod JWT exchanged from AUTHD_SERVICE_KEY (spec
// 5/1); the monolith path falls back to CHANNEL_SECRET. These tests pin both.

import (
	"crypto/ecdsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
)

// fakeAuthdToken serves /v1/service-token minting a service:onbod JWT carrying
// messages:write — the endpoint auth.ServiceToken exchanges against.
func fakeAuthdToken(t *testing.T) (*httptest.Server, *auth.SigningKey) {
	t.Helper()
	key, err := auth.NewSigningKey("kid-onbod")
	if err != nil {
		t.Fatalf("signing key: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/service-token", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Daemon string `json:"daemon"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		tok, err := key.Sign(auth.TokenClaims{
			Sub: "service:" + req.Daemon, Typ: "service", Scope: []string{"messages:write"},
		}, time.Hour)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"token": tok})
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, key
}

// captureOutbound records the Authorization bearer on /v1/outbound.
func captureOutbound(t *testing.T, gotBearer *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*gotBearer = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestSendReplyUsesServiceToken: with a service-token source wired, sendReply
// presents the service:onbod JWT (not CHANNEL_SECRET), and it verifies against
// the authd JWKS with messages:write — the real /v1/outbound gate.
func TestSendReplyUsesServiceToken(t *testing.T) {
	authd, key := fakeAuthdToken(t)
	var bearer string
	out := captureOutbound(t, &bearer)

	src, err := auth.ServiceToken(authd.URL, "onbod", "boot-onbod")
	if err != nil {
		t.Fatalf("service token source: %v", err)
	}
	cfg := config{gatedURL: out.URL, svcToken: src.Token}
	sendReply(cfg, "telegram:1", "welcome")

	if bearer == "" {
		t.Fatalf("sendReply presented no bearer, want a service JWT")
	}
	ks := auth.NewKeySet(map[string]*ecdsa.PublicKey{key.Kid: &key.Priv.PublicKey})
	sub, err := auth.VerifyToken(bearer, ks)
	if err != nil {
		t.Fatalf("service token must verify: %v", err)
	}
	if sub.Sub != "service:onbod" {
		t.Fatalf("token sub = %q, want service:onbod", sub.Sub)
	}
	if !auth.HasScope(sub.Scope, "messages", "write") {
		t.Fatalf("service:onbod token must carry messages:write, got %v", sub.Scope)
	}
}

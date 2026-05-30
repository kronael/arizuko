package runed

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/types"
)

// Step 5 (spec 5/1 § Service bootstrap): the full boot-exchange flow end to
// end through a stub authd. /v1/service-token mints the service:runed parent
// from AUTHD_SERVICE_KEY (auth.ServiceToken); the broker then presents that
// live, authd-minted token on the /v1/tokens downscope — proving the
// bootstrap, not a static env value, reaches authd. (TokenFn forwarding and
// the static fallback are covered separately in broker_token_test.go.)
func TestHTTPBrokerWithSource_BootstrapExchange(t *testing.T) {
	key, _ := auth.NewSigningKey("k1")

	var gotParent string
	authd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/service-token":
			if r.Header.Get("Authorization") != "Bearer runed-secret" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			tok, _ := key.Sign(auth.TokenClaims{
				Sub: "service:runed", Typ: "service", Scope: []string{"tokens:mint"},
			}, time.Hour)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token":      tok,
				"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			})
		case "/v1/tokens":
			gotParent = r.Header.Get("Authorization")
			child, _ := key.Sign(auth.TokenClaims{
				Sub: "user:bob", Typ: "downscoped", ParentJTI: "p", Scope: []string{"messages:send"},
			}, 10*time.Minute)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token":      child,
				"jti":        "child-jti",
				"expires_at": time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339),
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer authd.Close()

	src, err := auth.ServiceToken(authd.URL, "runed", "runed-secret")
	if err != nil {
		t.Fatal(err)
	}
	broker := NewHTTPBrokerWithSource(authd.URL, src.Token)

	jws, jti, _, err := broker.Broker(context.Background(),
		types.UserSub("bob"), "atlas", []types.Scope{"messages:send"}, 10*time.Minute)
	if err != nil {
		t.Fatalf("broker: %v", err)
	}
	if jws == "" || jti != "child-jti" {
		t.Fatalf("unexpected downscope result: jws=%q jti=%q", jws, jti)
	}
	// The downscope carried the authd-minted service:runed token — the
	// bootstrap exchange wired through, not a static env.
	sub, verr := auth.VerifyHTTP(reqWithAuth(gotParent), keySetFor(key))
	if verr != nil {
		t.Fatalf("parent token invalid: %v", verr)
	}
	if sub.Sub != "service:runed" {
		t.Fatalf("parent sub = %q, want service:runed", sub.Sub)
	}
}

func reqWithAuth(authHeader string) *http.Request {
	r := httptest.NewRequest("POST", "/", nil)
	r.Header.Set("Authorization", authHeader)
	return r
}

func keySetFor(k *auth.SigningKey) *auth.KeySet {
	return auth.NewKeySet(map[string]*ecdsa.PublicKey{k.Kid: &k.Priv.PublicKey})
}

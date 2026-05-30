package runed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kronael/arizuko/types"
)

// The cutover wiring (spec 5/1 § Service bootstrap) lets runed pass a
// refreshing TokenFn (auth.ServiceToken) as the downscope PARENT instead of a
// static service token. Either way the live parent token must reach authd's
// POST /v1/tokens Authorization header. A stub authd records the bearer +
// returns a downscoped token.

func downscopeStub(t *testing.T, gotBearer *string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/tokens", func(w http.ResponseWriter, r *http.Request) {
		*gotBearer = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"token": "child.jws", "jti": "jti-1", "expires_at": "2099-01-01T00:00:00Z",
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestBroker_WithSource_ForwardsLiveParentToken(t *testing.T) {
	var bearer string
	srv := downscopeStub(t, &bearer)
	calls := 0
	tokenFn := func(context.Context) (string, error) {
		calls++
		return "live-service-runed", nil
	}
	b := NewHTTPBrokerWithSource(srv.URL, tokenFn)
	jws, jti, _, err := b.Broker(context.Background(), types.UserSub("google:7"), "atlas/main",
		[]types.Scope{"messages:send"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if bearer != "Bearer live-service-runed" {
		t.Fatalf("parent Authorization = %q; want Bearer live-service-runed", bearer)
	}
	if jws != "child.jws" || jti != "jti-1" {
		t.Fatalf("downscope result = (%q,%q); want (child.jws,jti-1)", jws, jti)
	}
	if calls != 1 {
		t.Fatalf("TokenFn calls = %d; want 1", calls)
	}
}

func TestBroker_Static_ForwardsParentToken(t *testing.T) {
	var bearer string
	srv := downscopeStub(t, &bearer)
	b := NewHTTPBroker(srv.URL, "static-service-runed")
	if _, _, _, err := b.Broker(context.Background(), types.UserSub("google:7"), "atlas/main",
		[]types.Scope{"messages:send"}, time.Minute); err != nil {
		t.Fatal(err)
	}
	if bearer != "Bearer static-service-runed" {
		t.Fatalf("parent Authorization = %q; want Bearer static-service-runed", bearer)
	}
}

package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The cutover wiring (spec 5/1 § Service bootstrap) lets routd pass a
// refreshing TokenFn (auth.ServiceToken) instead of a static bearer. Either
// way the live token must reach the POST /v1/runs Authorization header. A stub
// runed records the bearer it received.

func bearerCapturingRuned(t *testing.T, got *string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/runs", func(w http.ResponseWriter, r *http.Request) {
		*got = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(RunOutcome{RunID: "r1", Outcome: OutcomeOK})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestClient_WithSource_ForwardsLiveToken(t *testing.T) {
	var got string
	srv := bearerCapturingRuned(t, &got)
	calls := 0
	tokenFn := func(context.Context) (string, error) {
		calls++
		return "live-es256-token", nil
	}
	c := NewClientWithSource(srv.URL, tokenFn, 5*time.Second)
	if _, err := c.Run(context.Background(), RunRequest{Folder: "atlas/main", TurnID: "t1"}); err != nil {
		t.Fatal(err)
	}
	if got != "Bearer live-es256-token" {
		t.Fatalf("Authorization = %q; want Bearer live-es256-token", got)
	}
	if calls != 1 {
		t.Fatalf("TokenFn calls = %d; want 1 (consulted per call)", calls)
	}
}

func TestClient_Static_ForwardsToken(t *testing.T) {
	var got string
	srv := bearerCapturingRuned(t, &got)
	c := NewClient(srv.URL, "static-token", 5*time.Second)
	if _, err := c.Run(context.Background(), RunRequest{Folder: "atlas/main", TurnID: "t1"}); err != nil {
		t.Fatal(err)
	}
	if got != "Bearer static-token" {
		t.Fatalf("Authorization = %q; want Bearer static-token", got)
	}
}

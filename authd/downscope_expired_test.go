package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
)

// The token expired one second ago. The handler reads the signing keys from
// the store, so the test runs on the real clock and signs the expiry into the
// past.
func TestExpiredDownscopeHTTP(t *testing.T) {
	a := newTestAuthd(t, testDB(t))
	k, err := a.activeKey()
	if err != nil {
		t.Fatal(err)
	}
	token, err := k.Sign(auth.TokenClaims{Sub: "user:alice", Typ: "user", Scope: []string{"tasks:read"}}, -time.Second)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/v1/tokens", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+token)
	out := httptest.NewRecorder()
	(&server{a: a}).handleTokens(out, req)
	if out.Code != http.StatusUnauthorized {
		t.Errorf("status=%d body=%q", out.Code, out.Body.String())
	}
}

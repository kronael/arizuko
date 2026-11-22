package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/kronael/arizuko/auth"
)

func TestExpiredDownscopeHTTP(t *testing.T) {
	a := newTestAuthd(t, testDB(t))
	k, err := a.activeKey()
	if err != nil {
		t.Fatal(err)
	}
	synctest.Test(t, func(t *testing.T) {
		token, err := k.Sign(auth.TokenClaims{Sub: "user:alice", Typ: "user", Scope: []string{"tasks:read"}}, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Second)
		req := httptest.NewRequest("POST", "/v1/tokens", strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer "+token)
		out := httptest.NewRecorder()
		(&server{a: a}).handleTokens(out, req)
		if out.Code != http.StatusUnauthorized {
			t.Errorf("status=%d body=%q", out.Code, out.Body.String())
		}
	})
}

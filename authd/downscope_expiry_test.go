package main

import (
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/kronael/arizuko/auth"
)

func TestDownscopeExpiredParent(t *testing.T) {
	a := newTestAuthd(t, testDB(t))
	k, err := a.activeKey()
	if err != nil {
		t.Fatal(err)
	}
	ks := keySet(t, a)
	synctest.Test(t, func(t *testing.T) {
		token, err := k.Sign(auth.TokenClaims{
			Sub: "user:alice", Typ: "user", Scope: []string{"tasks:read"},
		}, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Second)
		parent, err := auth.VerifyToken(token, ks)
		if err != nil {
			t.Fatal(err)
		}
		_, err = a.Downscope(parent, nil, "", 0)
		if !errors.Is(err, auth.ErrExpiredToken) {
			t.Fatalf("Downscope error = %v, want expired token", err)
		}
	})
}

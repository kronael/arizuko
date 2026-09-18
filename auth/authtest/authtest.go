// Package authtest builds the two halves a dashboard gate needs, so every test
// of an operator page states the same thing about what makes a stamped header
// trustworthy.
//
// A dashboard reads the operator scope out of `X-User-Groups`, and that scope
// counts for nothing on its own: proxyd strips the caller's copy, stamps its
// own, and attaches a `service:proxyd` bearer as the proof it did. A test that
// carries only the header is testing a forgery.
package authtest

import (
	"crypto/ecdsa"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
)

// Operator returns a KeySet and the proxyd service bearer that verifies against
// it. Set the bearer as `Authorization` beside `X-User-Groups: ["**"]` to reach
// an operator page; leave it off to assert the gate refuses the header alone.
func Operator(t *testing.T) (*auth.KeySet, string) {
	t.Helper()
	k, err := auth.NewSigningKey("k1")
	if err != nil {
		t.Fatalf("sign key: %v", err)
	}
	tok, err := k.Sign(auth.TokenClaims{Sub: auth.ProxydTransitSub, Typ: "service"}, time.Minute)
	if err != nil {
		t.Fatalf("sign the proxyd transit bearer: %v", err)
	}
	return auth.NewKeySet(map[string]*ecdsa.PublicKey{"k1": &k.Priv.PublicKey}), tok
}

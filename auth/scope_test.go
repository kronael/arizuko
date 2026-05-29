package auth

import (
	"crypto/ecdsa"
	"encoding/json"
	"testing"

	jose "github.com/go-jose/go-jose/v4"
)

func keySetFromJWKS(t *testing.T, body []byte) *KeySet {
	t.Helper()
	var set jose.JSONWebKeySet
	if err := json.Unmarshal(body, &set); err != nil {
		t.Fatal(err)
	}
	keys := map[string]*ecdsa.PublicKey{}
	for _, jwk := range set.Keys {
		pub, ok := jwk.Key.(*ecdsa.PublicKey)
		if !ok {
			t.Fatalf("jwk %s is not ECDSA", jwk.KeyID)
		}
		keys[jwk.KeyID] = pub
	}
	return NewKeySet(keys)
}

func TestHasScopeExactAndWildcard(t *testing.T) {
	cases := []struct {
		scope    []string
		res, vrb string
		want     bool
	}{
		{[]string{"tasks:read"}, "tasks", "read", true},
		{[]string{"tasks:read"}, "tasks", "write", false},
		{[]string{"tasks:*"}, "tasks", "write", true},
		{[]string{"tasks:*"}, "messages", "write", false},
		{[]string{"*:*"}, "tasks", "read", false}, // global wildcard is never valid
		{[]string{"*"}, "tasks", "read", false},
		{nil, "tasks", "read", false},
	}
	for _, c := range cases {
		if got := HasScope(c.scope, c.res, c.vrb); got != c.want {
			t.Errorf("HasScope(%v, %q, %q) = %v, want %v", c.scope, c.res, c.vrb, got, c.want)
		}
	}
}

func TestIntersectScopes(t *testing.T) {
	got := intersectScopes(
		[]string{"tasks:write", "tasks:read", "tasks:read", "messages:write"},
		[]string{"tasks:*"})
	// dedup + only tasks:* coverage; messages:write dropped.
	if len(got) != 2 || got[0] != "tasks:write" || got[1] != "tasks:read" {
		t.Fatalf("got %v", got)
	}
}

package main

import (
	"crypto/ecdsa"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
)

// keysetVerifier is what runed and every operator call are checked against.
// A verifier that returned a partial subject alongside an error would hand the
// caller's identity to a handler that only checked err on some paths.
func TestKeysetVerifierReturnsNothingOnAFailedVerify(t *testing.T) {
	k, err := auth.NewSigningKey("k1")
	if err != nil {
		t.Fatal(err)
	}
	ks := auth.NewKeySet(map[string]*ecdsa.PublicKey{k.Kid: &k.Priv.PublicKey})
	v := keysetVerifier{ks: ks}

	tok, err := k.Sign(auth.TokenClaims{
		Sub: "service:runed", Typ: "service", Scope: []string{"runs:write"},
		Extra: map[string]string{"arz/folder": "acme/eng"},
	}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest("GET", "http://routd/v1/tasks", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)

	sub, scope, folder, err := v.Verify(req)
	if err != nil {
		t.Fatalf("a live token failed verification: %v", err)
	}
	if sub != "service:runed" {
		t.Errorf("sub = %q", sub)
	}
	if len(scope) != 1 || scope[0] != "runs:write" {
		t.Errorf("scope = %v", scope)
	}
	// The folder claim is what binds a caller to its tenant. Losing it here
	// makes every folder-scoped read unbounded.
	if folder != "acme/eng" {
		t.Errorf("folder = %q, want the arz/folder claim", folder)
	}

	for _, bad := range []*http.Request{
		mustReq(t, ""),
		mustReq(t, "Bearer not-a-token"),
		mustReq(t, tok), // no Bearer scheme
	} {
		sub, scope, folder, err := v.Verify(bad)
		if err == nil {
			t.Errorf("Verify accepted %q", bad.Header.Get("Authorization"))
		}
		if sub != "" || scope != nil || folder != "" {
			t.Errorf("a failed verify returned (%q, %v, %q); a handler checking only some fields would trust it",
				sub, scope, folder)
		}
	}
}

func mustReq(t *testing.T, authz string) *http.Request {
	t.Helper()
	r, err := http.NewRequest("GET", "http://routd/v1/tasks", nil)
	if err != nil {
		t.Fatal(err)
	}
	if authz != "" {
		r.Header.Set("Authorization", authz)
	}
	return r
}

// The model is the one env reader whose default must be EMPTY. Every other one
// falls back to a working value; this one falling back to a vendor id makes
// "let the harness choose" unexpressible, and switching ARIZUKO_BACKEND then
// sends a claude id to codex — 100% of turns 400d on krons 2026-08-27 (L129).
//
// Asserted against the SOURCE, not the value: the config is a struct literal
// inside main(), so there is nothing to call. Calling core.EnvOr with an empty
// fallback tests core.EnvOr and passes just as happily with the vendor id
// restored at the call site — which is exactly how the first version of this
// test failed to catch its own mutation.
func TestDefaultModelHasNoVendorFallback(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	line := regexp.MustCompile(`DefaultModel:\s*core\.EnvOr\("ARIZUKO_DEFAULT_MODEL",\s*([^)]*)\)`)
	m := line.FindSubmatch(src)
	if m == nil {
		t.Fatal("DefaultModel is no longer read with core.EnvOr — update this guard")
	}
	if got := strings.TrimSpace(string(m[1])); got != `""` {
		t.Errorf("DefaultModel fallback is %s, want \"\": a vendor id here makes "+
			"an unset model unexpressible, so a non-claude backend gets a claude id", got)
	}
}

package main

import (
	"crypto/ecdsa"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
)

// The env readers decide routd's whole runtime shape, and none of them was
// covered: the package sat at 0%. Each one has the same load-bearing rule —
// an unset OR unparseable value must land on the documented default, never on
// the zero value. A poll interval of 0 is a busy loop; a container cap of 0 is
// a daemon that spawns nothing; a channel list that keeps its whitespace never
// matches an adapter name.

func TestEnvOrFallsBackOnUnsetAndEmpty(t *testing.T) {
	const k = "ARIZUKO_ROUTD_TEST_STR"
	t.Setenv(k, "")
	if got := envOr(k, "fallback"); got != "fallback" {
		t.Errorf("empty: %q, want the default", got)
	}
	t.Setenv(k, "set")
	if got := envOr(k, "fallback"); got != "set" {
		t.Errorf("set: %q", got)
	}
}

func TestIntOrRejectsAnythingUnparseable(t *testing.T) {
	const k = "ARIZUKO_ROUTD_TEST_INT"
	cases := map[string]int{
		"":    7, // unset-shaped
		"abc": 7,
		"1.5": 7,
		"3 ":  7, // strconv.Atoi rejects the trailing space
		"12":  12,
		"-4":  -4,
		"0":   0, // an explicit 0 IS the operator's choice and must win
	}
	for in, want := range cases {
		t.Setenv(k, in)
		if got := intOr(k, 7); got != want {
			t.Errorf("intOr(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestDurOrRejectsAnythingUnparseable(t *testing.T) {
	const k = "ARIZUKO_ROUTD_TEST_DUR"
	cases := map[string]time.Duration{
		"":          time.Minute,
		"5":         time.Minute, // no unit
		"5 minutes": time.Minute,
		"forever":   time.Minute,
		"30s":       30 * time.Second,
		"2h":        2 * time.Hour,
		"0s":        0,
	}
	for in, want := range cases {
		t.Setenv(k, in)
		if got := durOr(k, time.Minute); got != want {
			t.Errorf("durOr(%q) = %s, want %s", in, got, want)
		}
	}
}

// SEND_DISABLED_CHANNELS is matched against adapter names. An entry that keeps
// its surrounding whitespace matches nothing, so the operator believes a
// channel is disabled while it keeps sending.
func TestParseCSVTrimsAndDropsEmpties(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{",", nil},
		{"  ,  ", nil},
		{"teled", []string{"teled"}},
		{" teled ", []string{"teled"}},
		{"teled,slakd", []string{"teled", "slakd"}},
		{"teled, slakd ,, discd", []string{"teled", "slakd", "discd"}},
		{"\tteled\n", []string{"teled"}},
	}
	for _, c := range cases {
		if got := parseCSV(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("parseCSV(%q) = %#v, want %#v", c.in, got, c.want)
		}
	}
}

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

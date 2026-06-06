package routd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// stubToken is a fixed-bearer token source for the identity client tests.
func stubToken(context.Context) (string, error) { return "service-routd-token", nil }

// TestHTTPIdentity_Resolves: the client GETs /v1/identities/{sub} against a stub
// authd, sends the service bearer, and decodes the identity + subs. This is the
// HTTP path that replaced the messages.db sibling-read (authd OWNS identity).
func TestHTTPIdentity_Resolves(t *testing.T) {
	var gotAuth, gotPath string
	authd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		json.NewEncoder(w).Encode(map[string]any{
			"identity": map[string]any{"id": "idn-alice", "name": "alice", "created_at": "2026-01-01T00:00:00Z"},
			"subs":     []string{"tg:42", "discord:7"},
		})
	}))
	defer authd.Close()

	r := NewIdentityResolver(authd.URL, stubToken)
	idn, subs, ok := r.Resolve("tg:42")
	if !ok {
		t.Fatal("Resolve: not ok")
	}
	if idn.Name != "alice" || idn.ID != "idn-alice" {
		t.Fatalf("identity = %+v want alice/idn-alice", idn)
	}
	if len(subs) != 2 {
		t.Fatalf("subs = %v want 2", subs)
	}
	if gotAuth != "Bearer service-routd-token" {
		t.Fatalf("Authorization = %q want service bearer", gotAuth)
	}
	if gotPath != "/v1/identities/tg:42" {
		t.Fatalf("path = %q want /v1/identities/tg:42", gotPath)
	}
}

// TestHTTPIdentity_Unclaimed: authd returns {"identity":null} → (zero,nil,false),
// the unclaimed shape inspect_identity renders.
func TestHTTPIdentity_Unclaimed(t *testing.T) {
	authd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"identity": nil, "subs": []string{}})
	}))
	defer authd.Close()

	r := NewIdentityResolver(authd.URL, stubToken)
	if _, _, ok := r.Resolve("tg:999"); ok {
		t.Fatal("unclaimed sub should resolve ok=false")
	}
}

// TestHTTPIdentity_Unreachable: authd down / 5xx → unclaimed, never an error
// (advisory only — faithful to the old nil-sibling guard).
func TestHTTPIdentity_Unreachable(t *testing.T) {
	authd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer authd.Close()

	r := NewIdentityResolver(authd.URL, stubToken)
	if _, _, ok := r.Resolve("tg:42"); ok {
		t.Fatal("5xx should resolve ok=false (advisory, no error path)")
	}
}

// TestNewIdentityResolver_Empty: empty AUTHD_URL → nil resolver (auth-only
// deployment; inspect_identity then answers unclaimed via Server.resolveIdentity).
func TestNewIdentityResolver_Empty(t *testing.T) {
	if r := NewIdentityResolver("", stubToken); r != nil {
		t.Fatalf("empty authdURL → resolver should be nil, got %v", r)
	}
}

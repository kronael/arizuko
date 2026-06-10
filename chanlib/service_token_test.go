package chanlib

// Adapter service-token auth on routd ingress (spec 5/1, the split's A1). A
// RouterClient with a service-token source exchanges its AUTHD_SERVICE_KEY for a
// service:<adapter> JWT against an in-process authd and presents that JWT on
// /v1/messages; a routd-shaped server verifies it over the SAME auth.FetchKeys
// JWKS path production runs. The monolith path (no source) is unchanged: the
// registration token rides the call.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
)

// fakeAuthd serves /v1/keys (JWKS) and /v1/service-token, minting a
// service:<daemon> JWT with the requested scope — exactly the endpoint
// auth.ServiceToken exchanges against.
func fakeAuthd(t *testing.T, key *auth.SigningKey, scope ...string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/keys", func(w http.ResponseWriter, _ *http.Request) {
		doc, err := auth.PublicJWKS(key)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(doc)
	})
	mux.HandleFunc("POST /v1/service-token", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Daemon string `json:"daemon"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		tok, err := key.Sign(auth.TokenClaims{Sub: "service:" + req.Daemon, Typ: "service", Scope: scope}, time.Hour)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"token": tok, "token_type": "Bearer", "scope": scope})
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

// routdLike serves /v1/messages gated on a verified messages:write JWT, the way
// routd's authz does: no bearer / bad bearer → 401, valid token without the
// scope → 403, valid token with messages:write → 200. Returns the server and a
// pointer to the last accepted message id.
func routdLike(t *testing.T, ks *auth.KeySet, gotID *string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/messages", func(w http.ResponseWriter, r *http.Request) {
		sub, err := auth.VerifyHTTP(r, ks)
		if err != nil {
			WriteErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if !auth.HasScope(sub.Scope, "messages", "write") {
			WriteErr(w, http.StatusForbidden, "forbidden")
			return
		}
		var m InboundMsg
		_ = json.NewDecoder(r.Body).Decode(&m)
		*gotID = m.ID
		WriteJSON(w, map[string]any{"ok": true, "id": m.ID})
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

// TestServiceTokenSendMessage: a RouterClient with a real service-token source
// posts /v1/messages and routd verifies + accepts (200). This is the A1 path.
func TestServiceTokenSendMessage(t *testing.T) {
	key, err := auth.NewSigningKey("kid-chanlib")
	if err != nil {
		t.Fatalf("signing key: %v", err)
	}
	authd := fakeAuthd(t, key, "messages:write")
	ks, err := auth.FetchKeys(context.Background(), authd.URL)
	if err != nil {
		t.Fatalf("fetch keys: %v", err)
	}
	var gotID string
	routd := routdLike(t, ks, &gotID)

	rc := NewRouterClient(routd.URL)
	src, err := auth.ServiceToken(authd.URL, "teled", "boot-teled")
	if err != nil {
		t.Fatalf("service token source: %v", err)
	}
	rc.SetServiceToken(src.Token)

	if err := rc.SendMessage(InboundMsg{ID: "in-1", ChatJID: "telegram:1", Content: "hi"}); err != nil {
		t.Fatalf("SendMessage with service token: %v", err)
	}
	if gotID != "in-1" {
		t.Fatalf("routd stored id %q, want in-1", gotID)
	}
}

// TestServiceTokenRejectedWithoutScope: a service token lacking messages:write is
// rejected at routd (403) — the source path is gated, not a bare pass.
func TestServiceTokenRejectedWithoutScope(t *testing.T) {
	key, err := auth.NewSigningKey("kid-chanlib2")
	if err != nil {
		t.Fatalf("signing key: %v", err)
	}
	authd := fakeAuthd(t, key, "chats:read") // wrong scope
	ks, err := auth.FetchKeys(context.Background(), authd.URL)
	if err != nil {
		t.Fatalf("fetch keys: %v", err)
	}
	var gotID string
	routd := routdLike(t, ks, &gotID)

	rc := NewRouterClient(routd.URL)
	src, _ := auth.ServiceToken(authd.URL, "teled", "boot-teled")
	rc.SetServiceToken(src.Token)

	err = rc.SendMessage(InboundMsg{ID: "in-2", ChatJID: "telegram:1", Content: "hi"})
	if err == nil {
		t.Fatal("SendMessage without messages:write must fail")
	}
	if !strings.Contains(err.Error(), "status 403") {
		t.Fatalf("err = %v, want status 403", err)
	}
	if gotID != "" {
		t.Fatal("rejected message must not be stored")
	}
}

// TestServiceTokenAbsentRejected: against a JWT-gated routd, a client with NO
// service-token source presents no bearer → routd 401s. Proves the gate is real
// and that the deleted registration-token fallback does not slip a caller through.
func TestServiceTokenAbsentRejected(t *testing.T) {
	key, err := auth.NewSigningKey("kid-chanlib3")
	if err != nil {
		t.Fatalf("signing key: %v", err)
	}
	authd := fakeAuthd(t, key, "messages:write")
	ks, err := auth.FetchKeys(context.Background(), authd.URL)
	if err != nil {
		t.Fatalf("fetch keys: %v", err)
	}
	var gotID string
	routd := routdLike(t, ks, &gotID)

	// No service-token source → bearer() returns "" → no Authorization header →
	// the gated routd 401s. (A local-dev routd with no JWKS would be open.)
	rc := NewRouterClient(routd.URL)
	err = rc.SendMessage(InboundMsg{ID: "in-3", ChatJID: "telegram:1", Content: "hi"})
	if err == nil {
		t.Fatal("unauthenticated send against a gated routd must fail")
	}
	if !strings.Contains(err.Error(), "status 401") {
		t.Fatalf("err = %v, want status 401", err)
	}
}

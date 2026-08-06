package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
)

// forwarderTok mints a service token for sub under the same key webdBearerKS
// trusts, so a test can prove the allowlist — not the signature — is what
// admits or rejects a forwarder.
func forwarderTok(t *testing.T, key *auth.SigningKey, sub string) string {
	t.Helper()
	tok, err := key.Sign(auth.TokenClaims{Sub: sub, Typ: "service"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// dashd is the operator half of the two-forwarder allowlist: /dash/proxyd/
// reads and writes proxyd_routes over this REST face, so a service:dashd bearer
// must be a valid transit proof exactly as service:webd is.
func TestRoutesResource_DashdBearerAccepted(t *testing.T) {
	ks, key, _ := webdBearerKS(t)
	mux, _, _ := testResourceMux(t, callerFromHTTP(ks))
	req := httptest.NewRequest("GET", "/v1/proxyd_routes", nil)
	req.Header.Set("X-User-Sub", "op@example")
	req.Header.Set("X-User-Groups", `["**"]`)
	req.Header.Set("Authorization", "Bearer "+forwarderTok(t, key, callerDashd))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("service:dashd bearer status = %d body=%s, want 200", w.Code, w.Body.String())
	}
}

// The allowlist stayed an allowlist. A VALID service token for a daemon that is
// not a forwarder (same trusted key, Typ "service") must still be rejected —
// otherwise widening the pin to admit dashd would have turned it into "any
// service token", and every adapter holding one could forge operator X-User-*.
func TestRoutesResource_UntrustedServiceBearerRejected(t *testing.T) {
	ks, key, _ := webdBearerKS(t)
	mux, _, _ := testResourceMux(t, callerFromHTTP(ks))
	for _, sub := range []string{"service:teled", "service:runed", "service:proxyd"} {
		req := httptest.NewRequest("GET", "/v1/proxyd_routes", nil)
		req.Header.Set("X-User-Sub", "op@example")
		req.Header.Set("X-User-Groups", `["**"]`) // forged operator claim
		req.Header.Set("Authorization", "Bearer "+forwarderTok(t, key, sub))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s bearer status = %d, want 401 (allowlist widened to any service token!)", sub, w.Code)
		}
	}
}

// The audit row names the STAMPED operator, not the forwarding daemon. This is
// what lets dashd mutate through proxyd's API without emitting its own audit
// row: the owner records the human in the mutation's own transaction.
func TestRoutesResource_AuditNamesStampedOperator(t *testing.T) {
	ks, key, _ := webdBearerKS(t)
	mux, _, st := testResourceMux(t, callerFromHTTP(ks))

	req := httptest.NewRequest("POST", "/v1/proxyd_routes",
		strings.NewReader(`{"path":"/myapp/","backend":"http://myapp:8080","auth":"user"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-Sub", "op@example")
	req.Header.Set("X-User-Groups", `["**"]`)
	req.Header.Set("Authorization", "Bearer "+forwarderTok(t, key, callerDashd))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST status = %d body=%s", w.Code, w.Body.String())
	}

	var actorSub, resource string
	if err := st.DB().QueryRow(
		`SELECT COALESCE(actor_sub,''), COALESCE(resource,'') FROM audit_log
		 WHERE action = 'proxyd_routes:create' ORDER BY id DESC LIMIT 1`,
	).Scan(&actorSub, &resource); err != nil {
		t.Fatalf("no proxyd_routes:create audit row: %v", err)
	}
	if actorSub != "op@example" {
		t.Errorf("audit actor_sub = %q, want op@example (the forwarder must not be the actor)", actorSub)
	}
	if !strings.Contains(resource, "proxyd_routes") {
		t.Errorf("audit resource = %q, want the proxyd_routes resource", resource)
	}
}

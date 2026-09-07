package main

// signing_keys_resource.go publishes signing-key METADATA at
// GET /v1/signing_keys — kid, algorithm, lifecycle timestamps, and whether the
// key still verifies. Never key material: authstore.SigningKeyMetadata's column
// list is the enforcement.
//
// It closes the first of spec 5/1's open items. `GET /v1/keys` serves the JWK
// Set and `auth.PublicJWKS` emits {kty,crv,x,y,kid,alg,use} — everything needed
// to verify a token and nothing about the key's life, so `active`, `created_at`
// and `retired_at` were reachable only with sqlite3 on the box. An operator
// could not answer "did the rotation take" from any surface arizuko serves.
//
// The gate, the caller-builder and the mount are audit_resource.go's, reused
// rather than re-invented (5/I is the template): one bearer verification
// against authd's own in-process key set, one literal resource:verb scope.

import (
	"context"
	"net/http"
	"time"

	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
)

// signingKeysResource is authd's mounted decl of the shared catalog resource
// (resreg/resources/signing_keys.go). Store is non-nil only because a nil Store
// marks a forwarder and resreg.invoke skips authorization for those; list does
// not mutate, so no transaction is ever opened on it.
func (s *server) signingKeysResource() resreg.Resource {
	r := resreg.Mount(resources.SigningKeysName)
	r.Authz = func(resreg.Caller, resreg.Action, resreg.Args) (string, map[string]string, error) {
		return "", nil, nil
	}
	r.Gate = instanceWideGate("signing_keys", "read")
	r.Handler = s.signingKeysHandler
	r.Store = s.auditStore()
	return r
}

func (s *server) mountSigningKeys(m *http.ServeMux) {
	resreg.RegisterREST(m, s.signingKeysResource(), s.auditCaller)
}

func (s *server) signingKeysHandler(_ context.Context, x resreg.Execution) (any, error) {
	if x.Action != resreg.ActionList {
		return nil, resreg.Errorf(http.StatusBadRequest, "unknown action %q", x.Action)
	}
	rows, err := s.a.st.SigningKeyMetadata(s.a.maxAccessTTL, time.Now())
	if err != nil {
		return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
	}
	return rows, nil
}

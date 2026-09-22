package main

// signing_keys_resource.go publishes signing-key METADATA at
// GET /v1/signing_keys: kid, algorithm, lifecycle timestamps, and whether the
// key still verifies. Never key material: authstore.SigningKeyMetadata's
// projection is the enforcement. `GET /v1/keys` serves the JWK Set, which says
// nothing about a key's life (spec 5/1).

import (
	"context"
	"net/http"
	"time"

	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
)

// signingKeysResource is authd's mounted decl of the shared catalog resource
// (resreg/resources/signing_keys.go).
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

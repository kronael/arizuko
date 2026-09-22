package routd

// secrets_resource.go — the operator secret write surface on one
// resreg.Resource. NO agent MCP tool and NO read face: a sealed value must
// never appear in any read surface (spec 5/8). FORWARDER (Store nil), unlike
// every other cold-tier fold: resreg.invoke copies ALL call args into
// audit_log, and the redaction regex matches `key`/`token`/`secret` but NOT a
// bare `value`, so resreg's own audit would write the plaintext. The handler
// calls the sealing s.db.SetSecret/DeleteSecret, which emit the value-SAFE
// audit rows themselves. resreg skips the Gate for forwarders, so the
// operator auth rides Authz and denies BEFORE the handler logs anything.

import (
	"context"
	"errors"
	"net/http"

	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	"github.com/kronael/arizuko/store"
)

// mountSecrets wires the write-only REST face (POST create + DELETE delete).
func (s *Server) mountSecrets(mux *http.ServeMux) {
	resreg.RegisterREST(mux, s.secretsResource(), s.restCaller(nil))
}

// secretsResource is the single renderer for the secret write surface; Store
// is nil (forwarder), Authz carries the operator gate.
func (s *Server) secretsResource() resreg.Resource {
	r := resreg.Mount(resources.SecretsName)
	r.Authz = s.secretsAuthz
	r.Handler = s.secretsHandler
	return r
}

// secretsAuthz requires the secrets:write scope, then binds the target scope
// to the caller's authority: a folder secret must sit in the caller's subtree
// (ownsFolder); a user secret must be the caller's own sub (an empty JWT
// folder is root, unconstrained). scope_kind validation + the env-profile-key
// rejection stay in store.validateScope. A nil Verifier is open.
func (s *Server) secretsAuthz(c resreg.Caller, _ resreg.Action, args resreg.Args) (string, map[string]string, error) {
	if s.verify == nil {
		return "", nil, nil
	}
	if !hasAnyScope(c.Scopes(), []string{"secrets:write"}) {
		return "", nil, resreg.Errorf(http.StatusForbidden, "missing scope secrets:write")
	}
	scopeID := argString(args, "scope_id")
	switch store.SecretScope(argString(args, "scope")) {
	case store.ScopeFolder:
		if !ownsFolder(c.Claims["jwt_folder"], scopeID) {
			return "", nil, resreg.Errorf(http.StatusForbidden, "secret scope outside caller subtree: %s", scopeID)
		}
	case store.ScopeUser:
		if jf := c.Claims["jwt_folder"]; jf != "" && scopeID != c.Sub {
			return "", nil, resreg.Errorf(http.StatusForbidden, "user secret scope must be your own sub")
		}
	}
	return "", nil, nil
}

// secretsHandler runs the two writes through the audited, sealing store
// methods: create seals + upserts; delete 404s when absent.
func (s *Server) secretsHandler(_ context.Context, x resreg.Execution) (any, error) {
	scope := store.SecretScope(argString(x.Args, "scope"))
	scopeID := argString(x.Args, "scope_id")
	key := argString(x.Args, "key")
	switch x.Action {
	case resreg.ActionCreate:
		value := argString(x.Args, "value")
		if value == "" {
			return nil, resreg.Errorf(http.StatusBadRequest, "value required")
		}
		if err := s.db.SetSecret(scope, scopeID, key, value); err != nil {
			return nil, resreg.Errorf(http.StatusBadRequest, "%v", err)
		}
		return apiv1.OK{OK: true}, nil
	case resreg.ActionDelete:
		err := s.db.DeleteSecret(scope, scopeID, key)
		if errors.Is(err, store.ErrSecretNotFound) {
			return nil, resreg.Errorf(http.StatusNotFound, "no such secret")
		}
		if err != nil {
			return nil, resreg.Errorf(http.StatusBadRequest, "%v", err)
		}
		return apiv1.OK{OK: true}, nil
	}
	return nil, resreg.Errorf(http.StatusBadRequest, "unknown action %q", x.Action)
}

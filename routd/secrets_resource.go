package routd

// secrets_resource.go — 5/16 REST-face fold of the operator secret write surface
// (POST /v1/secrets set/seal, DELETE /v1/secrets/{key}) onto one resreg.Resource,
// the write-only twin of the acl/tasks folds. There is NO agent MCP tool and NO
// read/list face: the agent never sets secrets, and a secret's sealed value must
// never appear in any read surface (spec 5/8 §"Secret safety"). So the resource
// carries exactly two write Endpoints and no MCPDoc — deriveMCPTools surfaces
// nothing.
//
// FORWARDER, not tx-backed (Store nil). Every other cold-tier fold sets Store
// non-nil so resreg opens the mutation+audit tx. secrets does NOT, for one
// reason: resreg.invoke copies ALL call args into the audit_log params_summary,
// and the audit redaction regex (audit/log.go) matches `key`/`token`/`secret`
// but NOT a bare `value` — so routing the write through resreg's own audit would
// write the plaintext secret into audit_log on every path (success, deny, error).
// As a forwarder resreg writes no audit row and opens no tx; the handler calls
// the EXISTING s.db.SetSecret/DeleteSecret, which seal at rest under SECRETS_KEY,
// run store.validateScope (env-profile key rejection), and emit the value-SAFE
// secret.set/secret.delete audit rows themselves (params = {encrypted}, never the
// value). No nested tx, no deadlock, one audit row, zero plaintext on disk.
//
// Because resreg skips the injected Gate for forwarders, the operator auth —
// secrets:write scope + folder/scope containment — rides Authz, the one gate hook
// invoke runs for a forwarder. It denies BEFORE the handler, so a rejected write
// never reaches SetSecret and never logs a value. This also closes the pre-fold
// hole where handleSecretSet gated on the secrets:write bearer scope ALONE and
// never bound the target scope to the caller's folder (the acl fold closed the
// identical hole).

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	"github.com/kronael/arizuko/store"
)

// mountSecrets wires the operator/human REST face (POST create + DELETE delete)
// onto the shared secretsHandler via resreg.RegisterREST. Write-only: no read
// twin, no agent MCP tool.
func (s *Server) mountSecrets(mux *http.ServeMux) {
	resreg.RegisterREST(mux, s.secretsResource(), s.secretsRESTCaller)
}

// secretsResource is the single renderer for the secret write surface. Store is
// nil (forwarder — see file header): the handler calls the audited, sealing
// s.db.SetSecret/DeleteSecret, and resreg neither opens a tx nor writes an audit
// row. Authz carries the operator gate (forwarders skip Resource.Gate).
func (s *Server) secretsResource() resreg.Resource {
	return resreg.Resource{
		Name:      resources.SecretsName,
		Endpoints: resources.SecretsEndpoints, // single source: doc + REST read one list
		Authz:     s.secretsAuthz,
		Handler:   s.secretsHandler,
	}
}

// secretsRESTCaller builds the REST Caller: identity via the Verifier, held
// scopes + JWT folder in Claims for secretsAuthz. A nil Verifier is open
// (local-dev), mirroring s.authz.
func (s *Server) secretsRESTCaller(r *http.Request) (resreg.Caller, error) {
	var sub, folder string
	var scope []string
	if s.verify != nil {
		var err error
		sub, scope, folder, err = s.verify.Verify(r)
		if err != nil {
			return resreg.Caller{}, err
		}
	}
	return resreg.Caller{
		Sub:    sub,
		Folder: folder,
		Claims: map[string]string{"scopes": strings.Join(scope, " "), "jwt_folder": folder},
	}, nil
}

// secretsAuthz is the operator gate (forwarders skip Resource.Gate, so the policy
// lives here — the hook invoke always runs). It requires the secrets:write scope,
// then binds the target scope to the caller's authority: a folder secret must sit
// in the caller's folder subtree (ownsFolder); a user secret must be the caller's
// own sub (root — an empty JWT folder — is unconstrained). scope_kind validation +
// the env-profile-key rejection stay in store.validateScope (a 400 from the
// handler). A nil Verifier is open.
func (s *Server) secretsAuthz(c resreg.Caller, _ resreg.Action, args resreg.Args) (string, map[string]string, error) {
	if s.verify == nil {
		return "", nil, nil
	}
	if !hasAnyScope(strings.Fields(c.Claims["scopes"]), []string{"secrets:write"}) {
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

// secretsHandler runs the two write actions against routd's OWN routd.db via the
// EXISTING audited, sealing store methods (see file header for why not tx-bound):
// create seals + upserts under SECRETS_KEY; delete removes and 404s when absent.
// resreg writes no audit row for this forwarder — s.db.SetSecret/DeleteSecret emit
// the value-safe secret.set/secret.delete rows themselves.
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

package main

// audit_resource.go publishes authd's own audit_log at GET /v1/audit. `login`
// rows are the ONLY place that knows which provider identity was presented
// before everything downstream collapses to the canonical sub (BUGS F29). No
// handler here touches a signing key, refresh token or service secret.

import (
	"context"
	"net/http"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/store"
)

// auditResource is authd's mounted decl of the shared catalog resource
// (resreg/resources/audit.go).
func (s *server) auditResource() resreg.Resource {
	r := resreg.Mount(resources.AuditName)
	r.Authz = func(resreg.Caller, resreg.Action, resreg.Args) (string, map[string]string, error) {
		return "", nil, nil
	}
	r.Gate = scopeGate("audit", "read")
	r.Handler = s.auditHandler
	r.Store = s.auditStore()
	return r
}

// auditStore is the non-forwarder marker every authd resource passes as Store:
// resreg.invoke treats a nil Store as a forwarder and SKIPS authorization, so
// it must be non-nil even on read-only resources.
func (s *server) auditStore() *store.Store { return store.NewOver(s.a.obj) }

// mountAudit wires GET /v1/audit onto authd's mux.
func (s *server) mountAudit(m *http.ServeMux) {
	resreg.RegisterREST(m, s.auditResource(), s.auditCaller)
}

// auditCaller verifies the bearer against authd's own in-process key set; no
// JWKS fetch, because authd holds the private keys. The scope rides in Claims
// so the Gate can answer 403; a builder can only fail 401.
func (s *server) auditCaller(r *http.Request) (resreg.Caller, error) {
	caller, err := auth.VerifyHTTP(r, s.a.LocalKeySet())
	if err != nil {
		return resreg.Caller{}, err
	}
	return resreg.Caller{
		Sub:    caller.Sub,
		Folder: caller.Extra["arz/folder"],
		Claims: resreg.ScopeClaims(caller.Scope),
	}, nil
}

// scopeGate is the operator-only check every authd resource injects: a literal
// resource:verb scope. Such a scope is unreachable by any human bearer, and
// that is the mechanism: a user token's scope list holds FOLDER GLOBS and
// auth.scopeMatches rejects every held value without a colon. There is no
// local-dev open branch: authd always has a key set.
func scopeGate(resource, verb string) func(resreg.Execution, string, map[string]string) error {
	return func(x resreg.Execution, _ string, _ map[string]string) error {
		if !auth.HasScope(x.Caller.Scopes(), resource, verb) {
			return resreg.Errorf(http.StatusForbidden, "missing scope %s:%s", resource, verb)
		}
		return nil
	}
}

// instanceWideGate is scopeGate plus a refusal of any folder-claimed caller,
// for the resources that have NOTHING to narrow by: a signing key is
// instance-global and a session is keyed by `sub`. Defence in depth: a bearer
// holding `sessions:write` has already proven it is not a human token.
func instanceWideGate(resource, verb string) func(resreg.Execution, string, map[string]string) error {
	scoped := scopeGate(resource, verb)
	return func(x resreg.Execution, scope string, params map[string]string) error {
		if err := scoped(x, scope, params); err != nil {
			return err
		}
		if x.Caller.Folder != "" {
			return resreg.Errorf(http.StatusForbidden,
				"%s is instance-wide and cannot be narrowed to folder %q", resource, x.Caller.Folder)
		}
		return nil
	}
}

// auditHandler serves the read through audit.Query, the same renderer routd
// and runed use. A token carrying a folder claim is pinned to that subtree and
// its `folder` argument cannot widen it. No claim means instance-wide ONLY
// because the Gate already proved audit:read; an absent folder claim is not
// evidence of operator authority, since a two-grant tenant is equally
// claimless (the recorded cross-tenant leak).
func (s *server) auditHandler(ctx context.Context, x resreg.Execution) (any, error) {
	if x.Action != resreg.ActionList {
		return nil, resreg.Errorf(http.StatusBadRequest, "unknown action %q", x.Action)
	}
	f := audit.Filter{
		Folder:   x.Args.Str("folder"),
		Category: x.Args.Str("category"),
		Actor:    x.Args.Str("actor"),
		Before:   x.Args.Str("before_id"),
		Limit:    int(x.Args.Int("limit")),
	}
	if x.Caller.Folder != "" {
		f.Folder = x.Caller.Folder
	}
	rows, err := audit.Query(ctx, s.a.obj, f)
	if err != nil {
		return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
	}
	return rows, nil
}

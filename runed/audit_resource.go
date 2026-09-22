package runed

// audit_resource.go publishes runed's own audit_log at GET /v1/audit: "who
// killed that run" (BUGS F29). No action mutates, which is load-bearing: a
// read that audited itself would make one operator page-load append rows
// forever.

import (
	"context"
	"net/http"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/store"
)

// auditScope is the scope a caller must hold; runed owns no acl table. It is
// fail-closed against a human bearer by construction: a user token's scope
// list holds FOLDER GLOBS and auth.scopeMatches rejects any held value
// without a colon.
const auditScope = "audit:read"

// auditResource is the mounted decl. Store is non-nil so resreg runs the Gate
// at all: a nil Store marks a forwarder and invoke skips authorization.
func (s *Server) auditResource() resreg.Resource {
	r := resreg.Mount(resources.AuditName)
	r.Gate = s.auditGate
	r.Handler = s.auditHandler
	r.Store = store.NewOver(s.db.Obj())
	return r
}

// mountAudit wires GET /v1/audit onto the mux.
func (s *Server) mountAudit(mux *http.ServeMux) {
	resreg.RegisterREST(mux, s.auditResource(), s.auditCaller)
}

// auditCaller builds the REST Caller from the verified bearer. The scope rides
// in Claims because the Gate must answer 403 and a builder can only fail 401.
// A nil Verifier is open (local-dev).
func (s *Server) auditCaller(r *http.Request) (resreg.Caller, error) {
	if s.verify == nil {
		return resreg.Caller{}, nil
	}
	sub, scope, folder, err := s.verify.Verify(r)
	if err != nil {
		return resreg.Caller{}, err
	}
	return resreg.Caller{Sub: sub, Folder: folder, Claims: resreg.ScopeClaims(scope)}, nil
}

// auditGate is the injected authorization: hold audit:read, or 403.
func (s *Server) auditGate(x resreg.Execution, _ string, _ map[string]string) error {
	if s.verify == nil {
		return nil // local-dev open mode, mirrors Server.authz
	}
	if !auth.HasScope(x.Caller.Scopes(), "audit", "read") {
		return resreg.Errorf(http.StatusForbidden, "missing scope "+auditScope)
	}
	return nil
}

// auditHandler serves the read through audit.Query. CONTAINMENT is resolved
// here, not in the Gate, because it is a bound on the rows: a token carrying
// a folder claim is pinned to that subtree and its `folder` argument cannot
// widen it. That is safe ONLY because the Gate already proved audit:read; an
// empty folder claim is NOT evidence of operator authority (the recorded
// cross-tenant leak).
func (s *Server) auditHandler(ctx context.Context, x resreg.Execution) (any, error) {
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
	rows, err := audit.Query(ctx, s.db.Obj(), f)
	if err != nil {
		return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
	}
	return rows, nil
}

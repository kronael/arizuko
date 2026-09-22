package routd

// audit_resource.go publishes routd's own audit_log on both faces. The
// resource is registered ONCE and mounted by each daemon that owns an
// audit_log, so `/v1/audit` means "THIS daemon's log" everywhere (BUGS F29).
// A successful read writes no row (specs/5/I): one page-load must not append
// to the log it is reading. `audit:read` is unreachable by any human bearer
// (a user token's scopes are FOLDER GLOBS), so REST is service:dashd only;
// the agent tool is default-deny behind `mcp:query_audit` at the agent's OWN
// folder. Neither gate reads the folder claim to decide AUTHORITY, only to
// narrow rows: an absent claim is not evidence of operator status, and keying
// a list-all on it is the recorded cross-tenant leak.

import (
	"context"
	"net/http"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
)

// auditMCPNames is single-sourced in resreg/resources.
var auditMCPNames = resources.AuditMCPNames

// auditResource is the single renderer for both faces. Store is non-nil so
// resreg runs the Gate at all — a nil Store marks a forwarder, and invoke
// skips authorization for those.
func (s *Server) auditResource() resreg.Resource {
	r := resreg.Mount(resources.AuditName)
	r.Handler = s.auditHandler
	r.Store = s.db.store()
	return r
}

// mountAudit wires GET /v1/audit onto the same handler query_audit uses.
func (s *Server) mountAudit(mux *http.ServeMux) {
	res := s.auditResource()
	res.Gate = s.auditRESTGate
	resreg.RegisterREST(mux, res, s.restCaller(nil))
}

// auditRESTGate requires the audit:read scope, matching runed's and authd's
// gate on the same path so one dashd fan-out meets one answer three times.
func (s *Server) auditRESTGate(x resreg.Execution, _ string, _ map[string]string) error {
	if s.verify == nil {
		return nil // local-dev open mode, mirrors s.authz
	}
	if !auth.HasScope(x.Caller.Scopes(), "audit", "read") {
		return resreg.Errorf(http.StatusForbidden, "missing scope audit:read")
	}
	return nil
}

// auditPostBuild mounts query_audit on the agent socket at the agent's OWN
// folder — not the whole tree as installed_packages binds — because an audit
// row is folder-shaped.
func (s *Server) auditPostBuild(folder, callerSub string, authorize authorizeFn, visible func(string) bool) func(*mcpserver.MCPServer) {
	res := s.auditResource()
	res.Gate = func(x resreg.Execution, _ string, _ map[string]string) error {
		name := auditMCPNames[x.Action]
		if !authorize(folder, "mcp:"+name, nil) {
			return resreg.Errorf(http.StatusForbidden,
				"%s reads this folder's audit trail: not permitted", name)
		}
		return nil
	}
	return mountAgentResource(res, callerSub, folder, visible)
}

// auditHandler serves the read through audit.Query, the renderer runed and
// authd share. Containment is a bound on the rows, resolved here: a caller
// with a folder is pinned to it and the `folder` argument cannot widen it; on
// the AGENT socket a missing folder is refused rather than read as "everything".
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
	if x.Surface == audit.SurfaceMCP && x.Caller.Folder == "" {
		return nil, resreg.Errorf(http.StatusForbidden,
			"query_audit needs a folder-bound socket")
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

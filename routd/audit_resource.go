package routd

// audit_resource.go publishes routd's own audit_log on both faces: the
// operator REST face at GET /v1/audit and the agent socket's `query_audit`.
//
// The resource is registered ONCE (resreg/resources/audit.go) and mounted by
// each daemon that owns an audit_log — routd, runed, authd — so `/v1/audit`
// means "THIS daemon's log" everywhere and dashd federates by fanning ONE path
// out to three hosts (BUGS F29).
//
// Nothing here writes a row on a successful read: list does not mutate, so
// resreg opens no tx and resreg.emitAudit returns before the insert. Denials
// and errors still land, which is the forensic value a read surface has. That
// is the read/write split specs/5/I exists to draw, and on this table it is
// what stops one operator page-load appending to the log it is reading.
//
// TWO GATES, ONE HANDLER, and the containment differs because the callers do:
//
//   - REST is service-to-service. `audit:read` is unreachable by any human
//     bearer — a user token's scope list holds FOLDER GLOBS (UserScopes), and
//     auth.scopeMatches rejects every held value without a colon, so `acme/**`
//     fails and so does an operator's own `**`. The lone holder is
//     service:dashd, whose /dash/audit/ page is requireOperator-gated.
//   - The agent socket is where a genuinely non-operator caller exists, so
//     that is where folder containment does real work: the tool is default-
//     deny behind `mcp:query_audit` at the agent's OWN folder, and the handler
//     pins the row filter to that folder with no argument able to widen it.
//     This is specs/5/I open question 2 — self-introspection scoped to the
//     agent's own folder — answered as the spec leaned.
//
// Neither gate reads the folder claim to decide AUTHORITY, only to narrow rows.
// An absent claim is not evidence of operator status: routd stamps a folder
// only when the sub holds exactly one scope (handleUserScopes), so a tenant
// with two grants is equally claimless. Keying a list-all on that claim is the
// recorded cross-tenant leak, and this file never does it.

import (
	"context"
	"net/http"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/store"
)

// auditMCPNames is the action→flat-tool-name map, single-sourced in
// resreg/resources. Aliased here for the Gate's action→policy-name lookup.
var auditMCPNames = resources.AuditMCPNames

// auditResource is the single renderer for both faces. Store is non-nil so
// resreg runs the Gate at all — a nil Store marks a forwarder and invoke skips
// authorization for those.
func (s *Server) auditResource() resreg.Resource {
	return resreg.Resource{
		Name:      "audit",
		Endpoints: resources.AuditEndpoints,
		MCPDoc:    resources.AuditMCPDoc,
		MCPArgs:   resources.AuditMCPArgs,
		MCPNames:  auditMCPNames,
		Authz: func(resreg.Caller, resreg.Action, resreg.Args) (string, map[string]string, error) {
			return "", nil, nil
		},
		Handler: s.auditHandler,
		Store:   store.New(s.db.SQL()),
	}
}

// mountAudit wires GET /v1/audit onto the same handler the agent's query_audit
// tool uses.
func (s *Server) mountAudit(mux *http.ServeMux) {
	res := s.auditResource()
	res.Gate = s.auditRESTGate
	resreg.RegisterREST(mux, res, s.auditRESTCaller)
}

// auditRESTCaller builds the REST Caller. The scope rides in Claims because a
// caller-builder can only fail 401 and the Gate must answer 403. A nil Verifier
// is open (local-dev), mirroring s.authz and every sibling REST caller.
func (s *Server) auditRESTCaller(r *http.Request) (resreg.Caller, error) {
	if s.verify == nil {
		return resreg.Caller{}, nil
	}
	sub, scope, folder, err := s.verify.Verify(r)
	if err != nil {
		return resreg.Caller{}, err
	}
	return resreg.Caller{Sub: sub, Folder: folder, Claims: resreg.ScopeClaims(scope)}, nil
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

// auditPostBuild mounts query_audit on the agent socket. The authorization
// target is the agent's OWN folder — not the whole tree as installed_packages
// binds — because an audit row is folder-shaped and an agent asking what it
// did last turn is asking about its own folder.
func (s *Server) auditPostBuild(folder, callerSub string, authorize authorizeFn, visible func(string) bool) func(*mcpserver.MCPServer) {
	res := s.auditResource()
	res.Gate = func(x resreg.Execution, _ string, _ map[string]string) error {
		name := auditMCPNames[x.Action]
		if !authorize(callerSub, folder, "mcp:"+name, nil) {
			return resreg.Errorf(http.StatusForbidden,
				"%s reads this folder's audit trail: not permitted", name)
		}
		return nil
	}
	return mountAgentResource(res, callerSub, folder, visible)
}

// auditHandler serves the read off routd.db through audit.Query — the same
// renderer runed and authd serve theirs through.
//
// Containment is resolved here rather than in either Gate, because it is not a
// yes/no on the call but a bound on the rows:
//
//   - A caller with a folder is pinned to that subtree; the `folder` argument
//     cannot widen it, only the pin decides.
//   - On the AGENT socket a missing folder is refused outright rather than read
//     as "everything". The socket folder is supposed to be the agent's group;
//     if it ever arrives empty the safe reading is that containment is unknown,
//     and an audit log is the last table to hand out on an unknown bound.
//     REST keeps the opposite default because its Gate already proved
//     instance-wide authority via a scope no folder-bound principal can hold.
func (s *Server) auditHandler(ctx context.Context, x resreg.Execution) (any, error) {
	if x.Action != resreg.ActionList {
		return nil, resreg.Errorf(http.StatusBadRequest, "unknown action %q", x.Action)
	}
	f := audit.Filter{
		Folder:   x.Args.Str("folder"),
		Category: x.Args.Str("category"),
		Actor:    x.Args.Str("actor"),
		BeforeID: x.Args.Int("before_id"),
		Limit:    int(x.Args.Int("limit")),
	}
	if x.Surface == audit.SurfaceMCP && x.Caller.Folder == "" {
		return nil, resreg.Errorf(http.StatusForbidden,
			"query_audit needs a folder-bound socket")
	}
	if x.Caller.Folder != "" {
		f.Folder = x.Caller.Folder
	}
	rows, err := audit.Query(ctx, s.db.SQL(), f)
	if err != nil {
		return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
	}
	return rows, nil
}

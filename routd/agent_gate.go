package routd

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/store"
)

// agent_gate.go holds the closures every cold-tier resource's agent-socket
// postBuild shares. 4/R: authorization is ONE evaluator (auth.Authorize on the
// ACTUAL target scope); visibility is one view (auth.EffectiveActions over the acl
// rows). Keeping ONE source per concern stops the per-resource copies from drifting
// (a past drift opened a cross-tenant hole — see web_routes delete).

// authorizeFn is db.Authorize's shape — the per-call row-ACL check the agent socket
// runs. `folder` is the SCOPE matched against the caller's grant globs (the caller's
// own folder for magnitude, the target folder for containment). ServeTurnMCP injects
// it per turn via turnAuthorize.
type authorizeFn func(sub, folder, action string, params map[string]string) bool

// turnAuthorize renders the per-call row-ACL check for one turn's agent socket. A
// normal turn binds db.Authorize (role:member floor + operator/delegated grants). An
// elevated (operator /root) turn allows every call — the operator holds role:operator
// (`*`, WITH GRANT OPTION), so /root is unrestricted.
func (s *Server) turnAuthorize(elevated bool) authorizeFn {
	if !elevated {
		return s.db.Authorize
	}
	return func(string, string, string, map[string]string) bool { return true }
}

// turnIdentity is the turn's effective identity for the agent-socket gates: the
// socket folder normally, root under an operator /root elevation (cmdRoot gates
// /root on IsOperator, so this is reachable only by a verified operator).
func turnIdentity(folder string, elevated bool) auth.Identity {
	id := auth.Resolve(folder)
	if elevated {
		id.IsRoot = true // /root elevation is root (spec 4/R decision 1)
	}
	return id
}

// turnVisible builds the tools/list visibility predicate for one turn: an elevated
// /root turn sees everything; else a tool shows iff the caller holds its `mcp:<tool>`
// grant at any scope (auth.EffectiveActions). Reads are advertised unconditionally by
// their own registration, never through this predicate.
func (s *Server) turnVisible(callerSub string, elevated bool) func(name string) bool {
	if elevated {
		return func(string) bool { return true }
	}
	held := auth.EffectiveActions(store.New(s.db.SQL()), auth.Caller{Principal: callerSub})
	return func(name string) bool { return held("mcp:" + name) }
}

// agentAllowGate is the no-op resreg Gate the non-forwarder agent resources set so
// resreg's operator defaultGate (a `<resource>:<action>` scope check the agent
// principal never holds) does NOT run on the agent socket. The real authorization —
// one evaluator, auth.Authorize on the ACTUAL target — rides each handler's `contain`
// seam, which resolves the target from the args/id before any write.
func agentAllowGate(resreg.Execution, string, map[string]string) error { return nil }

// agentCallerFor is the Caller resolver every agent-socket resource uses: the
// socket's own principal + folder — these tools never take a caller arg.
func agentCallerFor(callerSub, folder string) func(context.Context, mcp.CallToolRequest) (resreg.Caller, error) {
	return func(context.Context, mcp.CallToolRequest) (resreg.Caller, error) {
		return resreg.Caller{Sub: callerSub, Folder: folder}, nil
	}
}

// mountAgentResource is the ServeMCP seam every agent postBuild returns: mount res's
// tools on the agent socket with the socket's own Caller + the turn's visibility view.
func mountAgentResource(res resreg.Resource, callerSub, folder string, visible func(string) bool) func(*mcpserver.MCPServer) {
	return func(srv *mcpserver.MCPServer) {
		resreg.MCPTools(srv, res, agentCallerFor(callerSub, folder), visible)
	}
}

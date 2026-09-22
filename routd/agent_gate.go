package routd

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/resreg"
)

// agent_gate.go holds the closures every cold-tier resource's agent-socket
// postBuild shares (5/33-paths-roles): authorization is ONE evaluator (auth.Authorize on
// the ACTUAL target scope), visibility one view (auth.EffectiveActions). One
// source per concern stops the per-resource copies drifting — a past drift
// opened a cross-tenant hole (web_routes delete).

// authorizeFn is the per-call row-ACL check the agent socket runs; `folder`
// is the SCOPE matched against the grant globs. It takes NO principal: a
// turn's principals are a property of the turn, so turnAuthorize closes over
// both and no call site can supply a third; the folder principal survives
// only as the audit actor, never as an authorization input.
type authorizeFn func(folder, action string, params map[string]string) bool

// turnAuthorize renders the check on the two axes of 5/33-paths-roles: the turn's folder
// confers, the attested sender adds inside that folder's subtree. An elevated
// /root turn allows every call — the ONLY path by which a person's own
// authority reaches outside the turn's subtree.
func (s *Server) turnAuthorize(t turnMCP) authorizeFn {
	if t.elevated {
		return func(string, string, map[string]string) bool { return true }
	}
	sender := turnCallerSub(t.trigger)
	return func(folder, action string, params map[string]string) bool {
		return s.db.AuthorizeTurn(sender, t.folder, folder, action, params)
	}
}

// turnIdentity is the turn's effective identity for the agent-socket gates:
// the socket folder, or root under an operator /root elevation.
func turnIdentity(folder string, elevated bool) auth.Identity {
	id := auth.Resolve(folder)
	if elevated {
		id.IsRoot = true // /root elevation is root (spec 5/33-paths-roles decision 1)
	}
	return id
}

// turnVisible builds the tools/list predicate: an elevated turn sees
// everything; else a tool shows iff the turn holds its `mcp:<tool>` grant at
// any scope, on the SAME two-axis Caller turnAuthorize evaluates. Listed is
// not authorized: the gate is per-target and confines the sender's half.
func (s *Server) turnVisible(t turnMCP) func(name string) bool {
	if t.elevated {
		return func(string) bool { return true }
	}
	held := auth.EffectiveActions(s.db.store(),
		auth.Caller{Principal: turnCallerSub(t.trigger), Route: t.folder})
	return func(name string) bool { return held("mcp:" + name) }
}

// agentAllowGate is the no-op Gate the non-forwarder agent resources set so
// resreg's operator defaultGate (a scope the agent principal never holds) does
// NOT run; the real authorization rides each handler's `contain` seam.
func agentAllowGate(resreg.Execution, string, map[string]string) error { return nil }

// agentCallerFor is the Caller resolver every agent-socket resource uses: the
// socket's own principal + folder — these tools never take a caller arg.
func agentCallerFor(callerSub, folder string) func(context.Context, mcp.CallToolRequest) (resreg.Caller, error) {
	return func(context.Context, mcp.CallToolRequest) (resreg.Caller, error) {
		return resreg.Caller{Sub: callerSub, Folder: folder}, nil
	}
}

// mountAgentResource is the ServeMCP seam every agent postBuild returns.
func mountAgentResource(res resreg.Resource, callerSub, folder string, visible func(string) bool) func(*mcpserver.MCPServer) {
	return func(srv *mcpserver.MCPServer) {
		resreg.MCPTools(srv, res, agentCallerFor(callerSub, folder), visible)
	}
}

package routd

import (
	"context"
	"net/http"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/kronael/arizuko/auth"
	grantslib "github.com/kronael/arizuko/grants"
	"github.com/kronael/arizuko/resreg"
)

// agent_gate.go holds the closures every cold-tier resource's agent-socket
// postBuild shares. The tool-grant decision is the agent MCP face's single
// security-sensitive line; keeping ONE source stops the per-resource copies from
// drifting (a past drift opened a cross-tenant hole — see web_routes delete).

// authorizeFn is db.Authorize's shape — the per-call row-ACL check the agent
// socket runs. ServeTurnMCP injects it per turn via turnAuthorize.
type authorizeFn func(sub, folder, action string, params map[string]string) bool

// turnAuthorize renders the per-call row-ACL check for one turn's agent socket
// (toolGrant's second half + ipc's authorizeCall via StoreFns.Authorize). A
// normal turn binds db.Authorize (operator acl rows + the folder's static-tier
// default fallback). An elevated (operator /root) turn allows every call:
// ServeTurnMCP already swaps the socket's grant rules to the tier-0 `*` set,
// but db.Authorize's fallback re-derives the folder's STATIC tier, so every
// root-only tool 403'd even under /root and the agent fell back to mcpc
// (issue_chat_link — marinade atlas, 2026-07-16). One elevation, both gates.
func (s *Server) turnAuthorize(elevated bool) authorizeFn {
	if !elevated {
		return s.db.Authorize
	}
	return func(string, string, string, map[string]string) bool { return true }
}

// turnIdentity is the turn's EFFECTIVE structural identity for the agent-socket
// gates' auth.AuthorizeStructural check: the socket folder's tier normally, tier 0
// under an operator /root elevation (cmdRoot gates /root on IsOperator, so this is
// reachable only by a verified operator). Without it a /root turn from a tier-2
// folder still resolved to tier 2 in the STRUCTURAL gate — so network_allow/add_acl/
// register_group 403'd "tier N cannot ..." even under /root, the mirror of the
// row-ACL bug turnAuthorize fixed (auth.Resolve's own docstring names tier 0 the
// /root elevation). One elevation, both gates.
func turnIdentity(folder string, elevated bool) auth.Identity {
	id := auth.Resolve(folder)
	if elevated {
		id.Tier = 0
	}
	return id
}

// toolGrant is the agent socket's tool-grant check: the tool must be permitted by
// the socket's grant rules AND by the injected authorize (tier defaults + operator
// ACL overlay for a normal turn; allow-all for an elevated one), both keyed on the
// socket folder. Returns nil when permitted, else a 403. Resources with extra
// per-target containment (acl, groups) call this, then add their
// AuthorizeStructural.
func toolGrant(rules []string, authorize authorizeFn, callerSub, folder, name string) error {
	if !grantslib.CheckAction(rules, name, nil) {
		return resreg.Errorf(http.StatusForbidden, "%s: not permitted", name)
	}
	if callerSub != "" && !authorize(callerSub, folder, "mcp:"+name, nil) {
		return resreg.Errorf(http.StatusForbidden, "%s: not permitted", name)
	}
	return nil
}

// agentCallerFor is the Caller resolver every agent-socket resource uses: the
// socket's own principal + folder — these tools never take a caller arg.
func agentCallerFor(callerSub, folder string) func(context.Context, mcp.CallToolRequest) (resreg.Caller, error) {
	return func(context.Context, mcp.CallToolRequest) (resreg.Caller, error) {
		return resreg.Caller{Sub: callerSub, Folder: folder}, nil
	}
}

// agentVisible is the tools/list visibility predicate: a tool shows only when a
// socket grant rule matches its name. acl overrides this with its own predicate.
func agentVisible(rules []string) func(string) bool {
	return func(name string) bool { return len(grantslib.MatchingRules(rules, name)) > 0 }
}

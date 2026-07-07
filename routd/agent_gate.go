package routd

import (
	"context"
	"net/http"

	"github.com/mark3labs/mcp-go/mcp"

	grantslib "github.com/kronael/arizuko/grants"
	"github.com/kronael/arizuko/resreg"
)

// agent_gate.go holds the closures every cold-tier resource's agent-socket
// postBuild shares. The tool-grant decision is the agent MCP face's single
// security-sensitive line; keeping ONE source stops the per-resource copies from
// drifting (a past drift opened a cross-tenant hole — see web_routes delete).

// toolGrant is the agent socket's tool-grant check: the tool must be permitted by
// the socket's grant rules AND by db.Authorize (tier defaults + operator ACL
// overlay), both keyed on the socket folder. Returns nil when permitted, else a
// 403. Resources with extra per-target containment (acl, groups) call this, then
// add their AuthorizeStructural.
func (s *Server) toolGrant(rules []string, callerSub, folder, name string) error {
	if !grantslib.CheckAction(rules, name, nil) {
		return resreg.Errorf(http.StatusForbidden, "%s: not permitted", name)
	}
	if callerSub != "" && !s.db.Authorize(callerSub, folder, "mcp:"+name, nil) {
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

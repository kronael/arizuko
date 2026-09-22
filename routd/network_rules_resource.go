package routd

// network_rules_resource.go folds the agent's egress-allowlist tools onto ONE
// resreg.Resource (spec 5/16); there is no REST face. Both writers normalize
// a target through the same grammar, so a rule added as *.example.com is
// removable as example.com. allow/deny take an optional `folder` TARGET arg,
// so a caller may open or close egress for any folder its grant scope covers;
// containment is the Gate's single evaluator on the ARG folder (5/33), and
// egress management is default-deny, absent from role:member.

import (
	"context"
	"log/slog"
	"net/http"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/kronael/arizuko/egress/match"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
)

// list reuses resreg.ActionList so it stays read-only (no tx); allow/deny are
// custom strings so Mutates() is true and each opens the mutation+audit batch.
const (
	networkActionAllow = resreg.Action("allow")
	networkActionDeny  = resreg.Action("deny")
)

// networkMCPNames is single-sourced in resreg/resources.
var networkMCPNames = resources.NetworkRulesMCPNames

// networkRulesResource is the single renderer for the agent's three egress
// tools; the REST face is not mounted from it (network is agent-only).
func (s *Server) networkRulesResource() resreg.Resource {
	r := resreg.Mount(resources.NetworkRulesName)
	r.Handler = s.networkRulesHandler
	r.Store = s.db.store()
	return r
}

// networkRulesHandler runs allow/deny/list: hostname validation and the
// `*.`-glob→apex normalization on write, the resolved+own split on list.
// allow/deny apply to the TARGET folder the Gate has already bounded.
func (s *Server) networkRulesHandler(ctx context.Context, x resreg.Execution) (any, error) {
	switch x.Action {
	case resreg.ActionList:
		folder := x.Caller.Folder
		resolved, err := s.db.ResolveAllowlist(folder)
		if err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		own, err := s.db.ListNetworkRules(folder)
		if err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		return map[string]any{"folder": folder, "resolved": resolved, "own": own}, nil

	case networkActionAllow:
		folder := networkTargetFolder(x.Args, x.Caller.Folder)
		// One rule grammar for every write path (match.ValidEntry, spec 6/25 1b).
		// Bare * is rejected BEFORE the shared grammar: opening all egress is an
		// operator decision, not an agent grant.
		raw := argString(x.Args, "host")
		if raw == "*" {
			return nil, resreg.Errorf(http.StatusBadRequest,
				"opening all egress is an operator decision, not an agent grant")
		}
		if err := match.ValidEntry(raw); err != nil {
			return nil, resreg.Errorf(http.StatusBadRequest,
				"%v — write a bare domain (example.com) or subdomain glob (*.example.com)", err)
		}
		host := match.NormalizeEntry(raw)
		if err := s.db.store().AddNetworkRuleInto(x.Writes, folder, host, x.Caller.Sub); err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		slog.Info("egress allowed", "folder", folder, "host", host)
		return map[string]any{"allowed": true, "folder": folder, "host": host}, nil

	case networkActionDeny:
		folder := networkTargetFolder(x.Args, x.Caller.Folder)
		// Normalized as allow stores it, so the same syntax removes it.
		host := match.NormalizeEntry(argString(x.Args, "host"))
		if host == "" {
			return nil, resreg.Errorf(http.StatusBadRequest, "host required")
		}
		if err := s.db.store().RemoveNetworkRuleInto(x.Writes, folder, host); err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		slog.Info("egress denied", "folder", folder, "host", host)
		return map[string]any{"denied": true, "folder": folder, "host": host}, nil
	}
	return nil, resreg.Errorf(http.StatusBadRequest, "unknown action %q", x.Action)
}

// networkTargetFolder resolves the folder a rule applies to: the `folder` arg,
// else the socket folder. Shared by the Gate and the handler so they cannot
// drift; list declares no `folder` arg.
func networkTargetFolder(args resreg.Args, socketFolder string) string {
	if f := argString(args, "folder"); f != "" {
		return f
	}
	return socketFolder
}

// networkRulesPostBuild mounts the egress tools on the agent socket with the
// db.Authorize Gate + the turn's visibility view.
func (s *Server) networkRulesPostBuild(folder, callerSub string, authorize authorizeFn, visible func(string) bool) func(*mcpserver.MCPServer) {
	res := s.networkRulesResource()
	res.Gate = func(x resreg.Execution, _ string, _ map[string]string) error {
		name := networkMCPNames[x.Action]
		// One evaluator on the TARGET folder: only an operator-delegated
		// `mcp:<name>` scoped to cover it, or /root (5/33 decision 8).
		target := networkTargetFolder(x.Args, folder)
		if !authorize(target, "mcp:"+name, nil) {
			return resreg.Errorf(http.StatusForbidden, "%s on %s: not permitted", name, target)
		}
		return nil
	}
	return mountAgentResource(res, callerSub, folder, visible)
}

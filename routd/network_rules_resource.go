package routd

// network_rules_resource.go folds the agent's egress-allowlist tools onto ONE
// resreg.Resource (spec 5/16). resreg owns the plumbing; routd owns the auth POLICY.
// allow/deny/list are resource-specific verbs mapped to the flat live tool names via
// MCPNames.
//
// allow/deny take an optional `folder` TARGET arg (defaulting to the caller's own
// folder), so a caller may open or close egress for its own folder OR any folder its
// grant scope covers — the documented egress-escalation path. Containment is the
// injected Gate's single evaluator run on the ARG folder (5/33: the grant scope IS the
// containment), and egress management is default-deny, absent from role:member. list
// reads the socket folder's own resolved+own view. There is no REST face today.

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"time"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/kronael/arizuko/egress/match"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/store"
)

// Egress-allowlist actions are resource-specific verbs, not the CRUD shape. list
// reuses resreg.ActionList (== "list") so it stays read-only (Mutates() false, no
// tx); allow/deny are custom strings so Mutates() is true and each opens the
// mutation+audit tx.
const (
	networkActionAllow = resreg.Action("allow")
	networkActionDeny  = resreg.Action("deny")
)

// networkMCPNames is the action→flat-tool-name map, single-sourced in resreg/
// resources (NetworkRulesMCPNames). Aliased here for the Gate's action→policy-name
// lookup.
var networkMCPNames = resources.NetworkRulesMCPNames

// networkRulesResource is the single renderer for the agent's three egress tools.
// Endpoints exist only to drive deriveMCPTools (Action ∩ MCPDoc) — the REST face
// is NOT mounted from this resource (network is agent-only). Store is a
// store.Store over routd.db so resreg.invoke opens the mutation+audit tx there.
func (s *Server) networkRulesResource() resreg.Resource {
	r := resreg.Mount(resources.NetworkRulesName)
	r.Authz = func(resreg.Caller, resreg.Action, resreg.Args) (string, map[string]string, error) {
		return "", nil, nil
	}
	r.Handler = s.networkRulesHandler
	r.Store = store.New(s.db.SQL())
	return r
}

// networkRulesHandler runs allow/deny/list against routd.db, folding in the bespoke
// semantics the deleted ipc bodies enforced: hostname validation and the
// `*.`-glob→apex normalization on write, and the resolved+own split on list.
// allow/deny apply to the TARGET folder, which the Gate has already bounded to the
// caller's granted scope.
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
		// One rule grammar for every write path (match.ValidEntry — spec 6/25 1b): a
		// rule that can never match (URL, host:port, IP) is refused with its reason. The
		// *.example.com glob normalizes onto its apex, which already covers subdomains.
		// Bare * stays rejected here BEFORE the shared grammar: opening all egress is an
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
		if err := addNetworkRuleTx(ctx, x.Tx, folder, host, x.Caller.Sub); err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		slog.Info("egress allowed", "folder", folder, "host", host)
		return map[string]any{"allowed": true, "folder": folder, "host": host}, nil

	case networkActionDeny:
		folder := networkTargetFolder(x.Args, x.Caller.Folder)
		// Normalize the *.example.com glob the same way allow stores it, so a rule
		// added as *.example.com can be removed by the same syntax.
		host := match.NormalizeEntry(argString(x.Args, "host"))
		if host == "" {
			return nil, resreg.Errorf(http.StatusBadRequest, "host required")
		}
		if err := removeNetworkRuleTx(ctx, x.Tx, folder, host); err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		slog.Info("egress denied", "folder", folder, "host", host)
		return map[string]any{"denied": true, "folder": folder, "host": host}, nil
	}
	return nil, resreg.Errorf(http.StatusBadRequest, "unknown action %q", x.Action)
}

// networkTargetFolder resolves the folder an allow/deny rule applies to: the
// `folder` arg when supplied, else the caller's own (socket) folder. Shared by
// the Gate (containment target) and the handler (write target) so they can't
// drift. list declares no `folder` arg, so it always resolves to the socket
// folder.
func networkTargetFolder(args resreg.Args, socketFolder string) string {
	if f := argString(args, "folder"); f != "" {
		return f
	}
	return socketFolder
}

// networkRulesPostBuild returns the ServeMCP seam that mounts the egress tools on
// the agent socket, with the db.Authorize Gate + the turn's visibility view
// injected. Only grants the caller already holds can widen visibility, so a
// denied caller sees nothing new.
func (s *Server) networkRulesPostBuild(folder, callerSub string, authorize authorizeFn, visible func(string) bool) func(*mcpserver.MCPServer) {
	res := s.networkRulesResource()
	res.Gate = func(x resreg.Execution, _ string, _ map[string]string) error {
		name := networkMCPNames[x.Action]
		// One evaluator on the TARGET folder. Egress management is default-deny:
		// role:member does not grant it, so only an operator-delegated `mcp:<name>`
		// scoped to cover the target — or /root — authorizes it. 5/33 decision 8: the
		// grant scope IS the containment; the old hard tier cap is gone.
		target := networkTargetFolder(x.Args, folder)
		if !authorize(callerSub, target, "mcp:"+name, nil) {
			return resreg.Errorf(http.StatusForbidden, "%s on %s: not permitted", name, target)
		}
		return nil
	}
	return mountAgentResource(res, callerSub, folder, visible)
}

// addNetworkRuleTx appends one egress allowlist row on tx (mirrors
// store.AddNetworkRule so the mutation lands in resreg.invoke's tx alongside its
// audit_log row). INSERT OR IGNORE keeps the append idempotent.
func addNetworkRuleTx(ctx context.Context, tx *sql.Tx, folder, target, by string) error {
	_, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO network_rules (folder, target, created_at, created_by) VALUES (?, ?, ?, ?)`,
		folder, target, time.Now().UTC().Format(time.RFC3339), by)
	return err
}

// removeNetworkRuleTx drops one egress allowlist row on tx (mirrors
// store.RemoveNetworkRule). No error if the row is absent.
func removeNetworkRuleTx(ctx context.Context, tx *sql.Tx, folder, target string) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM network_rules WHERE folder = ? AND target = ?`, folder, target)
	return err
}

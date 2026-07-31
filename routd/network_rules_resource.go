package routd

// network_rules_resource.go is the spec 5/16 step after the web_routes pilot:
// the agent's egress-allowlist tools (network_allow/network_deny/network_list)
// ride ONE resreg.Resource instead of three hand-rolled ipc/ipc.go tool bodies.
//
// resreg owns the plumbing (handler dispatch + one tx wrapping the mutation AND
// its audit_log row); routd owns the auth POLICY. Two differences from the
// web_routes pilot, both preserved faithfully:
//
//   - Network tools carry a STRUCTURAL TIER GATE that web_routes lacked
//     (auth.AuthorizeStructural: tier 2+ can't manage egress, tier 1 confined to
//     its subtree; policy.go). It is a hard cap that operator ACL grants can't
//     widen, orthogonal to the row-grant db.Authorize — so the injected Gate runs
//     it too, not just CheckAction + db.Authorize. Dropping it would let an
//     operator-granted tier-2 folder gain egress management the structural gate
//     forbids.
//   - Custom actions: allow/deny/list are resource-specific verbs (not the CRUD
//     ActionCreate/Delete), mapped to the flat live tool names via MCPNames.
//
// allow/deny take an optional `folder` TARGET arg (defaults to the caller's own
// folder): a tier-0/1 caller may open/close egress for its own folder OR any
// folder in its subtree — the documented egress-escalation path (ant/CLAUDE.md
// network_allow(folder, host)). Containment is enforced by AuthorizeStructural on
// the ARG folder, resolving the caller's tier from the SOCKET folder, so cross-
// subtree writes are impossible; tier 2+ can't manage egress at all. list reads
// the socket folder's own resolved+own view (no target arg). There is no REST
// face today (agent-only, like the pilot's agent surface).

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"strings"
	"time"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/groupfolder"
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
	return resreg.Resource{
		Name:      "network_rules",
		Endpoints: resources.NetworkRulesEndpoints, // single source: doc + MCP read one list
		MCPDoc:    resources.NetworkRulesMCPDoc,    // single source (resreg/resources)
		MCPArgs:   resources.NetworkRulesMCPArgs,
		MCPNames:  networkMCPNames,
		Authz: func(resreg.Caller, resreg.Action, resreg.Args) (string, map[string]string, error) {
			return "", nil, nil
		},
		Handler: s.networkRulesHandler,
		Store:   store.New(s.db.SQL()),
	}
}

// networkRulesHandler runs allow/deny/list against routd.db, folding in the
// bespoke semantics the deleted ipc bodies enforced: hostname validation and the
// `*.`-glob→apex normalization on write, and the resolved+own split on list.
// allow/deny apply to the TARGET folder (the `folder` arg, defaulting to the
// caller's own folder) — the egress-escalation path a tier-0/1 caller uses to
// open egress for a descendant; the Gate has already bounded that target to the
// caller's subtree. list reads the caller's own (socket) folder view.
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
		// Accept *.example.com — crackbox already suffix-matches a bare domain to
		// all its subdomains, so normalize the glob to the apex and store that.
		// Bare * (allow-all) stays rejected: opening all egress is an operator
		// decision, not an agent grant.
		host := strings.TrimPrefix(argString(x.Args, "host"), "*.")
		if !validHostname(host) {
			return nil, resreg.Errorf(http.StatusBadRequest,
				"host must be a bare domain (example.com) or subdomain glob "+
					"(*.example.com); no scheme/port/path. A bare domain already covers its subdomains.")
		}
		if err := addNetworkRuleTx(ctx, x.Tx, folder, host, x.Caller.Sub); err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		slog.Info("egress allowed", "folder", folder, "host", host)
		return map[string]any{"allowed": true, "folder": folder, "host": host}, nil

	case networkActionDeny:
		folder := networkTargetFolder(x.Args, x.Caller.Folder)
		// Normalize the *.example.com glob the same way allow stores it, so a rule
		// added as *.example.com can be removed by the same syntax.
		host := strings.TrimPrefix(argString(x.Args, "host"), "*.")
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
// the agent socket, with the tier-aware Gate + MatchingRules visibility for this
// folder's grant rules injected. Only rules the socket already carries can widen
// visibility, so a denied tier still sees nothing new.
func (s *Server) networkRulesPostBuild(folder, callerSub string, rules []string, authorize authorizeFn, callerID auth.Identity) func(*mcpserver.MCPServer) {
	res := s.networkRulesResource()
	res.Gate = func(x resreg.Execution, _ string, _ map[string]string) error {
		name := networkMCPNames[x.Action]
		// Tool grant: rules + the elevation-aware row-ACL check (shared toolGrant,
		// as every other agent resource). A /root turn's rules are `*` AND authorize
		// is allow-all — leaving the raw s.db.Authorize here re-derived the folder's
		// static tier, so egress tools 403'd even under /root (the turnAuthorize bug,
		// which never reached this postBuild — it was the one that took no authorize).
		if err := toolGrant(rules, authorize, callerSub, folder, name); err != nil {
			return err
		}
		// Structural tier gate (web_routes had none): tier 2+ can't manage egress,
		// tier 1 is confined to its own subtree, tier 0 is unrestricted. Containment
		// is checked on the TARGET (arg) folder — resolving the caller's tier from
		// callerID (tier 0 under /root, else the SOCKET folder's tier) — so a tier-0/1
		// caller may open egress for any folder in its subtree while a non-elevated
		// tier-2 stays denied. A hard cap operator ACL grants can't widen; only the
		// operator-gated /root elevation lifts it. Exactly the deleted ipc body's
		// authzStructural(name, TargetFolder: argFolder).
		target := networkTargetFolder(x.Args, folder)
		if err := auth.AuthorizeContainment(store.New(s.db.SQL()), callerID.Folder, name, target, callerID.IsRoot); err != nil {
			return resreg.Errorf(http.StatusForbidden, "%s: %v", name, err)
		}
		return nil
	}
	return mountAgentResource(res, callerSub, folder, rules)
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

// validHostname keeps the network-rule call sites on their existing name while
// groupfolder owns hostname validation.
func validHostname(h string) bool {
	return groupfolder.ValidVhostName(h)
}

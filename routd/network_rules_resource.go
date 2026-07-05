package routd

// network_rules_resource.go is the spec 5/44 step after the web_routes pilot:
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

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/kronael/arizuko/auth"
	grantslib "github.com/kronael/arizuko/grants"
	"github.com/kronael/arizuko/resreg"
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

// networkMCPNames maps each resreg action to the flat tool name the live agent
// already calls; keeping them avoids renaming an in-container tool.
var networkMCPNames = map[resreg.Action]string{
	networkActionAllow: "network_allow",
	networkActionDeny:  "network_deny",
	resreg.ActionList:  "network_list",
}

// networkRulesResource is the single renderer for the agent's three egress tools.
// Endpoints exist only to drive deriveMCPTools (Action ∩ MCPDoc) — the REST face
// is NOT mounted from this resource (network is agent-only). Store is a
// store.Store over routd.db so resreg.invoke opens the mutation+audit tx there.
func (s *Server) networkRulesResource() resreg.Resource {
	return resreg.Resource{
		Name: "network_rules",
		Endpoints: []resreg.Endpoint{
			{Verb: "POST", Path: "/v1/network_rules", Action: networkActionAllow},
			{Verb: "DELETE", Path: "/v1/network_rules", Action: networkActionDeny},
			{Verb: "GET", Path: "/v1/network_rules", Action: resreg.ActionList},
		},
		MCPDoc: map[resreg.Action]string{
			networkActionAllow: "Open egress to `host` for `folder` and every descendant by appending an allowlist rule. " +
				"Use when an agent needs to reach a host the default-deny proxy blocks (e.g. a vendor API). " +
				"`host` is a bare domain (no scheme/port), e.g. 'example.com', or a `*.example.com` subdomain glob. " +
				"`folder` is the target folder — your own folder (the default when omitted) or any folder in your subtree, " +
				"e.g. network_allow(folder='atlas/search', host='krons.fiu.wtf'). A rule at a parent folder cascades to all children.",
			networkActionDeny: "Remove an egress allowlist rule: close access to `host` for `folder`. " +
				"`folder` defaults to your own folder and may be any folder in your subtree. " +
				"Only drops a rule set on `folder` itself; a host inherited from an ancestor must be removed at that ancestor.",
			resreg.ActionList: "List the egress allowlist for THIS folder: `resolved` is every host the folder can reach " +
				"(its own rules plus those inherited from ancestors and the instance base); `own` is only the rules set on this folder itself.",
		},
		MCPArgs: map[resreg.Action][]resreg.MCPArg{
			networkActionAllow: {{Name: "host", Type: "string", Required: true}, {Name: "folder", Type: "string"}},
			networkActionDeny:  {{Name: "host", Type: "string", Required: true}, {Name: "folder", Type: "string"}},
		},
		MCPNames: networkMCPNames,
		Authz:    func(resreg.Caller, resreg.Action, resreg.Args) (string, map[string]string, error) { return "", nil, nil },
		Handler:  s.networkRulesHandler,
		Store:    store.New(s.db.SQL()),
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
func (s *Server) networkRulesPostBuild(folder, callerSub string, rules []string) func(*mcpserver.MCPServer) {
	res := s.networkRulesResource()
	res.Gate = func(x resreg.Execution, _ string, _ map[string]string) error {
		name := networkMCPNames[x.Action]
		if !grantslib.CheckAction(rules, name, nil) {
			return resreg.Errorf(http.StatusForbidden, "%s: not permitted", name)
		}
		// Structural tier gate (web_routes had none): tier 2+ can't manage egress,
		// tier 1 is confined to its own subtree, tier 0 is unrestricted. Containment
		// is checked on the TARGET (arg) folder — resolving the caller's tier from
		// the SOCKET folder — so a tier-0/1 caller may open egress for any folder in
		// its subtree while a tier-2 stays denied. A hard cap operator grants can't
		// widen. Exactly the deleted ipc body's authzStructural(name, TargetFolder:
		// argFolder). db.Authorize below keeps the socket folder as its scope, as the
		// old authorizeCall did.
		target := networkTargetFolder(x.Args, folder)
		if err := auth.AuthorizeStructural(auth.Resolve(folder), name, auth.AuthzTarget{TargetFolder: target}); err != nil {
			return resreg.Errorf(http.StatusForbidden, "%s: %v", name, err)
		}
		if callerSub != "" && !s.db.Authorize(callerSub, folder, "mcp:"+name, nil) {
			return resreg.Errorf(http.StatusForbidden, "%s: not permitted", name)
		}
		return nil
	}
	callerFor := func(context.Context, mcp.CallToolRequest) (resreg.Caller, error) {
		return resreg.Caller{Sub: callerSub, Folder: folder}, nil
	}
	visible := func(name string) bool { return len(grantslib.MatchingRules(rules, name)) > 0 }
	return func(srv *mcpserver.MCPServer) {
		resreg.MCPTools(srv, res, callerFor, visible)
	}
}

// addNetworkRuleTx appends one egress allowlist row on tx (mirrors
// DB.AddNetworkRule so the mutation lands in resreg.invoke's tx alongside its
// audit_log row). INSERT OR IGNORE keeps the append idempotent.
func addNetworkRuleTx(ctx context.Context, tx *sql.Tx, folder, target, by string) error {
	_, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO network_rules (folder, target, created_at, created_by) VALUES (?, ?, ?, ?)`,
		folder, target, time.Now().UTC().Format(time.RFC3339), by)
	return err
}

// removeNetworkRuleTx drops one egress allowlist row on tx (mirrors
// DB.RemoveNetworkRule). No error if the row is absent.
func removeNetworkRuleTx(ctx context.Context, tx *sql.Tx, folder, target string) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM network_rules WHERE folder = ? AND target = ?`, folder, target)
	return err
}

// validHostname accepts a bare host: letters, digits, '.', '-', ':' (host:port),
// length-bounded. Moved here from ipc when the network tools migrated to resreg
// (network_allow was its only caller). groupfolder keeps its own mirror.
func validHostname(h string) bool {
	if h == "" || len(h) > 253 {
		return false
	}
	for _, r := range h {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '.' || r == '-' || r == ':':
		default:
			return false
		}
	}
	return true
}

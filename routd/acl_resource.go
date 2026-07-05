package routd

// acl_resource.go — 5/44 agent-face migration of the acl tools
// (add_acl/remove_acl/list_acl) onto one resreg.Resource + the injected Gate.
// resreg owns handler dispatch + the mutation's tx + its audit_log row; routd
// owns the auth POLICY (spec 5/44). Three semantics preserved from the deleted
// ipc bodies + s.grantACL/revokeACL:
//
//   - scope-containment (the stricter MCP gate): a caller may only grant/revoke
//     within its own authority — the Gate runs auth.AuthorizeStructural on the
//     `scope` arg (add/remove) or `folder` arg (list). scope "**" (operator
//     role) is thus tier-0-only by containment, exactly as before.
//   - the scope=="**" overload: grant/revoke write an acl_membership operator
//     edge, not an acl row. Done tx-aware here (calling s.grantACL would open
//     its own BeginTx and DEADLOCK inside invoke's tx) while PRESERVING the
//     recursive cycle check store.AddMembership does.
//   - list_acl is tier 0-1 only (Visible predicate) and returns only rows whose
//     scope == the queried folder.
//
// The REST face (/v1/acl POST + body-DELETE, routd/server.go) is untouched —
// agent-face only; unifying its scope model is a separate 5/44 step.

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/kronael/arizuko/auth"
	grantslib "github.com/kronael/arizuko/grants"
	"github.com/kronael/arizuko/resreg"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	"github.com/kronael/arizuko/store"
)

// aclMCPNames maps the resreg actions to the flat live tool names so the
// in-container agent's tools are not renamed.
var aclMCPNames = map[resreg.Action]string{
	resreg.Action("add"):    "add_acl",
	resreg.Action("remove"): "remove_acl",
	resreg.ActionList:       "list_acl",
}

// webRoutesResource sibling: the acl agent-face Resource. No RowType — args are
// the exact wire shapes (principal/scope/action/effect; folder for list).
func (s *Server) aclResource() resreg.Resource {
	arg := func(req ...resreg.MCPArg) []resreg.MCPArg { return req }
	return resreg.Resource{
		Name: "acl",
		Endpoints: []resreg.Endpoint{
			{Verb: "POST", Path: "/v1/acl", Action: resreg.Action("add")},
			{Verb: "DELETE", Path: "/v1/acl", Action: resreg.Action("remove")},
			{Verb: "GET", Path: "/v1/acl", Action: resreg.ActionList},
		},
		MCPDoc: map[resreg.Action]string{
			resreg.Action("add"):    "Grant a principal access to a folder scope (an acl row); scope '**' grants the operator role. You can only grant within your own authority. Defaults action=admin, effect=allow.",
			resreg.Action("remove"): "Revoke a principal's access to a folder scope (drop an acl row); scope '**' revokes the operator role. You can only revoke within your own authority.",
			resreg.ActionList:       "List acl rows for a folder (scope matches the folder). Audit what's permitted before changing. Tier 0-1 only.",
		},
		MCPArgs: map[resreg.Action][]resreg.MCPArg{
			resreg.Action("add"): arg(
				resreg.MCPArg{Name: "principal", Type: "string", Required: true},
				resreg.MCPArg{Name: "scope", Type: "string", Required: true},
				resreg.MCPArg{Name: "action", Type: "string"},
				resreg.MCPArg{Name: "effect", Type: "string"},
			),
			resreg.Action("remove"): arg(
				resreg.MCPArg{Name: "principal", Type: "string", Required: true},
				resreg.MCPArg{Name: "scope", Type: "string", Required: true},
				resreg.MCPArg{Name: "action", Type: "string"},
				resreg.MCPArg{Name: "effect", Type: "string"},
			),
			resreg.ActionList: arg(
				resreg.MCPArg{Name: "folder", Type: "string", Required: true},
			),
		},
		MCPNames: aclMCPNames,
		// Authz derives (scope, params) for the gate; acl's containment is done
		// in the injected Gate against the scope arg, so this is a no-op.
		Authz: func(resreg.Caller, resreg.Action, resreg.Args) (string, map[string]string, error) {
			return "", nil, nil
		},
		Handler: s.aclHandler,
		Store:   store.New(s.db.SQL()),
	}
}

func (s *Server) aclHandler(ctx context.Context, x resreg.Execution) (any, error) {
	switch x.Action {
	case resreg.ActionList:
		gf := argString(x.Args, "folder")
		if gf == "" {
			return nil, resreg.Errorf(http.StatusBadRequest, "folder required")
		}
		rows := s.db.ListACL("")
		out := make([]map[string]any, 0, len(rows))
		for _, r := range rows {
			if r.Scope != gf {
				continue
			}
			out = append(out, map[string]any{
				"principal": r.Principal, "action": r.Action,
				"scope": r.Scope, "effect": r.Effect,
				"params": r.Params, "predicate": r.Predicate,
			})
		}
		return map[string]any{"folder": gf, "acl": out}, nil

	case resreg.Action("add"):
		principal := argString(x.Args, "principal")
		scope := argString(x.Args, "scope")
		if principal == "" || scope == "" {
			return nil, resreg.Errorf(http.StatusBadRequest, "principal and scope required")
		}
		if err := grantACLTx(ctx, x.Tx, principal, scope,
			argString(x.Args, "action"), argString(x.Args, "effect"), "agent:"+x.Caller.Folder); err != nil {
			return nil, resreg.Errorf(http.StatusConflict, "%v", err)
		}
		return apiv1.OK{OK: true}, nil

	case resreg.Action("remove"):
		principal := argString(x.Args, "principal")
		scope := argString(x.Args, "scope")
		if principal == "" || scope == "" {
			return nil, resreg.Errorf(http.StatusBadRequest, "principal and scope required")
		}
		if err := revokeACLTx(ctx, x.Tx, principal, scope,
			argString(x.Args, "action"), argString(x.Args, "effect")); err != nil {
			return nil, resreg.Errorf(http.StatusConflict, "%v", err)
		}
		return apiv1.OK{OK: true}, nil
	}
	return nil, resreg.Errorf(http.StatusBadRequest, "unknown action %q", x.Action)
}

// aclPostBuild mounts the acl tools on the agent socket with the tier-aware Gate
// (CheckAction + db.Authorize(mcp:) + AuthorizeStructural scope-containment) and
// the Visible predicate (MatchingRules + list_acl tier<=1).
func (s *Server) aclPostBuild(folder, callerSub string, rules []string) func(*mcpserver.MCPServer) {
	res := s.aclResource()
	res.Gate = func(x resreg.Execution, _ string, _ map[string]string) error {
		name := aclMCPNames[x.Action]
		if !grantslib.CheckAction(rules, name, nil) {
			return resreg.Errorf(http.StatusForbidden, "%s: not permitted", name)
		}
		if callerSub != "" && !s.db.Authorize(callerSub, folder, "mcp:"+name, nil) {
			return resreg.Errorf(http.StatusForbidden, "%s: not permitted", name)
		}
		// Scope-containment: the caller must have authority over the target it
		// grants/revokes (add/remove) or lists. "**" requires tier-0 by design.
		var target string
		switch x.Action {
		case resreg.Action("add"), resreg.Action("remove"):
			target = argString(x.Args, "scope")
		case resreg.ActionList:
			target = argString(x.Args, "folder")
		}
		if err := auth.AuthorizeStructural(auth.Resolve(folder), name, auth.AuthzTarget{TargetFolder: target}); err != nil {
			return resreg.Errorf(http.StatusForbidden, "%v", err)
		}
		return nil
	}
	callerFor := func(context.Context, mcp.CallToolRequest) (resreg.Caller, error) {
		return resreg.Caller{Sub: callerSub, Folder: folder}, nil
	}
	// list_acl is tier 0-1 only (mirrors the old `if identity.Tier <= 1`
	// registration); other tools follow the socket's grant rules.
	visible := func(name string) bool {
		if name == "list_acl" && auth.Resolve(folder).Tier > 1 {
			return false
		}
		return len(grantslib.MatchingRules(rules, name)) > 0
	}
	return func(srv *mcpserver.MCPServer) {
		resreg.MCPTools(srv, res, callerFor, visible)
	}
}

// grantACLTx mirrors s.grantACL but writes via tx so the mutation + invoke's
// audit row commit as one unit. scope "**" → operator-role membership (with the
// same recursive cycle check store.AddMembership runs); else one acl row.
func grantACLTx(ctx context.Context, tx *sql.Tx, principal, scope, action, effect, grantedBy string) error {
	if grantedBy == "" {
		grantedBy = "routd"
	}
	if scope == "**" {
		return addMembershipTx(ctx, tx, principal, "role:operator", grantedBy)
	}
	if action == "" {
		action = "admin"
	}
	if effect == "" {
		effect = "allow"
	}
	_, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO acl (principal, action, scope, effect, params, predicate, granted_by, granted_at)
		 VALUES (?, ?, ?, ?, '', '', ?, ?)`,
		principal, action, scope, effect, grantedBy, nowTS())
	return err
}

// revokeACLTx mirrors s.revokeACL on tx.
func revokeACLTx(ctx context.Context, tx *sql.Tx, principal, scope, action, effect string) error {
	if scope == "**" {
		_, err := tx.ExecContext(ctx,
			`DELETE FROM acl_membership WHERE child=? AND parent=?`, principal, "role:operator")
		return err
	}
	if action == "" {
		action = "admin"
	}
	if effect == "" {
		effect = "allow"
	}
	_, err := tx.ExecContext(ctx,
		`DELETE FROM acl WHERE principal=? AND action=? AND scope=? AND effect=? AND params='' AND predicate=''`,
		principal, action, scope, effect)
	return err
}

// addMembershipTx inserts (child → parent) on tx, PRESERVING store.AddMembership's
// self + cycle rejection. Runs the recursive reachability walk on the same tx so
// the check + insert are atomic; no audit row (invoke writes the one audit row).
func addMembershipTx(ctx context.Context, tx *sql.Tx, child, parent, addedBy string) error {
	if child == parent {
		return store.ErrSelfMembership
	}
	var hits int
	if err := tx.QueryRowContext(ctx,
		`WITH RECURSIVE up(p) AS (
		   SELECT ? UNION
		   SELECT acl_membership.parent FROM acl_membership JOIN up ON acl_membership.child = up.p
		 ) SELECT COUNT(*) FROM up WHERE p = ?`,
		parent, child).Scan(&hits); err != nil {
		return err
	}
	if hits > 0 {
		return store.ErrCycle
	}
	var by any
	if addedBy != "" {
		by = addedBy
	}
	_, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO acl_membership (child, parent, added_by, added_at) VALUES (?, ?, ?, ?)`,
		child, parent, by, time.Now().Format(time.RFC3339))
	return err
}

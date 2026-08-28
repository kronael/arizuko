package routd

// acl_resource.go — the 5/16 agent-face migration of the acl tools onto one
// resreg.Resource plus the injected Gate. resreg owns handler dispatch, the
// mutation's tx and its audit_log row; routd owns the auth POLICY. Three semantics
// the shared handler preserves:
//
//   - scope-containment: a caller may only grant/revoke within its own authority —
//     the Gate runs the 5/33 single evaluator on the `scope` arg, then
//     auth.Delegate (subset-of-held) for a non-root add. "**" is thus reachable
//     only from an operator/root grant.
//   - the operator-role overload: scope "**" with the action OMITTED writes an
//     acl_membership operator edge, not an acl row. Any action the caller names at
//     "**" is an ordinary row — matching on scope alone discarded it and
//     over-granted. Done tx-aware here (s.db.AddMembership would open its own
//     BeginTx and DEADLOCK inside invoke's tx) while preserving the cycle check.
//   - list_acl is advertised only to a caller holding mcp:list_acl and returns only
//     rows whose scope == the queried folder.
//
// The REST face rides the SAME shared handler via mountACL. list_acl has NO REST
// twin — it is MCPOnly, which keeps it out of both the mux and the doc. The REST
// Gate binds the body `scope` to the caller's JWT folder, closing the pre-fold hole
// where the handlers gated on the acl:write bearer scope ALONE.

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"strings"
	"time"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	"github.com/kronael/arizuko/store"
)

// aclMCPNames is the action→flat-tool-name map, single-sourced in resreg/
// resources (ACLMCPNames). Aliased here for the Gate's action→policy-name lookup.
var aclMCPNames = resources.ACLMCPNames

// webRoutesResource sibling: the acl agent-face Resource. No RowType — args are
// the exact wire shapes (principal/scope/action/effect; folder for list),
// single-sourced in resreg/resources.
func (s *Server) aclResource() resreg.Resource {
	r := resreg.Mount(resources.ACLName)
	// Authz derives (scope, params) for the gate; acl's containment is done
	// in the injected Gate against the scope arg, so this is a no-op.
	r.Authz = func(resreg.Caller, resreg.Action, resreg.Args) (string, map[string]string, error) {
		return "", nil, nil
	}
	r.Handler = s.aclHandler
	r.Store = store.New(s.db.SQL())
	return r
}

// mountACL wires the /v1/acl REST face onto the SAME shared aclHandler the agent's
// MCP tools use. It mounts resources.ACLEndpoints verbatim. NEVER reintroduce an
// inline Endpoints literal here: the trimmed copy that used to live in this
// function is what left GET /v1/acl advertised and unmounted (BUGS F27);
// endpoints_source_test.go probes this mux to catch it.
func (s *Server) mountACL(mux *http.ServeMux) {
	res := s.aclResource()
	res.Gate = s.aclRESTGate
	resreg.RegisterREST(mux, res, s.aclRESTCaller)
}

// aclRESTCaller builds the REST Caller for the shared acl handler: identity via
// the Verifier, Caller.Folder = the caller's JWT folder (the handler stamps grant
// provenance from it; aclRESTGate binds the body scope to it). Held scopes ride in
// Claims for the Gate. A nil Verifier is open (local-dev), mirroring s.authz.
func (s *Server) aclRESTCaller(r *http.Request) (resreg.Caller, error) {
	var sub, folder string
	var scope []string
	if s.verify != nil {
		var err error
		sub, scope, folder, err = s.verify.Verify(r)
		if err != nil {
			return resreg.Caller{}, err
		}
	}
	return resreg.Caller{
		Sub:    sub,
		Folder: folder,
		Claims: map[string]string{"scopes": strings.Join(scope, " ")},
	}, nil
}

// aclRESTGate is the operator/human REST twin of aclPostBuild's Gate: the acl:write
// bearer scope, then scope-containment — ownsFolder on the body `scope` against the
// caller's JWT folder. This binds the granted/revoked scope to the caller's authority
// (scope "**" needs the empty-folder root token), closing the pre-fold REST hole where
// handleACLAdd/Remove gated on acl:write ALONE and never bound the body scope. A nil
// Verifier is open (mirrors s.authz).
func (s *Server) aclRESTGate(x resreg.Execution, _ string, _ map[string]string) error {
	if s.verify == nil {
		return nil
	}
	if !hasAnyScope(strings.Fields(x.Caller.Claims["scopes"]), []string{"acl:write"}) {
		return resreg.Errorf(http.StatusForbidden, "missing scope acl:write")
	}
	// Bind the granted/revoked scope to the caller's authority: an operator may only
	// grant within its own subtree; scope "**" needs the root (empty-folder) token.
	// Mirrors the routes/tasks REST containment (ownsFolder), self-or-descendant.
	if !ownsFolder(x.Caller.Folder, argString(x.Args, "scope")) {
		return resreg.Errorf(http.StatusForbidden, "scope outside caller subtree")
	}
	return nil
}

func (s *Server) aclHandler(ctx context.Context, x resreg.Execution) (any, error) {
	switch x.Action {
	case resreg.ActionList:
		gf := argString(x.Args, "folder")
		if gf == "" {
			return nil, resreg.Errorf(http.StatusBadRequest, "folder required")
		}
		rows, err := s.db.ListACL("")
		if err != nil {
			// A 200 with an empty `acl` array would read as "this folder has no
			// grants" — the exact confusion A4 is about. Say the read failed.
			return nil, resreg.Errorf(http.StatusInternalServerError, "acl read failed: %v", err)
		}
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
		warning := s.nonCanonicalWarning(principal)
		if err := grantACLTx(ctx, x.Tx, principal, scope,
			argString(x.Args, "action"), argString(x.Args, "effect"), "agent:"+x.Caller.Folder); err != nil {
			return nil, resreg.Errorf(http.StatusConflict, "%v", err)
		}
		return aclAddResult{OK: true, Warning: warning}, nil

	case resreg.Action("remove"):
		principal := argString(x.Args, "principal")
		scope := argString(x.Args, "scope")
		if principal == "" || scope == "" {
			return nil, resreg.Errorf(http.StatusBadRequest, "principal and scope required")
		}
		n, err := revokeACLTx(ctx, x.Tx, principal, scope,
			argString(x.Args, "action"), argString(x.Args, "effect"))
		if err != nil {
			return nil, resreg.Errorf(http.StatusConflict, "%v", err)
		}
		if n == 0 {
			return nil, resreg.Errorf(http.StatusNotFound,
				"no matching grant for principal %q scope %q — this surface addresses only un-narrowed rows; "+
					"a grant carrying params or a predicate must be revoked where it was created",
				principal, scope)
		}
		return apiv1.OK{OK: true}, nil
	}
	return nil, resreg.Errorf(http.StatusBadRequest, "unknown action %q", x.Action)
}

// aclPostBuild mounts the acl tools on the agent socket with the Gate
// (db.Authorize(mcp:) on the target scope + auth.Delegate subset-of-held on add)
// and the turn's visibility view.
func (s *Server) aclPostBuild(folder, callerSub string, authorize authorizeFn, visible func(string) bool, callerID auth.Identity) func(*mcpserver.MCPServer) {
	res := s.aclResource()
	res.Gate = func(x resreg.Execution, _ string, _ map[string]string) error {
		name := aclMCPNames[x.Action]
		// The target the caller grants/revokes (add/remove) or lists. "**" is covered
		// only by an operator/root grant.
		var target string
		switch x.Action {
		case resreg.Action("add"), resreg.Action("remove"):
			target = argString(x.Args, "scope")
		case resreg.ActionList:
			target = argString(x.Args, "folder")
		}
		// One evaluator: the caller must hold the acl tool scoped to cover the target.
		if !authorize(callerSub, target, "mcp:"+name, nil) {
			return resreg.Errorf(http.StatusForbidden, "%s on %s: not permitted", name, target)
		}
		// 5/33 lineage delegation: a NON-root writer may only grant a row it HOLDS with
		// the grant option (auth.Delegate — subset-of-held). Root delegates anything.
		// This is what makes a group unable to hand out authority it wasn't delegated.
		if x.Action == resreg.Action("add") && !callerID.IsRoot {
			act := argString(x.Args, "action")
			if act == "" {
				act = "admin" // grantACLTx default
			}
			want := core.ACLRow{
				Principal: argString(x.Args, "principal"),
				Action:    act,
				Scope:     target,
				Effect:    "allow",
			}
			if err := auth.Delegate(store.New(s.db.SQL()), callerSub, []core.ACLRow{want}); err != nil {
				return resreg.Errorf(http.StatusForbidden, "%v", err)
			}
		}
		return nil
	}
	return mountAgentResource(res, callerSub, folder, visible)
}

// aclAddResult is add_acl's reply: the old `{"ok":true}` plus an optional
// warning. `omitempty` keeps the wire shape identical when there is nothing to
// say, so the common case is unchanged for every existing caller.
type aclAddResult struct {
	OK      bool   `json:"ok"`
	Warning string `json:"warning,omitempty"`
}

// nonCanonicalWarning reports whether `principal` is a KNOWN alias of some other
// account, and returns the operator-facing warning if so (BUGS N2). 5/32 resolves a
// linked OAuth login to the canonical sub at MINT, and grants key on the exact
// string, so a grant written against a linked alias creates a row that can NEVER
// match — it looks applied and never fires. authd owns the answer; routd federates
// it over HTTP.
//
// FAIL-OPEN, deliberately. Refusing on authd's answer would put a hard dependency
// on authd into a write path that has none. But fail-open with a log only is a
// guard nobody sees, so the warning rides back on the RESPONSE. It never rewrites
// the principal — moving a grant the operator did not ask to move is the failure
// mode N1 chose an immutable canonical sub to avoid.
func (s *Server) nonCanonicalWarning(principal string) string {
	if s.identity == nil {
		return ""
	}
	id, _, claimed, err := s.identity.Resolve(principal)
	if err != nil {
		// A guard that stops working exactly when identity data is unavailable is
		// worth less than it reads, so say so rather than implying the principal
		// checked out.
		slog.Warn("add_acl: could not check principal canonicality", "principal", principal, "err", err)
		return "could not verify that " + principal + " is a canonical sub (authd did not answer): " +
			"the grant was written, but if this is a linked login it will never match a token"
	}
	if !claimed || id.ID == "" || id.ID == principal {
		return ""
	}
	return "granted to " + principal + ", which is a linked alias of " + id.ID +
		": tokens for this account always carry " + id.ID + ", so this row will never match. " +
		"Re-grant to " + id.ID + " and revoke this one"
}

// grantACLTx grants via tx so the mutation + invoke's audit row commit as one
// unit. The operator-role shortcut fires only for the exact shape role:operator
// models — (*, **, allow); every other action at scope "**" is an ordinary
// wildcard-scope acl row. Matching on scope alone discarded the requested
// action, so `add_acl(action="read", scope="**")` minted full operator
// membership, and a caller holding only (admin, **, grant_option=1) could pass
// auth.Delegate and then write a strictly stronger right than it held.
// action/effect default to admin/allow (the grant shape).
func grantACLTx(ctx context.Context, tx *sql.Tx, principal, scope, action, effect, grantedBy string) error {
	if grantedBy == "" {
		grantedBy = "routd"
	}
	// Tested on the RAW action, before the admin default: an omitted action at
	// scope "**" is the operator-grant shape (/root grant, the REST twin), and
	// "*" says the same thing explicitly. An action the caller actually named is
	// honoured as an ordinary row instead of being discarded.
	if scope == "**" && (action == "" || action == "*") && (effect == "" || effect == "allow") {
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

// revokeACLTx is the remove_acl/DELETE twin of grantACLTx. It reports how many rows
// it removed, and the caller MUST refuse when that is zero: a revoke is the one
// operation where "did nothing" must never read as success. This face matches
// `params='' AND predicate=''`, so a row an operator created WITH a params
// narrowing is invisible to it — it used to delete nothing and answer {"ok":true}
// (BUGS L79). Widening the face is a separate, additive change.
func revokeACLTx(ctx context.Context, tx *sql.Tx, principal, scope, action, effect string) (int64, error) {
	// Mirror of the grant guard, same raw-action test: keying on scope alone let
	// a narrow-row revoke strip an operator membership granted separately.
	if scope == "**" && (action == "" || action == "*") && (effect == "" || effect == "allow") {
		res, err := tx.ExecContext(ctx,
			`DELETE FROM acl_membership WHERE child=? AND parent=?`, principal, "role:operator")
		if err != nil {
			return 0, err
		}
		n, _ := res.RowsAffected()
		return n, nil
	}
	if action == "" {
		action = "admin"
	}
	if effect == "" {
		effect = "allow"
	}
	res, err := tx.ExecContext(ctx,
		`DELETE FROM acl WHERE principal=? AND action=? AND scope=? AND effect=? AND params='' AND predicate=''`,
		principal, action, scope, effect)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
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

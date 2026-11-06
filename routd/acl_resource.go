package routd

// The shared ACL handler checks each mutation and proves the policy version.

import (
	"context"
	"log/slog"
	"net/http"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	"github.com/kronael/arizuko/store"
)

// aclMCPNames is the action→flat-tool-name map, single-sourced in
// resreg/resources; the Gate's action→policy-name lookup.
var aclMCPNames = resources.ACLMCPNames

// aclResource is the acl agent-face Resource. No RowType: args are the exact
// wire shapes, single-sourced in resreg/resources.
type grantCheck func(resreg.Execution, auth.Mutation) (objstore.Version, error)

func (s *Server) aclResource(check grantCheck) resreg.Resource {
	r := resreg.Mount(resources.ACLName)
	r.Handler = func(ctx context.Context, x resreg.Execution) (any, error) { return s.aclHandler(ctx, x, check) }
	r.Store = s.db.store()
	return r
}

// mountACL wires /v1/acl onto the SAME aclHandler the MCP tools use, mounting
// resources.ACLEndpoints verbatim. NEVER an inline Endpoints literal here: a
// trimmed copy left GET /v1/acl advertised and unmounted (BUGS F27);
// endpoints_source_test.go probes this mux to catch it.
func (s *Server) mountACL(mux *http.ServeMux) {
	res := s.aclResource(func(x resreg.Execution, m auth.Mutation) (objstore.Version, error) {
		return auth.CheckGrant(s.db.store(), auth.Caller{Principal: x.Caller.Sub}, auth.Ask{}, m)
	})
	res.Gate = s.aclRESTGate
	resreg.RegisterREST(mux, res, s.restCaller(nil))
}

// aclRESTGate is the REST twin of aclPostBuild's Gate: the acl:write bearer
// scope, then ownsFolder on the body `scope` against the caller's JWT folder
// (scope "**" needs the empty-folder root token). A nil Verifier is open.
func (s *Server) aclRESTGate(x resreg.Execution, _ string, _ map[string]string) error {
	if s.verify == nil {
		return nil
	}
	if !hasAnyScope(x.Caller.Scopes(), []string{"acl:write"}) {
		return resreg.Errorf(http.StatusForbidden, "missing scope acl:write")
	}
	if !ownsFolder(x.Caller.Folder, argString(x.Args, "scope")) {
		return resreg.Errorf(http.StatusForbidden, "scope outside caller subtree")
	}
	return nil
}

func (s *Server) aclHandler(ctx context.Context, x resreg.Execution, check grantCheck) (any, error) {
	switch x.Action {
	case resreg.ActionList:
		gf := argString(x.Args, "folder")
		if gf == "" {
			return nil, resreg.Errorf(http.StatusBadRequest, "folder required")
		}
		rows, err := s.db.ListACL("")
		if err != nil {
			// A 200 with an empty `acl` would read as "no grants" (BUGS A4).
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
				"grant_option": r.GrantOption,
			})
		}
		return map[string]any{"folder": gf, "acl": out}, nil

	case resreg.Action("add"):
		row := aclArgsRow(x.Args)
		if row.Principal == "" || row.Scope == "" {
			return nil, resreg.Errorf(http.StatusBadRequest, "principal and scope required")
		}
		// Only ADD validates the narrowing: a remove must still address a row
		// written before this check existed.
		if err := store.ValidateACLAction(store.GrantDefaults(row).Action); err != nil {
			return nil, resreg.Errorf(http.StatusBadRequest, "%v", err)
		}
		if err := auth.ValidateParams(row.Params); err != nil {
			return nil, resreg.Errorf(http.StatusBadRequest, "%v", err)
		}
		if err := proveACL(x, check, row, false); err != nil {
			return nil, err
		}
		warning := s.nonCanonicalWarning(row.Principal)
		row.GrantedBy = "agent:" + x.Caller.Folder
		if err := s.db.grantACLInto(ctx, x.Writes, row); err != nil {
			return nil, resreg.Errorf(http.StatusConflict, "%v", err)
		}
		return aclAddResult{OK: true, Warning: warning}, nil

	case resreg.Action("remove"):
		row := aclArgsRow(x.Args)
		if row.Principal == "" || row.Scope == "" {
			return nil, resreg.Errorf(http.StatusBadRequest, "principal and scope required")
		}
		if err := proveACL(x, check, row, true); err != nil {
			return nil, err
		}
		// The delete conditions on the row being there, so its refusal IS the
		// not-found answer, reached from the batch rather than from this call.
		notFound := resreg.Errorf(http.StatusNotFound,
			"no matching grant for principal %q scope %q params %q — params must match the row EXACTLY; "+
				"a grant carrying a predicate must be revoked where it was created",
			row.Principal, row.Scope, row.Params)
		if err := s.db.revokeACLInto(x.Writes, row, notFound); err != nil {
			return nil, resreg.Errorf(http.StatusConflict, "%v", err)
		}
		return apiv1.OK{OK: true}, nil
	}
	return nil, resreg.Errorf(http.StatusBadRequest, "unknown action %q", x.Action)
}

func proveACL(x resreg.Execution, check grantCheck, row core.ACLRow, remove bool) error {
	m := auth.Mutation{Remove: remove, Row: store.GrantDefaults(row)}
	if store.OperatorShape(row) {
		m.Row = core.ACLRow{Principal: row.Principal}
		m.Parent = store.RoleOperator
	}
	v, err := check(x, m)
	if err != nil {
		return resreg.Errorf(http.StatusForbidden, "%v", err)
	}
	x.Writes.Prove(v)
	return nil
}

// aclPostBuild mounts the ACL tools with the turn’s policy and visibility.
func (s *Server) aclPostBuild(folder, callerSub string, authorize authorizeFn, visible func(string) bool, t turnMCP) func(*mcpserver.MCPServer) {
	res := s.aclResource(func(x resreg.Execution, m auth.Mutation) (objstore.Version, error) {
		return auth.CheckGrant(s.db.store(), turnGranter(t), auth.Ask{Action: "mcp:" + aclMCPNames[x.Action], Scope: argString(x.Args, "scope")}, m)
	})
	res.Gate = func(x resreg.Execution, _ string, _ map[string]string) error {
		if x.Action == resreg.ActionList && !authorize(argString(x.Args, "folder"), "mcp:"+aclMCPNames[x.Action], nil) {
			return resreg.Errorf(http.StatusForbidden, "list_acl: not permitted")
		}
		return nil
	}
	return mountAgentResource(res, callerSub, folder, visible)
}

// aclAddResult is add_acl's reply; `omitempty` keeps the wire shape identical
// when there is no warning.
type aclAddResult struct {
	OK      bool   `json:"ok"`
	Warning string `json:"warning,omitempty"`
}

// nonCanonicalWarning binds this handler's resolver to NonCanonicalWarning.
func (s *Server) nonCanonicalWarning(principal string) string {
	return NonCanonicalWarning(s.identity, principal)
}

// NonCanonicalWarning reports whether `principal` is a KNOWN alias of another
// account and returns the operator-facing warning (BUGS N2): 5/32-acl-unified resolves a
// linked login to the canonical sub at MINT and grants key on the exact
// string, so a grant against an alias can NEVER match. It is the ONE renderer
// for every surface that writes an acl row — dashd writes grants straight to
// routd's store (BUGS L152). FAIL-OPEN, deliberately: a hard dependency on
// authd does not belong in this write path, so the warning rides the RESPONSE.
// It never rewrites the principal.
func NonCanonicalWarning(ident IdentityResolver, principal string) string {
	if ident == nil {
		return ""
	}
	id, _, claimed, err := ident.Resolve(principal)
	if err != nil {
		// A guard that stops working when identity data is unavailable is
		// worth less than it reads, so say so.
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

// aclArgsRow reads the add/remove wire arguments with NO defaults applied —
// OperatorShape tests the raw row, where an omitted action is the signal.
// One builder for the Gate and the handler. There is no `predicate` argument:
// this face writes rules over call ARGUMENTS, and revokeACLInto pins
// `predicate=""` to match.
func aclArgsRow(args resreg.Args) core.ACLRow {
	return core.ACLRow{
		Principal:   argString(args, "principal"),
		Action:      argString(args, "action"),
		Scope:       argString(args, "scope"),
		Effect:      argString(args, "effect"),
		Params:      argString(args, "params"),
		GrantOption: args.Bool("grant_option"),
	}
}

// grantACLInto buffers the grant and its audit row in one batch.
func (d *DB) grantACLInto(ctx context.Context, ws *store.WriteSet, row core.ACLRow) error {
	grantedBy := row.GrantedBy
	if grantedBy == "" {
		grantedBy = "routd"
	}
	if store.OperatorShape(row) {
		// The edge is the operator role itself, so the cycle walk runs here too
		// (store.PutMembershipInto).
		return d.store().PutMembershipInto(ctx, ws, row.Principal, "role:operator", grantedBy)
	}
	row = store.GrantDefaults(row)
	row.GrantedBy = grantedBy
	return d.store().PutACLRowInto(ws, row)
}

// revokeACLInto is the remove_acl/DELETE twin of grantACLInto. Its write is
// CONDITIONED on the row being there: a revoke that removed nothing must never
// read as success. It matches `params` exactly, the empty string included, so
// a row this surface can write is a row it can remove (BUGS L79);
// `predicate=""` stays hardcoded because this face has no predicate argument.
func (d *DB) revokeACLInto(ws *store.WriteSet, row core.ACLRow, refused error) error {
	// Same raw-row test as the grant guard: keying on scope alone let a
	// narrow-row revoke strip an operator membership granted separately.
	if store.OperatorShape(row) {
		return d.store().RemoveMembershipInto(ws, row.Principal, "role:operator", refused)
	}
	// The six coordinate fields address exactly one grant.
	return d.store().RemoveACLRowInto(ws, store.GrantDefaults(row), refused)
}

package routd

// acl_resource.go — the acl tools on one resreg.Resource plus the injected
// Gate (spec 5/16); routd owns the auth POLICY. Three semantics the shared
// handler preserves: scope-containment (the Gate runs the 5/33-paths-roles evaluator on
// the `scope` arg, then auth.Delegate subset-of-held for a non-root add, so
// "**" is reachable only from an operator/root grant); the operator-role
// overload (scope "**" with the action OMITTED writes an acl_membership edge,
// not an acl row — any named action at "**" is an ordinary row, and matching
// on scope alone over-granted); and list_acl, MCPOnly, returning only rows
// whose scope == the queried folder. The REST face rides the SAME handler
// and binds the body `scope` to the caller's JWT folder.

import (
	"context"
	"log/slog"
	"net/http"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
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
func (s *Server) aclResource() resreg.Resource {
	r := resreg.Mount(resources.ACLName)
	r.Handler = s.aclHandler
	r.Store = s.db.store()
	return r
}

// mountACL wires /v1/acl onto the SAME aclHandler the MCP tools use, mounting
// resources.ACLEndpoints verbatim. NEVER an inline Endpoints literal here: a
// trimmed copy left GET /v1/acl advertised and unmounted (BUGS F27);
// endpoints_source_test.go probes this mux to catch it.
func (s *Server) mountACL(mux *http.ServeMux) {
	res := s.aclResource()
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

func (s *Server) aclHandler(ctx context.Context, x resreg.Execution) (any, error) {
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
		if err := auth.ValidateParams(row.Params); err != nil {
			return nil, resreg.Errorf(http.StatusBadRequest, "%v", err)
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

// aclPostBuild mounts the acl tools on the agent socket with the Gate
// (db.Authorize(mcp:) on the target scope + auth.Delegate subset-of-held on
// add) and the turn's visibility view.
func (s *Server) aclPostBuild(folder, callerSub string, authorize authorizeFn, visible func(string) bool, callerID auth.Identity) func(*mcpserver.MCPServer) {
	res := s.aclResource()
	res.Gate = func(x resreg.Execution, _ string, _ map[string]string) error {
		name := aclMCPNames[x.Action]
		// "**" is covered only by an operator/root grant.
		var target string
		switch x.Action {
		case resreg.Action("add"), resreg.Action("remove"):
			target = argString(x.Args, "scope")
		case resreg.ActionList:
			target = argString(x.Args, "folder")
		}
		// One evaluator: the caller must hold the tool scoped to cover the target.
		if !authorize(target, "mcp:"+name, nil) {
			return resreg.Errorf(http.StatusForbidden, "%s on %s: not permitted", name, target)
		}
		// 5/33-paths-roles delegation: a NON-root writer may only grant a row it HOLDS with
		// the grant option (auth.Delegate). Root delegates anything.
		if x.Action == resreg.Action("add") && !callerID.IsRoot {
			// The candidate MUST be the row grantACLInto will WRITE — same builder,
			// same defaults. A hardcoded `allow` validated an allow row while the
			// write took the caller's effect (BUGS L93); the same drift on `params`
			// would let `mcp:t(repo=mine/*)` write the unnarrowed `mcp:t`.
			want := store.GrantDefaults(aclArgsRow(x.Args))
			if err := auth.Delegate(s.db.store(), callerSub, []core.ACLRow{want}); err != nil {
				return resreg.Errorf(http.StatusForbidden, "%v", err)
			}
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
// aclOperatorShape tests the raw row, where an omitted action is the signal.
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

// aclOperatorShape reports whether a row is the operator-role overload: scope
// "**", the action OMITTED (or "*"), no narrowing — tested on the RAW row.
// Matching on scope alone let `add_acl(action="read", scope="**")` mint full
// operator membership, and a shaped row at "**" is an ordinary wildcard row.
func aclOperatorShape(r core.ACLRow) bool {
	return r.Scope == "**" &&
		(r.Action == "" || r.Action == "*") &&
		(r.Effect == "" || r.Effect == "allow") &&
		r.Params == "" && r.Predicate == ""
}

// grantACLInto buffers the grant so the mutation + audit row ride one batch.
// It takes the ROW so the candidate the Gate hands auth.Delegate and the row
// this writes are one value. `params` is the `name=glob` narrowing (spec 5/32-acl-unified);
// `grant_option` is 5/33-paths-roles's WITH GRANT OPTION, kept honest by the Gate: a
// non-root writer passes auth.Delegate, whose grantableRows holds only rows the
// caller itself holds WITH the option.
func (d *DB) grantACLInto(ctx context.Context, ws *store.WriteSet, row core.ACLRow) error {
	grantedBy := row.GrantedBy
	if grantedBy == "" {
		grantedBy = "routd"
	}
	if aclOperatorShape(row) {
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
	if aclOperatorShape(row) {
		return d.store().RemoveMembershipInto(ws, row.Principal, "role:operator", refused)
	}
	// The six coordinate fields address exactly one grant.
	return d.store().RemoveACLRowInto(ws, store.GrantDefaults(row), refused)
}

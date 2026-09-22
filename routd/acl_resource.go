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
//     over-granted. Buffered here (s.db.AddMembership would send a batch of its
//     own and record the act twice) while preserving the cycle check.
//   - list_acl is advertised only to a caller holding mcp:list_acl and returns only
//     rows whose scope == the queried folder.
//
// The REST face rides the SAME shared handler via mountACL. list_acl has NO REST
// twin — it is MCPOnly, which keeps it out of both the mux and the doc. The REST
// Gate binds the body `scope` to the caller's JWT folder, closing the pre-fold hole
// where the handlers gated on the acl:write bearer scope ALONE.

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

// aclMCPNames is the action→flat-tool-name map, single-sourced in resreg/
// resources (ACLMCPNames). Aliased here for the Gate's action→policy-name lookup.
var aclMCPNames = resources.ACLMCPNames

// webRoutesResource sibling: the acl agent-face Resource. No RowType — args are
// the exact wire shapes (principal/scope/action/effect; folder for list),
// single-sourced in resreg/resources.
func (s *Server) aclResource() resreg.Resource {
	r := resreg.Mount(resources.ACLName)
	r.Handler = s.aclHandler
	r.Store = s.db.store()
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
	resreg.RegisterREST(mux, res, s.restCaller(nil))
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
	if !hasAnyScope(x.Caller.Scopes(), []string{"acl:write"}) {
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
				"grant_option": r.GrantOption,
			})
		}
		return map[string]any{"folder": gf, "acl": out}, nil

	case resreg.Action("add"):
		row := aclArgsRow(x.Args)
		if row.Principal == "" || row.Scope == "" {
			return nil, resreg.Errorf(http.StatusBadRequest, "principal and scope required")
		}
		// Only ADD validates the narrowing. A remove must still address a row
		// written before this check existed, or the fix makes a bad row permanent.
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
		// not-found answer — declared here because it reaches the caller from the
		// batch rather than from this call.
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
		if !authorize(target, "mcp:"+name, nil) {
			return resreg.Errorf(http.StatusForbidden, "%s on %s: not permitted", name, target)
		}
		// 5/33 lineage delegation: a NON-root writer may only grant a row it HOLDS with
		// the grant option (auth.Delegate — subset-of-held). Root delegates anything.
		// This is what makes a group unable to hand out authority it wasn't delegated.
		if x.Action == resreg.Action("add") && !callerID.IsRoot {
			// The candidate MUST be the row grantACLInto will WRITE — same builder,
			// same defaults. Hardcoding `allow` here validated an allow row while
			// the write took the caller's effect, so Delegate's deny ban never saw
			// a requested deny (BUGS L93); the same drift on `params` would let a
			// caller holding only `mcp:t(repo=mine/*)` write the unnarrowed
			// `mcp:t`, since auth.paramsCover reads an empty spec as no constraint.
			want := store.GrantDefaults(aclArgsRow(x.Args))
			if err := auth.Delegate(s.db.store(), callerSub, []core.ACLRow{want}); err != nil {
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

// nonCanonicalWarning binds this handler's resolver to NonCanonicalWarning.
func (s *Server) nonCanonicalWarning(principal string) string {
	return NonCanonicalWarning(s.identity, principal)
}

// NonCanonicalWarning reports whether `principal` is a KNOWN alias of some other
// account, and returns the operator-facing warning if so (BUGS N2). 5/32 resolves a
// linked OAuth login to the canonical sub at MINT, and grants key on the exact
// string, so a grant written against a linked alias creates a row that can NEVER
// match — it looks applied and never fires. authd owns the answer; routd federates
// it over HTTP.
//
// It is the ONE renderer of that warning, for every surface that writes an acl
// row. dashd writes grants straight to routd's store rather than through this
// handler, so without a shared renderer the web form either stayed silent or
// grew a second copy of these sentences to drift (BUGS L152).
//
// FAIL-OPEN, deliberately. Refusing on authd's answer would put a hard dependency
// on authd into a write path that has none. But fail-open with a log only is a
// guard nobody sees, so the warning rides back on the RESPONSE. It never rewrites
// the principal — moving a grant the operator did not ask to move is the failure
// mode N1 chose an immutable canonical sub to avoid.
func NonCanonicalWarning(ident IdentityResolver, principal string) string {
	if ident == nil {
		return ""
	}
	id, _, claimed, err := ident.Resolve(principal)
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

// aclArgsRow reads the acl add/remove wire arguments into the row they name,
// with NO defaults applied — aclOperatorShape tests the raw row, where an
// omitted action is the operator-role signal. One builder for the Gate and the
// handler: the row auth.Delegate judges is the row the write produces.
//
// There is no `predicate` argument. This face writes rules over call ARGUMENTS;
// a claim condition is a different question with a different vocabulary, and
// revokeACLInto pins `predicate=”` to match.
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

// aclOperatorShape reports whether a row is the operator-role overload rather
// than an ordinary acl row: scope "**", the action OMITTED (or spelled "*"),
// and no narrowing. Tested on the RAW row, BEFORE aclDefaults, because the
// omission is the signal.
//
// Matching on scope alone discarded the requested action, so
// `add_acl(action="read", scope="**")` minted full operator membership. A
// narrowing counts the same way: a shaped row at "**" is an ordinary
// wildcard-scope row, and reading it as membership would promote a rule that
// constrains a few arguments into a role that constrains none.
func aclOperatorShape(r core.ACLRow) bool {
	return r.Scope == "**" &&
		(r.Action == "" || r.Action == "*") &&
		(r.Effect == "" || r.Effect == "allow") &&
		r.Params == "" && r.Predicate == ""
}

// grantACLInto buffers the grant so the mutation + invoke's audit row ride one
// batch. It takes the ROW rather than a positional argument list, so the
// candidate the Gate hands auth.Delegate and the row this writes are one value.
//
// `params` is the `name=glob` narrowing (spec 5/32 § The row). It was INSERTed
// as an empty literal until 2026-08-30, so no shaped rule was expressible on
// this face at all — including the skip-approval row spec 6/26 Decision 5's
// `always` verdict writes.
//
// `grant_option` is 5/33's WITH GRANT OPTION. Omitting it from the INSERT left
// the column at its DEFAULT on both live grant surfaces, so re-delegation was
// reachable only through `resreg.Apply`. The Gate is what keeps it honest: a
// non-root writer passes auth.Delegate, whose grantableRows only ever holds
// rows the caller itself holds WITH the option.
func (d *DB) grantACLInto(ctx context.Context, ws *store.WriteSet, row core.ACLRow) error {
	grantedBy := row.GrantedBy
	if grantedBy == "" {
		grantedBy = "routd"
	}
	if aclOperatorShape(row) {
		// The edge is the operator role itself, so the walk that refuses a cycle
		// runs here too — store.PutMembershipInto is that walk plus the write.
		return d.store().PutMembershipInto(ctx, ws, row.Principal, "role:operator", grantedBy)
	}
	row = store.GrantDefaults(row)
	row.GrantedBy = grantedBy
	return d.store().PutACLRowInto(ws, row)
}

// revokeACLInto is the remove_acl/DELETE twin of grantACLInto. Its write is
// CONDITIONED on the row being there, and refused is what a revoke that removed
// nothing means: a revoke is the one operation where "did nothing" must never
// read as success.
//
// It matches `params` exactly, the empty string included, so revoking is the
// inverse of granting on this face: a row this surface can write is a row it can
// remove. The hardcoded `params=”` it replaced deleted nothing while answering
// {"ok":true} for any narrowed row (BUGS L79) — leaving it would have re-created
// that hole the day `params` became writable. `predicate=”` stays hardcoded:
// this face has no predicate argument to write one with.
func (d *DB) revokeACLInto(ws *store.WriteSet, row core.ACLRow, refused error) error {
	// Mirror of the grant guard, same raw-row test: keying on scope alone let a
	// narrow-row revoke strip an operator membership granted separately.
	if aclOperatorShape(row) {
		return d.store().RemoveMembershipInto(ws, row.Principal, "role:operator", refused)
	}
	// The six coordinate fields address exactly one grant, and `predicate` is the
	// empty string on every row this face can write. The delete conditions on the
	// item being there, so its refusal IS the "no matching grant" answer the
	// caller must not read as success.
	return d.store().RemoveACLRowInto(ws, store.GrantDefaults(row), refused)
}

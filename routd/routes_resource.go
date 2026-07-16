package routd

// routes_resource.go is the spec 5/16 step after web_routes + network_rules +
// scheduled_tasks: the agent's message-routing tools (add_route/set_routes/
// list_routes/delete_route) ride ONE resreg.Resource instead of four hand-rolled
// ipc/ipc.go tool bodies. Routing is security-critical — a route's TARGET folder
// decides which group a chat's turns fire in — so the two invariants the deleted
// ipc bodies enforced are preserved EXACTLY:
//
//   1. TARGET CONTAINMENT. A route's target folder must be within the caller's
//      reach. add/set carry the target(s) in the JSON args; delete resolves the
//      target from the route id. All three run auth.AuthorizeStructural (the route
//      tier cap: tier 2+ can't manage routes, tier 1 confined to strict
//      descendants, tier 0 unrestricted). set ALSO runs the per-route
//      routeTargetWithin containment (own folder or a descendant) — the tier cap
//      binds set only to the OWN folder, so it's this per-route check that stops a
//      tier-0 bulk write from targeting another folder.
//   2. SELF-DEFAULT GUARD. A folder must not delete — or replace-away via set —
//      its own seq-0 default route (isSelfDefault). Without it a group routes its
//      own inbound traffic into the void and can't be respawned.
//
// resreg owns the plumbing (handler dispatch + one tx wrapping each mutation AND
// its audit_log row); routd owns the auth POLICY. Like scheduled_tasks (and unlike
// network_rules), BOTH invariants live in the HANDLER, not the injected Gate: add's
// target needs a JSON unmarshal, set's needs a read of the live table, and delete's
// needs a GetRoute read — all of which the handler already performs. Resolving them
// again in the Gate would duplicate the work AND reorder validation ahead of the
// tier decision, diverging from the ipc bodies. The Gate does only the TOOL grant
// (grants.CheckAction + db.Authorize); the handler's caps run before any store
// write and roll the tx back on denial, so an operator ACL grant can open the TOOL
// but never widen the tier/containment/self-default cap.
//
// Both faces ride this one resource: the agent's MCP tools via routesPostBuild,
// and the operator REST face (/v1/routes) via routes_http.go's mountRoutes, which
// mounts the SAME Endpoints on a copy with a REST caller + gate injected
// (routes:read/write scope + ownsFolder containment) — the spec 5/16 REST-face
// fold. All REST-only policy lives in routes_http.go; the handler stays surface-
// agnostic EXCEPT list, which the agent needs whole-table (cross-folder shadow
// analysis) while the operator needs subtree-scoped — the one x.Surface branch below.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/router"
	"github.com/kronael/arizuko/store"
)

// Route actions are resource-specific verbs, not the CRUD shape. list reuses
// resreg.ActionList (== "list") so it stays read-only (Mutates() false, no tx);
// add/set/delete are custom strings so Mutates() is true and each opens the
// mutation+audit tx. delete addresses a row by the autoincrement `id` arg (a
// number), NOT a PK path — matching the deleted ipc body.
const (
	routesActionAdd    = resreg.Action("add")
	routesActionSet    = resreg.Action("set")
	routesActionDelete = resreg.Action("delete")
)

// routesMCPNames is the action→flat-tool-name map, single-sourced in resreg/
// resources (RoutesMCPNames) so the agent socket derivation, the /openapi.json
// walk, and dashd's tool browser read one owner. Aliased here for the Gate's
// action→policy-name lookup + the contain closures below.
var routesMCPNames = resources.RoutesMCPNames

// containFn is the routd-internal per-face containment seam the shared routes/
// scheduled_tasks handlers call to authorize one (action, target) against the
// caller. resreg carries no auth policy of its own (CLAUDE.md "auth is a uniform
// middleware, INJECTED per surface"); each face closes over its own rule — the
// agent MCP face over the tier model (auth.AuthorizeStructural), the operator REST
// face over folder containment (ownsFolder) — and passes it into the resource
// constructor. A nil return means allowed; a non-nil error is a resreg.Errorf the
// handler returns verbatim.
type containFn func(c resreg.Caller, a resreg.Action, target string) error

// routesResource is the single renderer for the four routing verbs — mounted on
// the agent socket (routesPostBuild) AND the operator REST face (mountRoutes). The
// DELETE endpoint's /v1/routes/{id} path is the REST addressing (deriveMCPTools
// ignores the path; the agent's delete_route reads the `id` arg). There is NO
// RowType (args come from MCPArgs as raw JSON strings + the numeric id, preserving
// the exact wire arg names the agent already sends). Store is a store.Store over
// routd.db so resreg.invoke opens the mutation+audit tx there. contain is the
// per-face target-containment seam (tier model for the agent, ownsFolder for REST)
// closed into the handler — see containFn.
func (s *Server) routesResource(contain containFn) resreg.Resource {
	r := resreg.Resource{
		Name:      "routes",
		Endpoints: resources.RoutesEndpoints, // single source: doc + mount + MCP read one list
		MCPDoc:    resources.RoutesMCPDoc,    // single source (resreg/resources)
		MCPArgs:   resources.RoutesMCPArgs,
		MCPNames:  routesMCPNames,
		Authz: func(resreg.Caller, resreg.Action, resreg.Args) (string, map[string]string, error) {
			return "", nil, nil
		},
		Store: store.New(s.db.SQL()),
	}
	r.Handler = func(ctx context.Context, x resreg.Execution) (any, error) {
		return s.routesHandler(ctx, x, contain)
	}
	return r
}

// routesHandler runs add/set/delete/list against routd.db, folding in the two
// security invariants the deleted ipc bodies enforced (see file header). The socket
// folder (x.Caller.Folder) is the caller's own folder. Per-target containment (the
// route tier cap for the agent, ownsFolder for REST) is the injected contain seam;
// the self-default + per-route routeTargetWithin invariants are face-agnostic and
// stay in the handler.
func (s *Server) routesHandler(ctx context.Context, x resreg.Execution, contain containFn) (any, error) {
	folder := x.Caller.Folder
	switch x.Action {
	case resreg.ActionList:
		routes, err := s.db.Routes()
		if x.Surface != audit.SurfaceREST {
			// Agent (MCP): the whole table, annotated — list_routes describes every
			// route so the agent can reason about cross-folder shadowing (unchanged
			// from the pre-fold ipc body; the error is swallowed as it always was).
			return map[string]any{"routes": router.Describe(routes)}, nil
		}
		// Operator (REST): the same rows scoped to the caller's subtree. An EMPTY
		// folder claim (root / service token) sees everything; a folder-scoped token —
		// even a top-level tenant, which is tier 0 — sees only its own. The leak guard
		// keys on the empty folder claim, never the tier. shadowed_by is computed over
		// the full table first, so cross-folder shadows still surface.
		if err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "store_error")
		}
		views := router.Describe(routes)
		out := make([]router.RouteView, 0, len(views))
		for _, v := range views {
			if routeTargetOwned(folder, v.Target) {
				out = append(out, v)
			}
		}
		return out, nil

	case routesActionAdd:
		var route core.Route
		if err := json.Unmarshal([]byte(argJSONString(x.Args, "route")), &route); err != nil {
			return nil, resreg.Errorf(http.StatusBadRequest, "invalid route json")
		}
		if route.Target == "" {
			return nil, resreg.Errorf(http.StatusBadRequest, "route.target required")
		}
		// Invariant 1 (target containment): the arg-carried target must be within
		// the caller's reach — the per-face contain seam (agent tier cap /
		// REST ownsFolder).
		if err := contain(x.Caller, routesActionAdd, route.Target); err != nil {
			return nil, err
		}
		rid, err := addRouteTx(ctx, x.Tx, route)
		if err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		slog.Info("route added", "id", rid, "target", route.Target, "match", route.Match)
		return map[string]any{"id": rid}, nil

	case routesActionSet:
		// Invariant 1 (own-folder cap): set_routes rewrites the caller's own subtree,
		// so contain binds the caller's OWN folder (agent tier cap → in practice tier
		// 0 only; REST ownsFolder → own folder always passes). The per-route
		// routeTargetWithin loop below is what actually confines each target.
		if err := contain(x.Caller, routesActionSet, folder); err != nil {
			return nil, err
		}
		var routes []core.Route
		if err := json.Unmarshal([]byte(argJSONString(x.Args, "routes")), &routes); err != nil {
			return nil, resreg.Errorf(http.StatusBadRequest, "invalid routes json: %v", err)
		}
		// Invariant 1 (per-route containment): every target must be the own folder
		// or a descendant — the tier cap above binds only the own folder, so this is
		// what stops a bulk write from targeting another folder. Skipped for the empty
		// folder (root / operator via REST): it replaces the whole table unrestricted,
		// matching the retired handleRoutesReplace. The agent socket folder is never
		// empty, so this only ever loosens the REST root path.
		if folder != "" {
			for _, r := range routes {
				if !routeTargetWithin(r.Target, folder) {
					return nil, resreg.Errorf(http.StatusForbidden, "route target outside own folder: %s", r.Target)
				}
			}
		}
		// Invariant 2 (self-default guard): if the live table holds a seq-0 route
		// pointing at this folder, the replacement must keep one — else the folder
		// orphans its own inbound traffic. s.db.Routes() is the whole table (routd's
		// ListRoutes returned all), matching the deleted body's ListRoutes read.
		existing, _ := s.db.Routes()
		hadDefault := false
		for _, r := range existing {
			if isSelfDefault(r, folder) {
				hadDefault = true
				break
			}
		}
		if hadDefault {
			keepsDefault := false
			for _, r := range routes {
				if isSelfDefault(r, folder) {
					keepsDefault = true
					break
				}
			}
			if !keepsDefault {
				return nil, resreg.Errorf(http.StatusForbidden, "cannot delete own default route")
			}
		}
		if err := setRoutesTx(ctx, x.Tx, folder, routes); err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		slog.Info("routes set", "folder", folder, "count", len(routes))
		return map[string]any{"updated": true, "count": len(routes)}, nil

	case routesActionDelete:
		rid := argInt64(x.Args, "id")
		if rid == 0 {
			return nil, resreg.Errorf(http.StatusBadRequest, "id required")
		}
		route, err := s.db.GetRoute(rid)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, resreg.Errorf(http.StatusNotFound, "route not found: %d", rid)
		}
		if err != nil {
			// A genuine store error must surface as 500, not mask as 404 — the
			// discrimination the retired routesRESTGate.GetRoute performed before
			// its per-target check moved into contain.
			return nil, resreg.Errorf(http.StatusInternalServerError, "store_error")
		}
		// Invariant 2 (self-default guard): a folder can't delete its own seq-0
		// default route. Checked before the containment cap, matching the ipc order.
		if isSelfDefault(route, folder) {
			return nil, resreg.Errorf(http.StatusForbidden, "cannot delete own default route")
		}
		// Invariant 1 (target containment): resolve the route's target from the id
		// (like scheduled_tasks resolves GetTask.Owner) and bind it via contain — a
		// folder must not delete a route whose target is outside its reach.
		if err := contain(x.Caller, routesActionDelete, route.Target); err != nil {
			return nil, err
		}
		if err := deleteRouteTx(ctx, x.Tx, rid); err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		slog.Info("route deleted", "id", rid)
		return map[string]any{"deleted": true, "id": rid}, nil
	}
	return nil, resreg.Errorf(http.StatusBadRequest, "unknown action %q", x.Action)
}

// routesPostBuild returns the ServeMCP seam that mounts the routing tools on the
// agent socket, with the tier-aware Gate + MatchingRules visibility for this
// folder's grant rules injected. The Gate does the TOOL grant (CheckAction +
// db.Authorize); the containment + self-default caps live in the handler (see
// header). Only rules the socket already carries can widen visibility, so a denied
// tier still sees nothing new.
func (s *Server) routesPostBuild(folder, callerSub string, rules []string, authorize authorizeFn) func(*mcpserver.MCPServer) {
	// Agent face: the route tier cap on the arg/id-resolved target — tier 2+ denied,
	// tier 1 confined to strict descendants, tier 0 unrestricted (auth.Resolve over
	// the socket folder). Exactly the deleted ipc bodies' authzStructural.
	contain := func(_ resreg.Caller, a resreg.Action, target string) error {
		if err := auth.AuthorizeStructural(auth.Resolve(folder), routesMCPNames[a],
			auth.AuthzTarget{RouteTarget: target}); err != nil {
			return resreg.Errorf(http.StatusForbidden, "%v", err)
		}
		return nil
	}
	res := s.routesResource(contain)
	res.Gate = func(x resreg.Execution, _ string, _ map[string]string) error {
		return toolGrant(rules, authorize, callerSub, folder, routesMCPNames[x.Action])
	}
	return func(srv *mcpserver.MCPServer) {
		resreg.MCPTools(srv, res, agentCallerFor(callerSub, folder), agentVisible(rules))
	}
}

// argInt64 reads a numeric arg from a resreg.Args map. MCP number args decode to
// float64 (resreg.decodeMCPArgs); the REST `{id}` path placeholder arrives as a
// string (decodeRESTArgs); an absent/other arg is 0 (the "id required" sentinel
// the delete handler checks, mirroring int64(req.GetInt("id", 0))).
func argInt64(args resreg.Args, key string) int64 {
	switch v := args[key].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	case string:
		n, _ := strconv.ParseInt(v, 10, 64)
		return n
	}
	return 0
}

// argJSONString reads a route/routes arg that arrives either as a JSON string (the
// MCP contract — the agent sends `route`/`routes` serialized) or as a decoded
// object/array (the REST body, which resreg.decodeRESTArgs unmarshals to
// map/[]any). Both feed the SAME json.Unmarshal in the handler, so one renderer
// serves both faces. A missing/other-typed arg yields "".
func argJSONString(args resreg.Args, key string) string {
	switch v := args[key].(type) {
	case string:
		return v
	case []any, map[string]any:
		b, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return string(b)
	}
	return ""
}

// addRouteTx appends one route row on tx (mirrors DB.AddRoute so the mutation lands
// in resreg.invoke's tx alongside its audit_log row), returning the new id.
func addRouteTx(ctx context.Context, tx *sql.Tx, r core.Route) (int64, error) {
	res, err := tx.ExecContext(ctx,
		`INSERT INTO routes(seq, match, target, observe_window_messages, observe_window_chars) VALUES(?,?,?,?,?)`,
		r.Seq, r.Match, r.Target, nz(r.ObserveWindowMessages), nz(r.ObserveWindowChars))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// setRoutesTx replaces the routes whose target (sans #fragment) is `folder` or under
// `folder/`, then inserts `routes`, on tx (mirrors DB.SetRoutes). An empty folder
// replaces the whole table; the folder-scoped delete keeps a scoped caller from
// wiping another folder's routes.
func setRoutesTx(ctx context.Context, tx *sql.Tx, folder string, routes []core.Route) error {
	if folder == "" {
		if _, err := tx.ExecContext(ctx, "DELETE FROM routes"); err != nil {
			return err
		}
	} else if _, err := tx.ExecContext(ctx,
		`DELETE FROM routes WHERE target = ? OR target LIKE ?||'#%' OR target LIKE ?||'/%'`,
		folder, folder, folder); err != nil {
		return err
	}
	for _, r := range routes {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO routes(seq, match, target, observe_window_messages, observe_window_chars) VALUES(?,?,?,?,?)`,
			r.Seq, r.Match, r.Target, nz(r.ObserveWindowMessages), nz(r.ObserveWindowChars)); err != nil {
			return err
		}
	}
	return nil
}

// deleteRouteTx removes one route by id on tx so the mutation lands in
// resreg.invoke's tx alongside its audit_log row; ErrNotFound when the row is absent.
func deleteRouteTx(ctx context.Context, tx *sql.Tx, id int64) error {
	res, err := tx.ExecContext(ctx, "DELETE FROM routes WHERE id=?", id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// routeTargetWithin reports whether a route's target folder is the caller's own
// folder or a descendant. A folder:-prefixed target is compared after stripping the
// prefix; daemon:/builtin: targets are never "within" a folder. Moved here from ipc
// when the route tools migrated to resreg (set_routes was its only caller).
func routeTargetWithin(target, owner string) bool {
	switch {
	case strings.HasPrefix(target, "folder:"):
		target = strings.TrimPrefix(target, "folder:")
	case strings.HasPrefix(target, "daemon:"), strings.HasPrefix(target, "builtin:"):
		return false
	}
	return target == owner || strings.HasPrefix(target, owner+"/")
}

// isSelfDefault reports whether r is a folder's seq-0 default route (target == the
// owner's own folder) — the route a group can't delete without orphaning its own
// inbound traffic. Moved here from ipc with the route tools.
func isSelfDefault(r core.Route, owner string) bool {
	target := strings.TrimPrefix(r.Target, "folder:")
	return r.Seq == 0 && target == owner
}

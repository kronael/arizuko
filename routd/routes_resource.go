package routd

// routes_resource.go puts the agent's message-routing tools (add_route/set_routes/
// list_routes/delete_route) on ONE resreg.Resource instead of four hand-rolled ipc
// tool bodies (spec 5/16). Routing is security-critical — a route's TARGET folder
// decides which group a chat's turns fire in — so the two invariants the deleted
// ipc bodies enforced are preserved EXACTLY:
//
//  1. TARGET CONTAINMENT. A route's target folder must be within the caller's
//     reach. All three mutating actions run the injected contain seam (agent:
//     auth.Authorize on the resolved target; REST: ownsFolder on the JWT folder).
//     set ALSO runs the per-route routeTargetWithin check — contain binds set only
//     to the OWN folder, so it is this that stops a bulk write from targeting
//     another folder.
//  2. SELF-DEFAULT GUARD. A folder must not delete, or replace away via set, its
//     own seq-0 default route. Without it a group routes its own inbound into the
//     void and cannot be respawned.
//
// BOTH invariants live in the HANDLER, not the injected Gate: add's target needs a
// JSON unmarshal, set's needs a read of the live table, and delete's needs a
// GetRoute read — all of which the handler already performs. So the agent Gate is a
// no-op and the REST Gate carries only the scope check; the handler's caps run
// before any store write and roll the tx back on denial.
//
// Both faces ride this one resource. The handler stays surface-agnostic EXCEPT
// list, which the agent needs whole-table (cross-folder shadow analysis) while the
// operator needs subtree-scoped — the one x.Surface branch below.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/router"
	"github.com/kronael/arizuko/store"
)

// Route actions are resource-specific verbs, not the CRUD shape. list reuses
// resreg.ActionList (== "list") so it stays read-only (Mutates() false, no tx);
// add/set/delete are custom strings so Mutates() is true and each opens the
// mutation+audit batch. delete addresses a row by the autoincrement `id` arg (a
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
// agent MCP face over auth.Authorize on the target, the operator REST face over
// folder containment (ownsFolder) — and passes it into the resource
// constructor. A nil return means allowed; a non-nil error is a resreg.Errorf the
// handler returns verbatim.
type containFn func(c resreg.Caller, a resreg.Action, target string) error

// routesResource is the single renderer for the four routing verbs, mounted on the
// agent socket AND the operator REST face. There is NO RowType: args come from
// MCPArgs as raw JSON strings plus the numeric id, preserving the exact wire names
// the agent already sends. Store is a store.Store over routd.db so resreg.invoke
// sends the mutation+audit batch there. contain is the per-face target-containment
// seam closed into the handler.
func (s *Server) routesResource(contain containFn) resreg.Resource {
	r := resreg.Mount(resources.RoutesName)
	r.Store = s.db.store()
	r.Handler = func(ctx context.Context, x resreg.Execution) (any, error) {
		return s.routesHandler(ctx, x, contain)
	}
	return r
}

// routesHandler runs add/set/delete/list against routd.db, folding in the two
// security invariants the deleted ipc bodies enforced (see file header). The socket
// folder (x.Caller.Folder) is the caller's own folder. Per-target containment
// (auth.Authorize for the agent, ownsFolder for REST) is the injected contain seam;
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
		// even a top-level tenant — sees only its own. The leak guard keys on the
		// empty folder claim, never on folder depth. shadowed_by is computed over
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
		// the caller's reach — the per-face contain seam (agent auth.Authorize on
		// the target / REST ownsFolder).
		if err := contain(x.Caller, routesActionAdd, route.Target); err != nil {
			return nil, err
		}
		// An add is idempotent — a rule already there answers with its own id — so
		// the only refusal left is a rule another caller created between the read
		// and the batch, and that is the 409.
		raced := resreg.Errorf(http.StatusConflict,
			"route seq=%d target=%q match=%q was created under this call; ask again for its id",
			route.Seq, route.Target, route.Match)
		rid, err := s.db.store().PutRouteInto(ctx, x.Writes, route, x.Caller.Sub,
			routesMCPNames[routesActionAdd], raced)
		if errors.Is(err, store.ErrWebJIDRouted) {
			return nil, resreg.Errorf(http.StatusBadRequest, "%v", err)
		}
		if err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		slog.Info("route added", "id", rid, "target", route.Target, "match", route.Match)
		return map[string]any{"id": rid}, nil

	case routesActionSet:
		// Invariant 1 (own-folder cap): set_routes rewrites the caller's own subtree,
		// so contain binds the caller's OWN folder (agent: must hold mcp:set_routes
		// over it; REST ownsFolder → own folder always passes). The per-route
		// routeTargetWithin loop below is what actually confines each target.
		if err := contain(x.Caller, routesActionSet, folder); err != nil {
			return nil, err
		}
		var routes []core.Route
		if err := json.Unmarshal([]byte(argJSONString(x.Args, "routes")), &routes); err != nil {
			return nil, resreg.Errorf(http.StatusBadRequest, "invalid routes json: %v", err)
		}
		// Invariant 1 (per-route containment): every target must be the own folder
		// or a descendant — the contain check above binds only the own folder, so this
		// is what stops a bulk write from targeting another folder. Skipped for the empty
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
		if err := s.db.store().ReplaceFolderRoutesInto(ctx, x.Writes, folder, routes,
			x.Caller.Sub, routesMCPNames[routesActionSet]); err != nil {
			if errors.Is(err, store.ErrWebJIDRouted) {
				return nil, resreg.Errorf(http.StatusBadRequest, "%v", err)
			}
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
		if errors.Is(err, ErrNoRoute) {
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
		notFound := resreg.Errorf(http.StatusNotFound, "route not found: %d", rid)
		gone, err := s.db.store().DeleteRouteInto(ctx, x.Writes, rid, notFound)
		if err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		if !gone {
			return nil, notFound
		}
		slog.Info("route deleted", "id", rid)
		return map[string]any{"deleted": true, "id": rid}, nil
	}
	return nil, resreg.Errorf(http.StatusBadRequest, "unknown action %q", x.Action)
}

// routesPostBuild returns the ServeMCP seam that mounts the routing tools on the
// agent socket, with the per-target contain seam + the turn's visibility view
// injected. The Gate is a no-op here; the containment + self-default caps live in
// the handler (see header). Only grants the caller already holds can widen
// visibility, so a denied caller sees nothing new.
func (s *Server) routesPostBuild(folder, callerSub string, authorize authorizeFn, visible func(string) bool) func(*mcpserver.MCPServer) {
	// Agent face: one evaluator on the arg/id-resolved target — the caller must hold
	// the tool (mcp:<name>) scoped to cover the target. Magnitude + containment in one
	// call; /root elevates via authorize's allow-all.
	contain := func(_ resreg.Caller, a resreg.Action, target string) error {
		name := routesMCPNames[a]
		if !authorize(target, "mcp:"+name, nil) {
			return resreg.Errorf(http.StatusForbidden, "%s on %s: not permitted", name, target)
		}
		return nil
	}
	res := s.routesResource(contain)
	res.Gate = agentAllowGate
	return mountAgentResource(res, callerSub, folder, visible)
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

// The three writes are store.PutRouteTx, store.ReplaceFolderRoutesTx and
// store.DeleteRouteInto, on resreg.invoke's write set so each lands with the call's single
// audit_log row. They live there because a rule's coordinate is (seq, target,
// match) — with `seq` padded so text order IS precedence order — and because its
// `id` is minted from a counter store owns: two concurrent adds reading a maximum
// would hand one handle to two rules, after which delete_route names both.
//
// added_by/added_via carry the same (who, act) pair onbod stamps (spec 5/18 step
// 7). Redundant here with resreg's audit_log row — same DB, same tx — but a
// reader must not need to know which daemon wrote a rule to learn who wrote it;
// one read of `routes` answers for every attributed writer.

// routeTargetWithin reports whether a route's target folder is the caller's own
// folder or a descendant. Spec 5/34 gives targets no PREFIX vocabulary, so a target
// carrying one is not a folder — but a target legally carries a `#observe`/`#announce`
// FRAGMENT, and that has to come off before the comparison.
//
// It did not, so `main/trading` could not manage its own `main/trading#observe`
// route: a list→edit→set round-trip either 403'd on the caller's own row or dropped
// it, and it could not be re-added. sloth and marinade carry 10 such rows (BUGS L82).
func routeTargetWithin(target, owner string) bool {
	return groupfolder.Contains(owner, core.ParseRouteTarget(target).Folder)
}

// isSelfDefault reports whether r is a folder's seq-0 default route (target == the
// owner's own folder) — the route a group can't delete without orphaning its own
// inbound traffic. Moved here from ipc with the route tools.
func isSelfDefault(r core.Route, owner string) bool {
	return r.Seq == 0 && r.Target == owner
}

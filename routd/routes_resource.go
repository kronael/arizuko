package routd

// routes_resource.go puts the agent's routing tools on ONE resreg.Resource
// (spec 5/16). A route's TARGET decides which group a chat's turns fire in,
// so two invariants hold EXACTLY, in the HANDLER (each needs a read the
// handler already performs), not the Gate: TARGET CONTAINMENT — a target must
// be within the caller's reach via the injected contain seam, and set ALSO
// runs routeTargetWithin per route because contain binds only the OWN
// folder; and the SELF-DEFAULT GUARD — a folder must not delete or replace
// away its own seq-0 route, or it routes its own inbound into the void. list
// is the one x.Surface branch: the agent needs the whole table, the operator
// its subtree.

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

// list reuses resreg.ActionList so it stays read-only (no tx); add/set/delete
// are custom strings so Mutates() is true and each opens the mutation+audit
// batch. delete addresses a row by the numeric `id` arg, NOT a PK path.
const (
	routesActionAdd    = resreg.Action("add")
	routesActionSet    = resreg.Action("set")
	routesActionDelete = resreg.Action("delete")
)

// routesMCPNames is the action→flat-tool-name map, single-sourced in
// resreg/resources so the socket, /openapi.json and dashd's tool browser read
// one owner.
var routesMCPNames = resources.RoutesMCPNames

// containFn is the per-face containment seam the shared routes/scheduled_tasks
// handlers call for one (action, target). resreg carries no auth policy of its
// own; each face closes over its own rule — the agent over auth.Authorize on
// the target, REST over ownsFolder. nil means allowed; a non-nil error is a
// resreg.Errorf the handler returns verbatim.
type containFn func(c resreg.Caller, a resreg.Action, target string) error

// routesResource is the single renderer for the four routing verbs, mounted on
// the agent socket AND the operator REST face. No RowType: args are raw JSON
// strings plus the numeric id, the exact wire names the agent sends.
func (s *Server) routesResource(contain containFn) resreg.Resource {
	r := resreg.Mount(resources.RoutesName)
	r.Store = s.db.store()
	r.Handler = func(ctx context.Context, x resreg.Execution) (any, error) {
		return s.routesHandler(ctx, x, contain)
	}
	return r
}

// routesHandler runs add/set/delete/list. Per-target containment is the
// injected contain seam; the self-default + routeTargetWithin invariants are
// face-agnostic and stay here.
func (s *Server) routesHandler(ctx context.Context, x resreg.Execution, contain containFn) (any, error) {
	folder := x.Caller.Folder
	switch x.Action {
	case resreg.ActionList:
		routes, err := s.db.Routes()
		if x.Surface != audit.SurfaceREST {
			// Agent: the whole table, annotated, so the agent can reason about
			// cross-folder shadowing; the read error is swallowed.
			return map[string]any{"routes": router.Describe(routes)}, nil
		}
		// Operator (REST): scoped to the caller's subtree. The leak guard keys
		// on the EMPTY folder claim (root / service token), never on folder
		// depth; shadowed_by is computed over the full table first.
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
		// Invariant 1: the arg-carried target must be within the caller's reach.
		if err := contain(x.Caller, routesActionAdd, route.Target); err != nil {
			return nil, err
		}
		// An add is idempotent (a rule already there answers with its own id),
		// so the only refusal left is a rule created between the read and the
		// batch: the 409.
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
		// Invariant 1: contain binds the caller's OWN folder; the per-route loop
		// below is what confines each target.
		if err := contain(x.Caller, routesActionSet, folder); err != nil {
			return nil, err
		}
		var routes []core.Route
		if err := json.Unmarshal([]byte(argJSONString(x.Args, "routes")), &routes); err != nil {
			return nil, resreg.Errorf(http.StatusBadRequest, "invalid routes json: %v", err)
		}
		// Every target must be the own folder or a descendant. Skipped for the
		// empty folder (root via REST), which replaces the whole table; the agent
		// socket folder is never empty.
		if folder != "" {
			for _, r := range routes {
				if !routeTargetWithin(r.Target, folder) {
					return nil, resreg.Errorf(http.StatusForbidden, "route target outside own folder: %s", r.Target)
				}
			}
		}
		// Invariant 2: if the live table holds a seq-0 route at this folder, the
		// replacement must keep one.
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
		// An ambiguous id is a CONFLICT the caller can act on, and it carries both
		// coordinates: masked as store_error it is the silence L364 was filed
		// against, and the store refuses the delete either way.
		if errors.Is(err, store.ErrRouteIDAmbiguous) {
			return nil, resreg.Errorf(http.StatusConflict, "%v", err)
		}
		if err != nil {
			// A store error is a 500, never masked as a 404.
			return nil, resreg.Errorf(http.StatusInternalServerError, "store_error")
		}
		// Invariant 2, checked before containment.
		if isSelfDefault(route, folder) {
			return nil, resreg.Errorf(http.StatusForbidden, "cannot delete own default route")
		}
		// Invariant 1: the target is resolved from the id, then bound via contain.
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

// routesPostBuild mounts the routing tools on the agent socket. The Gate is a
// no-op; the containment + self-default caps live in the handler.
func (s *Server) routesPostBuild(folder, callerSub string, authorize authorizeFn, visible func(string) bool) func(*mcpserver.MCPServer) {
	// One evaluator on the resolved target: the caller must hold mcp:<name>
	// scoped to cover it; /root elevates via authorize's allow-all.
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

// argInt64 reads a numeric arg: MCP numbers decode to float64, the REST `{id}`
// placeholder arrives as a string; absent/other is 0, the "id required"
// sentinel the delete handler checks.
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

// argJSONString reads a route/routes arg that arrives as a JSON string (MCP)
// or a decoded object/array (REST), so one json.Unmarshal in the handler
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

// The three writes live in store, on resreg.invoke's write set: a rule's
// coordinate is (seq, target, match) with `seq` padded so text order IS
// precedence order, and its `id` is minted from a counter store owns — two
// concurrent adds reading a maximum would hand one handle to two rules.
// added_by/added_via carry the same (who, act) pair onbod stamps (spec 5/18):
// a reader must not need to know which daemon wrote a rule to learn who did.

// routeTargetWithin reports whether a route's target folder is the caller's
// own folder or a descendant. A target legally carries a `#observe`/`#announce`
// FRAGMENT, and it comes off before the comparison: with it on, `main/trading`
// could not manage its own `main/trading#observe` route (BUGS L82).
func routeTargetWithin(target, owner string) bool {
	return groupfolder.Contains(owner, core.ParseRouteTarget(target).Folder)
}

// isSelfDefault reports whether r is a folder's seq-0 default route — the one
// a group cannot delete without orphaning its own inbound traffic.
func isSelfDefault(r core.Route, owner string) bool {
	return r.Seq == 0 && r.Target == owner
}

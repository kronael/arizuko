package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/store"
)

// routesResource holds the proxyd `routes` Resource state: the route
// table held under an RWMutex (so reads stay lock-free under load) and
// the cached ReverseProxy map rebuilt atomically on every mutation.
type routesResource struct {
	st     *store.Store
	mu     sync.RWMutex
	routes []Route
	procs  map[string]*httputil.ReverseProxy
}

func newRoutesResource(st *store.Store, initial []Route) *routesResource {
	rr := &routesResource{st: st}
	_ = rr.swap(initial)
	return rr
}

// snapshot returns a read-locked view of the route table + proxies.
// Returned slices/maps are append-only (replaced wholesale on mutation),
// so callers can use them without holding the lock past the call.
func (rr *routesResource) snapshot() ([]Route, map[string]*httputil.ReverseProxy) {
	rr.mu.RLock()
	defer rr.mu.RUnlock()
	return rr.routes, rr.procs
}

func (rr *routesResource) swap(routes []Route) error {
	procs := make(map[string]*httputil.ReverseProxy, len(routes))
	for _, r := range routes {
		p := buildRouteProxy(r)
		if p == nil {
			return fmt.Errorf("invalid backend %q for path %q", r.Backend, r.Path)
		}
		procs[r.Path] = p
	}
	rr.mu.Lock()
	rr.routes = routes
	rr.procs = procs
	rr.mu.Unlock()
	return nil
}

func toStoreRoute(r Route) store.ProxydRoute {
	return store.ProxydRoute{
		Path: r.Path, Backend: r.Backend, Auth: r.Auth,
		GatedBy: r.GatedBy, PreserveHeaders: r.PreserveHeaders,
		StripPrefix: r.StripPrefix,
	}
}

func fromStoreRoute(r store.ProxydRoute) Route {
	return Route{
		Path: r.Path, Backend: r.Backend, Auth: r.Auth,
		GatedBy: r.GatedBy, PreserveHeaders: r.PreserveHeaders,
		StripPrefix: r.StripPrefix,
	}
}

func validateRoute(r Route, existing []Route, isUpdate bool) error {
	if !strings.HasPrefix(r.Path, "/") {
		return resreg.Errorf(http.StatusBadRequest, "path %q must start with '/'", r.Path)
	}
	if r.Backend == "" {
		return resreg.Errorf(http.StatusBadRequest, "backend is required")
	}
	if !validAuth[r.Auth] {
		return resreg.Errorf(http.StatusBadRequest, "auth %q not in {public,user,operator}", r.Auth)
	}
	if !isUpdate {
		for _, e := range existing {
			if e.Path == r.Path {
				return resreg.Errorf(http.StatusConflict, "path %q already exists", r.Path)
			}
		}
	}
	return nil
}

// argRoute pulls a Route out of resreg.Args. Re-marshal/unmarshal keeps
// JSON tag handling in one place.
func argRoute(args resreg.Args) (Route, error) {
	b, err := json.Marshal(args)
	if err != nil {
		return Route{}, resreg.Errorf(http.StatusBadRequest, "encode args: %v", err)
	}
	var r Route
	if err := json.Unmarshal(b, &r); err != nil {
		return Route{}, resreg.Errorf(http.StatusBadRequest, "decode route: %v", err)
	}
	return r, nil
}

// handle is the single Handler called by both REST and MCP adapters.
// Mutating actions run inside x.Tx — DB writes via tx-aware store
// helpers; in-memory cache swap happens after the tx commits in
// adapter (the swap is best-effort; restart picks up persisted state).
func (rr *routesResource) handle(ctx context.Context, x resreg.Execution) (any, error) {
	switch x.Action {
	case resreg.ActionList:
		routes, _ := rr.snapshot()
		out := make([]Route, 0, len(routes))
		out = append(out, routes...)
		return map[string]any{"routes": out}, nil

	case resreg.ActionGet:
		path := normalisePath(x.Args["path"])
		if path == "" {
			return nil, resreg.Errorf(http.StatusBadRequest, "path required")
		}
		routes, _ := rr.snapshot()
		for _, r := range routes {
			if r.Path == path {
				return r, nil
			}
		}
		return nil, resreg.Errorf(http.StatusNotFound, "route %q not found", path)

	case resreg.ActionCreate:
		r, err := argRoute(x.Args)
		if err != nil {
			return nil, err
		}
		routes, _ := rr.snapshot()
		if err := validateRoute(r, routes, false); err != nil {
			return nil, err
		}
		if err := insertProxydRouteTx(ctx, x.Tx, toStoreRoute(r)); err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "persist: %v", err)
		}
		next := append([]Route(nil), routes...)
		next = append(next, r)
		if err := rr.swap(next); err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "swap: %v", err)
		}
		return r, nil

	case resreg.ActionUpdate:
		path := normalisePath(x.Args["path"])
		if path == "" {
			return nil, resreg.Errorf(http.StatusBadRequest, "path required")
		}
		routes, _ := rr.snapshot()
		idx := -1
		for i, r := range routes {
			if r.Path == path {
				idx = i
				break
			}
		}
		if idx < 0 {
			return nil, resreg.Errorf(http.StatusNotFound, "route %q not found", path)
		}
		merged := routes[idx]
		if v, ok := x.Args["backend"].(string); ok && v != "" {
			merged.Backend = v
		}
		if v, ok := x.Args["auth"].(string); ok && v != "" {
			merged.Auth = v
		}
		if v, ok := x.Args["gated_by"].(string); ok {
			merged.GatedBy = v
		}
		if v, ok := x.Args["strip_prefix"].(bool); ok {
			merged.StripPrefix = v
		}
		if v, ok := x.Args["preserve_headers"].([]any); ok {
			hs := make([]string, 0, len(v))
			for _, h := range v {
				if s, ok := h.(string); ok {
					hs = append(hs, s)
				}
			}
			merged.PreserveHeaders = hs
		}
		if err := validateRoute(merged, routes, true); err != nil {
			return nil, err
		}
		if err := updateProxydRouteTx(ctx, x.Tx, toStoreRoute(merged)); err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "persist: %v", err)
		}
		next := append([]Route(nil), routes...)
		next[idx] = merged
		if err := rr.swap(next); err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "swap: %v", err)
		}
		return merged, nil

	case resreg.ActionDelete:
		path := normalisePath(x.Args["path"])
		if path == "" {
			return nil, resreg.Errorf(http.StatusBadRequest, "path required")
		}
		if _, err := deleteProxydRouteTx(ctx, x.Tx, path); err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "delete: %v", err)
		}
		routes, _ := rr.snapshot()
		next := make([]Route, 0, len(routes))
		for _, r := range routes {
			if r.Path != path {
				next = append(next, r)
			}
		}
		if err := rr.swap(next); err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "swap: %v", err)
		}
		return nil, nil

	default:
		return nil, resreg.Errorf(http.StatusBadRequest, "unknown action %q", x.Action)
	}
}

func normalisePath(v any) string {
	s, _ := v.(string)
	if s == "" {
		return ""
	}
	if !strings.HasPrefix(s, "/") {
		s = "/" + s
	}
	return s
}

// routesAuthz is the per-action ACL scope/params derivation. routes is
// a global resource — no per-folder axis, scope="" and no params.
// Operators carry an ACL row like `(google:op, '*', '**')`; the
// canonical auth.Authorize call is the gate.
func routesAuthz(_ resreg.Caller, _ resreg.Action, _ resreg.Args) (string, map[string]string, error) {
	return "", nil, nil
}

// routesResourceDecl is the resreg.Resource literal: REST endpoints,
// MCP tools, authz, single handler. Per spec 5/5.
func routesResourceDecl(rr *routesResource) resreg.Resource {
	return resreg.Resource{
		Name: "routes",
		Endpoints: []resreg.Endpoint{
			{Verb: "GET", Path: "/v1/routes", Action: resreg.ActionList, Status: http.StatusOK},
			{Verb: "GET", Path: "/v1/routes/{path...}", Action: resreg.ActionGet, Status: http.StatusOK},
			{Verb: "POST", Path: "/v1/routes", Action: resreg.ActionCreate, Status: http.StatusCreated},
			{Verb: "PATCH", Path: "/v1/routes/{path...}", Action: resreg.ActionUpdate, Status: http.StatusOK},
			{Verb: "DELETE", Path: "/v1/routes/{path...}", Action: resreg.ActionDelete, Status: http.StatusNoContent},
		},
		MCPTools: []resreg.MCPTool{
			{Name: "routes.list", Action: resreg.ActionList,
				Description: "List proxyd's runtime route table."},
			{Name: "routes.get", Action: resreg.ActionGet,
				Description: "Read one proxyd route by path.",
				Args: []resreg.MCPArg{{Name: "path", Type: "string", Required: true}}},
			{Name: "routes.create", Action: resreg.ActionCreate,
				Description: "Create a proxyd route. Body fields mirror the TOML proxyd_route block.",
				Args: []resreg.MCPArg{
					{Name: "path", Type: "string", Required: true},
					{Name: "backend", Type: "string", Required: true},
					{Name: "auth", Type: "string", Required: true, Description: "public | user | operator"},
					{Name: "gated_by", Type: "string"},
					{Name: "preserve_headers", Type: "array"},
					{Name: "strip_prefix", Type: "bool"},
				}},
			{Name: "routes.update", Action: resreg.ActionUpdate,
				Description: "Update fields on an existing proxyd route. Path is the key.",
				Args: []resreg.MCPArg{
					{Name: "path", Type: "string", Required: true},
					{Name: "backend", Type: "string"},
					{Name: "auth", Type: "string"},
					{Name: "gated_by", Type: "string"},
					{Name: "preserve_headers", Type: "array"},
					{Name: "strip_prefix", Type: "bool"},
				}},
			{Name: "routes.delete", Action: resreg.ActionDelete,
				Description: "Delete a proxyd route. Idempotent.",
				Args: []resreg.MCPArg{{Name: "path", Type: "string", Required: true}}},
		},
		Authz:   routesAuthz,
		Handler: rr.handle,
		Store:   rr.st,
	}
}

// callerFromHTTP builds a resreg.Caller from proxyd's signed identity
// headers. Operator detection (the `**` marker in X-User-Groups) is
// recorded into Claims["operator"]="1" so ACL row predicates can
// match `predicate="operator=1"`. Until the ACL is seeded with
// operator rows for the routes resource, this is the bridge — see
// spec 4/9 §"Operator implicit".
func callerFromHTTP(hmacSecret string) resreg.CallerFromHTTPFunc {
	return func(r *http.Request) (resreg.Caller, error) {
		if !auth.VerifyUserSig(hmacSecret, r) {
			return resreg.Caller{}, fmt.Errorf("missing or invalid identity")
		}
		sub := r.Header.Get("X-User-Sub")
		name := r.Header.Get("X-User-Name")
		var groups []string
		if hdr := r.Header.Get("X-User-Groups"); hdr != "" {
			_ = json.Unmarshal([]byte(hdr), &groups)
		}
		c := resreg.Caller{Sub: sub, Name: name, Claims: map[string]string{}}
		for _, g := range groups {
			if g == "**" {
				c.Claims["operator"] = "1"
				break
			}
		}
		return c, nil
	}
}

// Tx-aware store helpers — proxyd-local until store/ exposes them
// uniformly. Keeps the mutation + audit row in one transaction.
func insertProxydRouteTx(ctx context.Context, tx *sql.Tx, r store.ProxydRoute) error {
	headers, strip := proxydRouteJSON(r)
	_, err := tx.ExecContext(ctx, `INSERT INTO proxyd_routes
		(path, backend, auth, gated_by, preserve_headers, strip_prefix)
		VALUES (?, ?, ?, ?, ?, ?)`,
		r.Path, r.Backend, r.Auth, r.GatedBy, headers, strip)
	return err
}

func updateProxydRouteTx(ctx context.Context, tx *sql.Tx, r store.ProxydRoute) error {
	headers, strip := proxydRouteJSON(r)
	res, err := tx.ExecContext(ctx, `UPDATE proxyd_routes
		SET backend=?, auth=?, gated_by=?, preserve_headers=?, strip_prefix=?
		WHERE path=?`,
		r.Backend, r.Auth, r.GatedBy, headers, strip, r.Path)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("proxyd route %q not found", r.Path)
	}
	return nil
}

func deleteProxydRouteTx(ctx context.Context, tx *sql.Tx, path string) (bool, error) {
	res, err := tx.ExecContext(ctx, `DELETE FROM proxyd_routes WHERE path = ?`, path)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func proxydRouteJSON(r store.ProxydRoute) (headers string, strip int) {
	b, _ := json.Marshal(r.PreserveHeaders)
	if r.PreserveHeaders == nil {
		b = []byte("[]")
	}
	if r.StripPrefix {
		strip = 1
	}
	return string(b), strip
}

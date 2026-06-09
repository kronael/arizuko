package main

// proxyd `routes` Resource — spec 5/36 no-cache audit (May 2026).
//
// Prior to 5/36 this file held a routesResource with sync.RWMutex around
// a []Route snapshot rebuilt on every mutation. That cache violated spec
// 5/36 §"Two-table-class model" rule 6 ("no daemon may cache config-
// table rows"). The cache had no measurable benefit — DB reads cost
// microseconds — and created stale-read windows that broke YAML apply
// semantics.
//
// Replacement (this file): every request reads routes from the DB
// directly via store.AllProxydRoutes (or resreg's proxyd_routes
// ScanAll) and builds a fresh ReverseProxy per route. No row cache;
// the http.Transport pools backend connections on its own.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/store"
)

// routesResource is the stateless route handler.
type routesResource struct {
	st *store.Store

	// manualRoutes is a test-only fallback used when st is nil — i.e.
	// unit tests that exercise the routing plumbing without spinning up
	// a full store. In production paths st is always non-nil and this
	// stays empty; the DB is the source of truth.
	manualMu        sync.RWMutex
	manualRoutes    []Route
	manualWebRoutes []store.WebRoute
}

// newRoutesResource constructs the handle. The `initial` arg seeds the
// test-only manual route fallback when st is nil; production reads routes
// from the DB per request (spec 5/36).
func newRoutesResource(st *store.Store, initial []Route) *routesResource {
	rr := &routesResource{st: st}
	if st == nil {
		// Test path: hold routes in memory. Production paths always pass
		// a non-nil store.
		rr.installManual(initial)
	}
	return rr
}

// snapshot returns a fresh view of the route table by querying the DB.
// One indexed read per call; cheap on SQLite/WAL. Callers MUST NOT
// hold the returned slice across requests — it's a per-call snapshot.
func (rr *routesResource) snapshot() ([]Route, map[string]*httputil.ReverseProxy) {
	var routes []Route
	if rr.st != nil {
		stored, err := rr.st.AllProxydRoutes()
		if err != nil {
			// No row cache to fall back to (spec 5/36); surface the error
			// so a transient DB fault is a visible outage, not silent 404s.
			slog.Error("proxyd: read proxyd_routes", "err", err)
			return nil, nil
		}
		routes = make([]Route, 0, len(stored))
		for _, r := range stored {
			routes = append(routes, fromStoreRoute(r))
		}
	} else {
		rr.manualMu.RLock()
		routes = append([]Route(nil), rr.manualRoutes...)
		rr.manualMu.RUnlock()
	}
	// Each Path gets a fresh proxy: the Director closure captures
	// r.StripPrefix and r.PreserveHeaders. The http.Transport pools
	// connections per backend host on its own.
	procs := make(map[string]*httputil.ReverseProxy, len(routes))
	for _, r := range routes {
		procs[r.Path] = buildRouteProxy(r)
	}
	return routes, procs
}

// webSnapshot returns a fresh view of the web_routes table by querying
// the DB. Like snapshot(), one indexed read per call (no row cache, spec
// 5/36). The test path reads from manualWebRoutes when st is nil.
func (rr *routesResource) webSnapshot() []store.WebRoute {
	if rr.st != nil {
		stored, err := rr.st.AllWebRoutes()
		if err != nil {
			slog.Error("proxyd: read web_routes", "err", err)
			return nil
		}
		return stored
	}
	rr.manualMu.RLock()
	defer rr.manualMu.RUnlock()
	return append([]store.WebRoute(nil), rr.manualWebRoutes...)
}

// installManualWeb is the test-only entry point for seeding web_routes
// without a backing store. Production uses the DB as source of truth.
func (rr *routesResource) installManualWeb(routes []store.WebRoute) {
	rr.manualMu.Lock()
	rr.manualWebRoutes = append([]store.WebRoute(nil), routes...)
	rr.manualMu.Unlock()
}

// installManual is the test-only entry point for adding routes without
// a backing store. Production code MUST not use this — production uses
// the DB as source of truth.
func (rr *routesResource) installManual(routes []Route) {
	rr.manualMu.Lock()
	rr.manualRoutes = append([]Route(nil), routes...)
	rr.manualMu.Unlock()
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
// helpers. The DB is the source of truth; no in-memory swap needed.
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
		return merged, nil

	case resreg.ActionDelete:
		path := normalisePath(x.Args["path"])
		if path == "" {
			return nil, resreg.Errorf(http.StatusBadRequest, "path required")
		}
		if _, err := deleteProxydRouteTx(ctx, x.Tx, path); err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "delete: %v", err)
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

// routesAuthz derives the ACL scope/params for the routes resource. routes
// is GLOBAL (no per-folder axis), so the derivation is genuinely empty:
// scope="" and no params. This is NOT a bypass — resreg.invoke still runs
// auth.Authorize(store, caller, "routes.<action>", "", nil), and an empty
// scope is matched by an operator ACL row like `(google:op, '*', '**')`
// (`**` covers scope "", `*` covers any action). A non-operator presents no
// matching row and no mcp:* tier fallback applies, so the call is denied.
// The gate is the ACL row, not this function and not the operator marker in
// callerFromHTTP (which only records the `**` claim for predicate matching).
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

package main

import (
	"context"
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

// argRoute pulls a Route out of resreg.Args. Body is decoded as raw
// JSON via map[string]any; we re-marshal then unmarshal into Route so
// JSON tag handling stays in one place.
func argRoute(args resreg.Args) (Route, error) {
	b, err := json.Marshal(args)
	if err != nil {
		return Route{}, resreg.Errorf(http.StatusBadRequest, "encode args: %v", err)
	}
	var r Route
	dec := json.NewDecoder(strings.NewReader(string(b)))
	if err := dec.Decode(&r); err != nil {
		return Route{}, resreg.Errorf(http.StatusBadRequest, "decode route: %v", err)
	}
	return r, nil
}

// handle is the single Handler called by both REST and MCP adapters.
// The action selects the branch; no surface-specific code lives below
// this line.
func (rr *routesResource) handle(ctx context.Context, c resreg.Caller, action resreg.Action, args resreg.Args) (any, error) {
	switch action {
	case resreg.ActionList:
		routes, _ := rr.snapshot()
		out := make([]Route, 0, len(routes))
		out = append(out, routes...)
		return map[string]any{"routes": out}, nil

	case resreg.ActionGet:
		path, _ := args["path"].(string)
		if path == "" {
			return nil, resreg.Errorf(http.StatusBadRequest, "path required")
		}
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		routes, _ := rr.snapshot()
		for _, r := range routes {
			if r.Path == path {
				return r, nil
			}
		}
		return nil, resreg.Errorf(http.StatusNotFound, "route %q not found", path)

	case resreg.ActionCreate:
		r, err := argRoute(args)
		if err != nil {
			return nil, err
		}
		routes, _ := rr.snapshot()
		if err := validateRoute(r, routes, false); err != nil {
			return nil, err
		}
		if err := rr.st.InsertProxydRoute(toStoreRoute(r)); err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "persist: %v", err)
		}
		next := append([]Route(nil), routes...)
		next = append(next, r)
		if err := rr.swap(next); err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "swap: %v", err)
		}
		return r, nil

	case resreg.ActionUpdate:
		path, _ := args["path"].(string)
		if path == "" {
			return nil, resreg.Errorf(http.StatusBadRequest, "path required")
		}
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
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
		// Merge: start from existing, apply provided fields.
		merged := routes[idx]
		if v, ok := args["backend"].(string); ok && v != "" {
			merged.Backend = v
		}
		if v, ok := args["auth"].(string); ok && v != "" {
			merged.Auth = v
		}
		if v, ok := args["gated_by"].(string); ok {
			merged.GatedBy = v
		}
		if v, ok := args["strip_prefix"].(bool); ok {
			merged.StripPrefix = v
		}
		if v, ok := args["preserve_headers"].([]any); ok {
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
		if err := rr.st.UpdateProxydRoute(toStoreRoute(merged)); err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "persist: %v", err)
		}
		next := append([]Route(nil), routes...)
		next[idx] = merged
		if err := rr.swap(next); err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "swap: %v", err)
		}
		return merged, nil

	case resreg.ActionDelete:
		path, _ := args["path"].(string)
		if path == "" {
			return nil, resreg.Errorf(http.StatusBadRequest, "path required")
		}
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		if _, err := rr.st.DeleteProxydRoute(path); err != nil {
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
		return nil, resreg.Errorf(http.StatusBadRequest, "unknown action %q", action)
	}
}

// routesPolicy enforces operator-only access on all five actions. routes
// is a global resource (no :own_group axis) per spec 6/5 §"Per-resource
// access matrix". `routes:read` admits the two read actions; `routes:write`
// admits the three mutating actions.
func routesPolicy() map[resreg.Action]resreg.ScopePred {
	read := func(c resreg.Caller, _ string) bool {
		return resreg.HasScope(c, "routes", "read") || resreg.HasScope(c, "routes", "write")
	}
	write := func(c resreg.Caller, _ string) bool {
		return resreg.HasScope(c, "routes", "write")
	}
	return map[resreg.Action]resreg.ScopePred{
		resreg.ActionList:   read,
		resreg.ActionGet:    read,
		resreg.ActionCreate: write,
		resreg.ActionUpdate: write,
		resreg.ActionDelete: write,
	}
}

// routesResourceDecl is the resreg.Resource literal: REST endpoints,
// MCP tools, policy, single handler. New surface for an existing action
// = one row; no code beyond the literal. Per spec 6/5.
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
		Policy:  routesPolicy(),
		Handler: rr.handle,
	}
}

// callerFromHTTP builds a resreg.Caller from proxyd's signed identity
// headers. Operators carry the `**` marker in X-User-Groups (current
// auth model); they receive `routes:*` so both read+write admit. JWT
// scope claims will replace this mapping once they ship — documented
// in spec 6/5 §"Token / auth model" as a temporary gap.
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
		c := resreg.Caller{Sub: sub, Name: name}
		// Operator detection: the `**` marker in the groups claim is the
		// operator gate (auth.MatchGroups). Mint `routes:*` for them.
		for _, g := range groups {
			if g == "**" {
				c.Scope = append(c.Scope, "routes:*")
				break
			}
		}
		return c, nil
	}
}

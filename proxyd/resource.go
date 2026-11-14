package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"slices"
	"strings"
	"sync"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/store"
)

type routesResource struct {
	st     *store.Store
	routes objstore.Snapshot[routeSnapshot]
	web    objstore.Snapshot[[]store.WebRoute]

	manualMu        sync.RWMutex
	manualRoutes    []Route
	manualWebRoutes []store.WebRoute
}

type routeSnapshot struct {
	routes  []Route
	proxies map[string]*httputil.ReverseProxy
}

func newRoutesResource(st *store.Store, initial []Route) *routesResource {
	rr := &routesResource{st: st}
	if st == nil {

		rr.installManual(initial)
	}
	return rr
}

func (rr *routesResource) snapshot() ([]Route, map[string]*httputil.ReverseProxy, error) {
	var routes []Route
	if rr.st != nil {
		state, err := rr.routes.Read(context.Background(), rr.st.Obj(), store.OwnerScope(store.OwnerRoutd), func(context.Context) (routeSnapshot, error) {
			stored, err := rr.st.AllProxydRoutes()
			if err != nil {
				return routeSnapshot{}, err
			}
			rows := make([]Route, 0, len(stored))
			for _, row := range stored {
				rows = append(rows, fromStoreRoute(row))
			}
			return buildSnapshot(rows), nil
		})
		if err != nil {

			slog.Error("proxyd: read proxyd_routes", "err", err)
			return nil, nil, err
		}
		return state.routes, state.proxies, nil
	} else {
		rr.manualMu.RLock()
		routes = append([]Route(nil), rr.manualRoutes...)
		rr.manualMu.RUnlock()
	}
	state := buildSnapshot(routes)
	return state.routes, state.proxies, nil
}

func buildSnapshot(routes []Route) routeSnapshot {

	procs := make(map[string]*httputil.ReverseProxy, len(routes))
	for _, r := range routes {
		if r.RedirectTo == "" {
			procs[r.Path] = buildRouteProxy(r)
		}
	}
	return routeSnapshot{routes, procs}
}

func (rr *routesResource) webSnapshot() ([]store.WebRoute, error) {
	if rr.st != nil {
		stored, err := rr.web.Read(context.Background(), rr.st.Obj(), store.OwnerScope(store.OwnerRoutd), func(context.Context) ([]store.WebRoute, error) {
			return rr.st.AllWebRoutes()
		})
		if err != nil {
			slog.Error("proxyd: read web_routes", "err", err)
			return nil, err
		}
		return stored, nil
	}
	rr.manualMu.RLock()
	defer rr.manualMu.RUnlock()
	return append([]store.WebRoute(nil), rr.manualWebRoutes...), nil
}

func (rr *routesResource) installManualWeb(routes []store.WebRoute) {
	rr.manualMu.Lock()
	rr.manualWebRoutes = append([]store.WebRoute(nil), routes...)
	rr.manualMu.Unlock()
}

func (rr *routesResource) installManual(routes []Route) {
	rr.manualMu.Lock()
	rr.manualRoutes = append([]Route(nil), routes...)
	rr.manualMu.Unlock()
}

func toStoreRoute(r Route) store.ProxydRoute {
	return store.ProxydRoute{
		Path: r.Path, Backend: r.Backend, Auth: r.Auth,
		GatedBy: r.GatedBy, PreserveHeaders: r.PreserveHeaders,
		StripPrefix: r.StripPrefix, RedirectTo: r.RedirectTo,
		Owner: r.Owner,
	}
}

func fromStoreRoute(r store.ProxydRoute) Route {
	return Route{
		Path: r.Path, Backend: r.Backend, Auth: r.Auth,
		GatedBy: r.GatedBy, PreserveHeaders: r.PreserveHeaders,
		StripPrefix: r.StripPrefix, RedirectTo: r.RedirectTo,
		Owner: r.Owner,
	}
}

func validateRoute(r Route, existing []Route, isUpdate bool) error {
	if !strings.HasPrefix(r.Path, "/") {
		return resreg.Errorf(http.StatusBadRequest, "path %q must start with '/'", r.Path)
	}
	if r.Backend == "" && r.RedirectTo == "" {
		return resreg.Errorf(http.StatusBadRequest, "backend or redirect_to is required")
	}
	if !validAuth[r.Auth] {
		return resreg.Errorf(http.StatusBadRequest, "auth %q not in {public,optional,user,operator}", r.Auth)
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

func (rr *routesResource) handle(ctx context.Context, x resreg.Execution) (any, error) {
	routes, _, err := rr.snapshot()
	if err != nil {
		return nil, resreg.Errorf(http.StatusServiceUnavailable, "route state unavailable: %v", err)
	}
	switch x.Action {
	case resreg.ActionList:
		out := make([]Route, 0, len(routes))
		out = append(out, routes...)

		return out, nil

	case resreg.ActionGet:
		path := normalisePath(x.Args["path"])
		if path == "" {
			return nil, resreg.Errorf(http.StatusBadRequest, "path required")
		}
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
		if err := validateRoute(r, routes, false); err != nil {
			return nil, err
		}

		r.Owner = store.RouteOwnerOperator

		if err := rr.st.CreateProxydRouteInto(x.Writes, toStoreRoute(r),
			resreg.Errorf(http.StatusConflict,
				"proxyd route %q is already taken", r.Path)); err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "persist: %v", err)
		}
		return r, nil

	case resreg.ActionUpdate:
		path := normalisePath(x.Args["path"])
		if path == "" {
			return nil, resreg.Errorf(http.StatusBadRequest, "path required")
		}
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

		if !store.RouteSurvivesGenerate(routes[idx].Owner) {
			return nil, resreg.Errorf(http.StatusConflict,
				"route %q is owned by %q and is rewritten by every `arizuko generate`; "+
					"DELETE it and POST your own to take the path over", path, routes[idx].Owner)
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
		if err := rr.st.UpdateProxydRouteInto(x.Writes, toStoreRoute(merged),
			resreg.Errorf(http.StatusNotFound, "route %q not found", path)); err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "persist: %v", err)
		}
		return merged, nil

	case resreg.ActionDelete:
		path := normalisePath(x.Args["path"])
		if path == "" {
			return nil, resreg.Errorf(http.StatusBadRequest, "path required")
		}

		if err := rr.st.DeleteProxydRouteInto(x.Writes, path); err != nil {
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

func routesAuthz(_ resreg.Caller, _ resreg.Action, _ resreg.Args) (string, map[string]string, error) {
	return "", nil, nil
}

func routesResourceDecl(rr *routesResource) resreg.Resource {
	r := resreg.Mount(resources.ProxydRoutesName)
	r.Authz = routesAuthz
	r.Handler = rr.handle
	r.Store = rr.st
	return r
}

const (
	callerWebd  = "service:webd"
	callerDashd = "service:dashd"
)

var trustedForwarders = []string{callerWebd, callerDashd}

func channelTrusted(r *http.Request, ks *auth.KeySet) bool {
	if r.Header.Get("X-User-Sub") == "" {
		return false
	}
	if ks == nil {
		return true
	}
	sub, err := auth.VerifyHTTP(r, ks)
	return err == nil && sub.Typ == "service" && slices.Contains(trustedForwarders, sub.Sub)
}

func callerFromHTTP(ks *auth.KeySet) resreg.CallerFromHTTPFunc {
	return func(r *http.Request) (resreg.Caller, error) {
		if !channelTrusted(r, ks) {
			return resreg.Caller{}, fmt.Errorf("missing or invalid identity")
		}

		sub := auth.BareSub(r.Header.Get("X-User-Sub"))
		var groups []string
		if hdr := r.Header.Get("X-User-Groups"); hdr != "" {
			_ = json.Unmarshal([]byte(hdr), &groups)
		}
		c := resreg.Caller{Sub: sub, Claims: map[string]string{}}
		if slices.Contains(groups, "**") {
			c.Claims["operator"] = "1"
		}
		return c, nil
	}
}

func insertProxydRoute(ctx context.Context, st *store.Store, r store.ProxydRoute) error {
	created, err := st.CreateProxydRoute(ctx, r)
	if err != nil {
		return err
	}
	if !created {
		return fmt.Errorf("proxyd route %q is already taken", r.Path)
	}
	return nil
}

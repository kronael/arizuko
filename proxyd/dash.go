package main

import (
	"context"
	"fmt"
	"html"
	"html/template"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/compose"
	"github.com/kronael/arizuko/store"
	"github.com/kronael/arizuko/theme"
)

func (s *server) handleDash(w http.ResponseWriter, r *http.Request) {
	if !auth.RequireOperator(w, r, s.ks, "proxyd") {
		return
	}
	routes, err := s.routes()
	if err != nil {
		http.Error(w, "Route state unavailable", http.StatusServiceUnavailable)
		return
	}

	var b strings.Builder
	fmt.Fprint(&b, `<div class="crumb"><a href="/dash/services/">arizuko hub</a> › proxyd</div>`)
	fmt.Fprint(&b, `<h1>proxyd</h1>`)
	fmt.Fprintf(&b, `<div class="tiles"><div class="tile"><div class="tile-n">%d</div>`+
		`<div class="tile-l">Routes</div></div></div>`, len(routes))
	b.WriteString(proxydRouteTable(routes))
	b.WriteString(routeCreateForm())
	_, _ = fmt.Fprint(w, theme.Page("proxyd", template.HTML(b.String())))
}

func (s *server) handleDashRouteCreate(w http.ResponseWriter, r *http.Request) {
	if !auth.RequireWrite(w, r, s.ks, "proxyd") {
		return
	}
	path := strings.TrimSpace(r.FormValue("path"))
	backend := strings.TrimSpace(r.FormValue("backend"))
	mode := strings.TrimSpace(r.FormValue("auth"))
	if path == "" || backend == "" {
		http.Error(w, "path and backend required", http.StatusBadRequest)
		return
	}
	// A route claiming core ground would silently take over the operator
	// console or the agent surface, so it is refused with the path it collides
	// with rather than written and puzzled over later.
	if claim := compose.ReservedRouteClaim("", path); claim != "" {
		auth.Forbidden(w, "proxyd", "Path "+path+" takes over the core route "+claim+".")
		return
	}

	if err := s.writeRoute(r.Context(), store.ProxydRoute{
		Path: path, Backend: backend, Auth: mode,
		Owner: store.RouteOwnerOperator,
	}); err != nil {
		slog.Error("dash route create", "path", path, "err", err)
		http.Error(w, "write failed", http.StatusInternalServerError)
		return
	}
	slog.Info("dash route create", "path", path, "backend", backend, "auth", mode)
	http.Redirect(w, r, "/dash/proxyd/", http.StatusSeeOther)
}

func (s *server) handleDashRouteDelete(w http.ResponseWriter, r *http.Request) {
	if !auth.RequireWrite(w, r, s.ks, "proxyd") {
		return
	}
	path := strings.TrimSpace(r.FormValue("path"))
	if path == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}
	ok, err := s.deleteRoute(r.Context(), path)
	if err != nil {
		slog.Error("dash route delete", "path", path, "err", err)
		http.Error(w, "write failed", http.StatusInternalServerError)
		return
	}

	if !ok {
		auth.Forbidden(w, "proxyd", "No route at "+path+".")
		return
	}
	slog.Info("dash route delete", "path", path)
	http.Redirect(w, r, "/dash/proxyd/", http.StatusSeeOther)
}

func (s *server) writeRoute(ctx context.Context, rt store.ProxydRoute) error {
	return insertProxydRoute(ctx, s.stRoutd, rt)
}

func (s *server) deleteRoute(ctx context.Context, path string) (bool, error) {
	return s.stRoutd.DeleteProxydRoute(ctx, path)
}

func proxydRouteTable(routes []Route) string {
	if len(routes) == 0 {
		return `<div class="banner-err">No routes. proxyd answers nothing.</div>`
	}
	sorted := make([]Route, len(routes))
	copy(sorted, routes)
	sort.SliceStable(sorted, func(i, j int) bool {
		return len(sorted[i].Path) > len(sorted[j].Path)
	})
	var b strings.Builder
	b.WriteString(`<h2>Routes</h2><p class="dim">Longest prefix wins; listed in match order.</p>`)
	b.WriteString(`<table><thead><tr><th>path</th><th>backend</th><th>auth</th>` +
		`<th></th></tr></thead><tbody>`)
	for _, rt := range sorted {
		action := ""

		if compose.ReservedRouteClaim("", rt.Path) == "" {
			action = fmt.Sprintf(
				`<form method="post" action="/dash/proxyd/delete">`+
					`<input type="hidden" name="path" value="%s">`+
					`<button type="submit">delete</button></form>`,
				html.EscapeString(rt.Path))
		} else {
			action = `<span class="dim">core</span>`
		}
		fmt.Fprintf(&b, `<tr><td><code>%s</code></td><td>%s</td><td>%s</td><td>%s</td></tr>`,
			html.EscapeString(rt.Path), html.EscapeString(rt.Backend),
			html.EscapeString(rt.Auth), action)
	}
	b.WriteString(`</tbody></table>`)
	return b.String()
}

func routeCreateForm() string {
	return `<h2>Add a route</h2>` +
		`<form method="post" action="/dash/proxyd/">` +
		`<input name="path" placeholder="/thing/" required> ` +
		`<input name="backend" placeholder="http://thingd:8080" required> ` +
		`<select name="auth">` +
		`<option value="user" selected>user</option>` +
		`<option value="optional">optional</option>` +
		`<option value="public">public</option>` +
		`</select> ` +
		`<button type="submit">add</button></form>`
}

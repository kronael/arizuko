package main

// dash.go is proxyd's operator dashboard at GET /dash/proxyd/ (spec 6/28, 6/30).
//
// proxyd is the odd one in the federated set: it IS the front door, so a path
// on its own mux never reaches s.route — which is where a routed request gets
// authenticated and stamped with X-User-*. Every handler here is therefore
// mounted behind s.requireAuth, the same middleware every other proxyd-served
// path uses, and only then meets the shared operator gate. A bare mount would
// serve the route table, and its write forms, to an unauthenticated caller.
//
// The write forms moved WITH the page on purpose. proxyd matches longest-prefix,
// so routing /dash/proxyd/ here captures every POST under it; shipping the read
// half alone would have taken the operator's only UI path to route CRUD away
// with it (6/30's rule: a copy leaves dashd only once the replacement is live).

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

// handleDash renders the live route table plus the create form.
func (s *server) handleDash(w http.ResponseWriter, r *http.Request) {
	if !auth.RequireOperator(w, r, s.ks, "proxyd") {
		return
	}
	var routes []Route
	if s.rr != nil {
		routes, _ = s.rr.snapshot()
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

// handleDashRouteCreate adds or replaces one route.
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
	// Owner EXPLICITLY: an empty owner defaults to RouteOwnerArizuko, which says
	// the release derived this route, and the next `arizuko generate` deletes
	// every arizuko-owned row it did not derive. An operator's own route would
	// vanish on the next deploy (BUGS L205).
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

// handleDashRouteDelete removes one route by path.
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
	// Deleting nothing is not a delete — the operator may have mistyped, and a
	// silent redirect would read as "the route is gone".
	if !ok {
		auth.Forbidden(w, "proxyd", "No route at "+path+".")
		return
	}
	slog.Info("dash route delete", "path", path)
	http.Redirect(w, r, "/dash/proxyd/", http.StatusSeeOther)
}

// writeRoute and deleteRoute run the SAME store writers the /v1 resource
// buffers, so the dashboard is a third face on one writer rather than a second
// one.
//
// They send a batch of their own. The /v1 face buffers instead, because its row
// has to ride with the audit row resreg records; the dashboard writes the route
// and nothing else.
func (s *server) writeRoute(ctx context.Context, rt store.ProxydRoute) error {
	return insertProxydRoute(ctx, s.stRoutd, rt)
}

func (s *server) deleteRoute(ctx context.Context, path string) (bool, error) {
	return s.stRoutd.DeleteProxydRoute(ctx, path)
}

// proxydRouteTable renders the table in MATCH order — longest prefix first,
// which is the order proxyd resolves in. Alphabetical order hides which of two
// overlapping prefixes wins, and that is the only question the table answers.
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
		// A core route is not deletable from here: compose re-writes it on the
		// next generate, so the button would appear to work and then undo
		// itself.
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

// routeCreateForm renders the add-route form. auth defaults to `user`: a route
// added without thinking about its gate must be the CLOSED one.
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

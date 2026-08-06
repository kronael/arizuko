package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
)

// The proxyd control plane. proxyd owns the `proxyd_routes` table, so dashd
// never touches it in SQL — every read and write here is an HTTP call to
// proxyd's /v1/proxyd_routes resource. proxyd writes the single audit_log row
// inside the mutation's own transaction (resreg emits in-tx), attributed to the
// X-User-Sub dashd forwards, so a second audit.Emit here would double-count the
// same operator action.

// proxydRoute is the wire shape of one row of proxyd's /v1/proxyd_routes.
// Mirrors store.ProxydRoute; kept local so dashd depends on the JSON contract
// rather than on proxyd's internals.
type proxydRoute struct {
	Path            string   `json:"path"`
	Backend         string   `json:"backend"`
	Auth            string   `json:"auth"`
	GatedBy         string   `json:"gated_by,omitempty"`
	PreserveHeaders []string `json:"preserve_headers,omitempty"`
	StripPrefix     bool     `json:"strip_prefix,omitempty"`
	RedirectTo      string   `json:"redirect_to,omitempty"`
}

// proxydCall performs one authenticated request against proxyd's /v1 face.
// proxyd trusts the stamped X-User-* headers only on a service:dashd ES256
// bearer (proxyd/resource.go trustedForwarders), and it authorizes the STAMPED
// operator, not dashd — so the caller's identity must ride along on every call.
// Returns the response body and status; a non-2xx is the caller's to surface.
func (d *dash) proxydCall(ctx context.Context, r *http.Request, method, path string, body any) ([]byte, int, error) {
	if d.proxydURL == "" {
		return nil, 0, fmt.Errorf("PROXYD_URL not configured")
	}
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		rdr = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, d.proxydURL+path, rdr)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-User-Sub", r.Header.Get("X-User-Sub"))
	if g := r.Header.Get("X-User-Groups"); g != "" {
		req.Header.Set("X-User-Groups", g)
	}
	if d.svc != nil {
		tok, terr := d.svc(ctx)
		if terr != nil {
			return nil, 0, terr
		}
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	buf, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return buf, resp.StatusCode, err
}

// proxydUpstreamErr renders a non-2xx from proxyd as one operator-readable
// sentence. proxyd answers {"error":"..."}; fall back to the raw body.
func proxydUpstreamErr(status int, body []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	msg := strings.TrimSpace(string(body))
	if json.Unmarshal(body, &e) == nil && e.Error != "" {
		msg = e.Error
	}
	if status == http.StatusForbidden {
		return fmt.Sprintf("proxyd refused (403 %s) — your account needs an operator grant covering the proxyd_routes actions", msg)
	}
	return fmt.Sprintf("proxyd said %d: %s", status, msg)
}

// handleProxyd renders GET /dash/proxyd/ — the reverse-proxy cockpit: every URL
// prefix proxyd serves, the backend behind it, and who may reach it. Reads
// proxyd's /v1/proxyd_routes over HTTP. Operator-only.
func (d *dash) handleProxyd(w http.ResponseWriter, r *http.Request) {
	if !d.requireOperator(w, r) {
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageTopFor(w, r, "proxyd",
		struct{ Href, Label string }{"/dash/services/", "Services"},
		struct{ Href, Label string }{"", "proxyd"},
	)
	fmt.Fprint(w, `<p class="dim">Every web address this instance answers. `+
		`A request whose URL starts with a <code>path</code> below is forwarded to that <code>backend</code>. `+
		`Changes take effect immediately — no restart.</p>`)

	switch strings.TrimSpace(r.URL.Query().Get("msg")) {
	case "added":
		fmt.Fprint(w, htmlBanner("ok", "route added — it is serving now"))
	case "deleted":
		fmt.Fprint(w, htmlBanner("ok", "route removed — that address now returns 404"))
	}
	if e := strings.TrimSpace(r.URL.Query().Get("err")); e != "" {
		fmt.Fprint(w, htmlBanner("err", e))
	}

	if d.writeProxydRoutes(w, r) {
		writeProxydAddForm(w)
	}
	pageClose(w, r)
}

// writeProxydRoutes fetches and renders the route table. false when the fetch
// failed — the add form is then withheld, since a form that cannot show the
// current table would let an operator create a duplicate path blind.
func (d *dash) writeProxydRoutes(w http.ResponseWriter, r *http.Request) bool {
	if d.proxydURL == "" {
		fmt.Fprint(w, htmlBanner("warn", "proxyd unreachable — PROXYD_URL is not configured for dashd"))
		return false
	}
	body, status, err := d.proxydCall(r.Context(), r, http.MethodGet, "/v1/proxyd_routes", nil)
	if err != nil {
		slog.Warn("proxyd page: list", "err", err)
		fmt.Fprint(w, htmlBanner("err", "could not reach proxyd: "+err.Error()))
		return false
	}
	if status < 200 || status >= 300 {
		slog.Warn("proxyd page: list status", "status", status)
		fmt.Fprint(w, htmlBanner("err", proxydUpstreamErr(status, body)))
		return false
	}
	var out struct {
		Routes []proxydRoute `json:"routes"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		slog.Warn("proxyd page: decode", "err", err)
		fmt.Fprint(w, htmlBanner("err", "proxyd returned something dashd could not read: "+err.Error()))
		return false
	}

	var rows [][]string
	for _, rt := range out.Routes {
		del := fmt.Sprintf(
			`<form method="post" action="/dash/proxyd/delete" class="form-inline"`+
				` onsubmit="return confirm('Remove %s? Anyone opening that address will get a 404 straight away.')">`+
				`<input type="hidden" name="path" value="%s">`+
				`<button class="btn-danger btn-sm" type="submit">delete</button></form>`,
			esc(rt.Path), esc(rt.Path))
		rows = append(rows, []string{
			`<code>` + esc(rt.Path) + `</code>`,
			proxydTargetCell(rt),
			fmt.Sprintf(`<span class="status-%s">%s</span>`, proxydAuthClass(rt.Auth), esc(proxydAuthLabel(rt.Auth))),
			proxydFlagsCell(rt),
			del,
		})
	}
	fmt.Fprint(w, htmlTable(
		[]string{"Path", "Goes to", "Who can open it", "Notes", ""},
		rows,
		"No routes. proxyd is answering 404 for every address — check PROXYD_ROUTES_JSON in the instance .env."))
	return true
}

// proxydTargetCell renders where a route sends the request: a backend service
// or, for a redirect route, the address the browser is sent to instead.
func proxydTargetCell(rt proxydRoute) string {
	if rt.RedirectTo != "" {
		return `<span class="dim">redirect &rarr;</span> <code>` + esc(rt.RedirectTo) + `</code>`
	}
	return `<code>` + esc(rt.Backend) + `</code>`
}

// proxydAuthLabel spells out the `auth` field for an operator who has not read
// the spec: the wire values are public/user/operator.
func proxydAuthLabel(a string) string {
	switch a {
	case "public":
		return "anyone"
	case "user":
		return "signed-in users"
	case "operator":
		return "operators only"
	default:
		return a
	}
}

// proxydAuthClass colours the audience: a public path is the one worth
// noticing, so it reads warn; the gated ones read ok.
func proxydAuthClass(a string) string {
	if a == "public" {
		return "warn"
	}
	return statusOK
}

// proxydFlagsCell renders the per-route options that change how the request
// reaches the backend. Empty when the route is plain.
func proxydFlagsCell(rt proxydRoute) string {
	var parts []string
	if rt.StripPrefix {
		parts = append(parts, "path prefix removed")
	}
	if rt.GatedBy != "" {
		parts = append(parts, "set up by <code>"+esc(rt.GatedBy)+"</code>")
	}
	if len(rt.PreserveHeaders) > 0 {
		parts = append(parts, "keeps "+esc(strings.Join(rt.PreserveHeaders, ", ")))
	}
	if len(parts) == 0 {
		return `<span class="dim">&mdash;</span>`
	}
	return `<span class="dim">` + strings.Join(parts, " &middot; ") + `</span>`
}

// writeProxydAddForm renders the add-route form. Redirect routes and header
// allowlists are not offered — proxyd cannot PATCH `redirect_to` at all, so a
// half-editable field would be a trap; those still arrive via the instance's
// PROXYD_ROUTES_JSON.
func writeProxydAddForm(w http.ResponseWriter) {
	fmt.Fprint(w, htmlSection("Add route",
		`<form method="post" action="/dash/proxyd/">`+
			htmlFormRow("Path", `<input type="text" name="path" placeholder="/myapp/" required size="40">`)+
			htmlFormRow("Backend", `<input type="text" name="backend" placeholder="http://myapp:8080" required size="40">`)+
			htmlFormRow("Who can open it", `<select name="auth">`+
				`<option value="user">signed-in users</option>`+
				`<option value="operator">operators only</option>`+
				`<option value="public">anyone</option>`+
				`</select>`)+
			htmlFormRow("Strip the path prefix before forwarding",
				`<input type="checkbox" name="strip_prefix" value="1">`)+
			`<p><button class="btn-primary" type="submit">add route</button></p>`+
			`</form>`+
			`<p class="dim">A path ending in <code>/</code> matches everything under it; without one it must match exactly. `+
			`Choose <b>anyone</b> only when the backend checks the caller itself — a webhook verifying its own signature, for example.</p>`))
}

// proxydRedirect sends the operator back to the page with either a success
// banner or the upstream failure spelled out. Failures MUST reach the operator:
// a mutation that silently did nothing is worse than an error.
func proxydRedirect(w http.ResponseWriter, r *http.Request, msg, errMsg string) {
	dest := "/dash/proxyd/?msg=" + msg
	if errMsg != "" {
		dest = "/dash/proxyd/?err=" + url.QueryEscape(errMsg)
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

// handleProxydRouteCreate handles POST /dash/proxyd/ — create a route through
// proxyd's POST /v1/proxyd_routes. Operator-only; proxyd audits the write.
func (d *dash) handleProxydRouteCreate(w http.ResponseWriter, r *http.Request) {
	if !d.requireOperator(w, r) {
		return
	}
	if !requireSameOrigin(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	route := proxydRoute{
		Path:        strings.TrimSpace(r.FormValue("path")),
		Backend:     strings.TrimSpace(r.FormValue("backend")),
		Auth:        strings.TrimSpace(r.FormValue("auth")),
		StripPrefix: r.FormValue("strip_prefix") == "1",
	}
	if route.Path == "" || route.Backend == "" {
		http.Error(w, "path and backend required", http.StatusBadRequest)
		return
	}
	if route.Auth == "" {
		route.Auth = "user"
	}
	body, status, err := d.proxydCall(r.Context(), r, http.MethodPost, "/v1/proxyd_routes", route)
	if err != nil {
		slog.Warn("proxyd route create", "path", route.Path, "err", err)
		proxydRedirect(w, r, "", "could not reach proxyd: "+err.Error())
		return
	}
	if status < 200 || status >= 300 {
		slog.Warn("proxyd route create", "path", route.Path, "status", status)
		proxydRedirect(w, r, "", proxydUpstreamErr(status, body))
		return
	}
	slog.Info("proxyd route added", "path", route.Path, "backend", route.Backend)
	proxydRedirect(w, r, "added", "")
}

// handleProxydRouteDelete handles POST /dash/proxyd/delete — withdraw a route
// through proxyd's DELETE /v1/proxyd_routes/{path}. Operator-only; proxyd
// audits the write.
func (d *dash) handleProxydRouteDelete(w http.ResponseWriter, r *http.Request) {
	if !d.requireOperator(w, r) {
		return
	}
	if !requireSameOrigin(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	path := strings.TrimSpace(r.FormValue("path"))
	if path == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}
	body, status, err := d.proxydCall(r.Context(), r, http.MethodDelete,
		"/v1/proxyd_routes/"+url.PathEscape(path), nil)
	if err != nil {
		slog.Warn("proxyd route delete", "path", path, "err", err)
		proxydRedirect(w, r, "", "could not reach proxyd: "+err.Error())
		return
	}
	if status < 200 || status >= 300 {
		slog.Warn("proxyd route delete", "path", path, "status", status)
		proxydRedirect(w, r, "", proxydUpstreamErr(status, body))
		return
	}
	slog.Info("proxyd route deleted", "path", path)
	proxydRedirect(w, r, "deleted", "")
}

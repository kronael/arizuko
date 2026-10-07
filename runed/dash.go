package runed

// dash.go is runed's operator dashboard at GET /dash/runed/ (spec 6/28, 6/30):
// one handler in the daemon that owns the data, gated by auth.RequireOperator,
// reading the in-process readers its /v1 handlers read.

import (
	"fmt"
	"html"
	"html/template"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/substrate"
	"github.com/kronael/arizuko/theme"
)

// runsShown caps the run table: the recent window, not the archive.
const runsShown = 40

// handleDash renders capacity plus the recent run table.
func (s *Server) handleDash(w http.ResponseWriter, r *http.Request) {
	if !auth.RequireOperator(w, r, s.dashKeys, "runed") {
		return
	}
	// state= narrows the table. Empty is every state, which is the landing view.
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	ctx := r.Context()
	runs, err := s.db.RecentSpawns(ctx, state, strings.TrimSpace(r.URL.Query().Get("folder")), runsShown)
	if err != nil {
		slog.Error("dash: list spawns", "err", err)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, theme.Page("runed",
			template.HTML(`<div class="banner-err">Could not read the spawn table.</div>`)))
		return
	}

	var b strings.Builder
	fmt.Fprint(&b, `<div class="crumb"><a href="/dash/services/">arizuko hub</a> › runed</div>`)
	fmt.Fprint(&b, `<h1>runed</h1>`)

	active, _ := s.db.LiveSpawnCount(ctx)
	agents, _ := s.db.LiveAgentSpawnCount(ctx)
	fmt.Fprint(&b, `<div class="tiles">`)
	runedTile(&b, "Live spawns", active)
	runedTile(&b, "Live agents", agents)
	runedTile(&b, "Recent runs", len(runs))
	fmt.Fprint(&b, `</div>`)
	if s.sub.Declared.Backend != "" {
		b.WriteString(substrateLine(s.sub))
	}

	b.WriteString(stateFilter(state))
	b.WriteString(runTable(runs))
	_, _ = fmt.Fprint(w, theme.Page("runed", template.HTML(b.String())))
}

// handleDashKill stops one run. RequireWrite: operator scope AND same origin,
// because killing a run from a browser is a cookie-authenticated mutation.
func (s *Server) handleDashKill(w http.ResponseWriter, r *http.Request) {
	if !auth.RequireWrite(w, r, s.dashKeys, "runed") {
		return
	}
	runID := strings.TrimSpace(r.FormValue("run_id"))
	if runID == "" {
		http.Error(w, "run_id required", http.StatusBadRequest)
		return
	}
	// Kill is idempotent: killing an exited run is a 200 no-op.
	if err := s.mgr.Kill(r.Context(), runID); err != nil {
		slog.Error("dash kill", "run_id", runID, "err", err)
		http.Error(w, "kill failed", http.StatusInternalServerError)
		return
	}
	slog.Info("dash kill", "run_id", runID)
	http.Redirect(w, r, "/dash/runed/", http.StatusSeeOther)
}

// substrateLine renders what the substrate delivers and, when that is less
// than it declares, why.
func substrateLine(sub substrate.Effective) string {
	d := sub.Delivered
	line := fmt.Sprintf(`<p class="sub">Substrate: %s · %s · egress %s`,
		d.Backend, d.Persistence, d.Egress)
	if sub.Reason != "" {
		line += fmt.Sprintf(` (declared %s — %s)`,
			sub.Declared.Egress, html.EscapeString(sub.Reason))
	}
	return line + `</p>`
}

func runedTile(b *strings.Builder, label string, n int) {
	fmt.Fprintf(b, `<div class="tile"><div class="tile-n">%d</div><div class="tile-l">%s</div></div>`,
		n, html.EscapeString(label))
}

// runStates are the filter links, listed rather than derived from the rows on
// screen: a state with no current runs must still be selectable.
var runStates = []string{"", "queued", "running", "exited", "killed"}

func stateFilter(current string) string {
	var b strings.Builder
	b.WriteString(`<p class="filter">`)
	for _, st := range runStates {
		label, q := st, "?state="+st
		if st == "" {
			label, q = "all", ""
		}
		if st == current {
			fmt.Fprintf(&b, `<strong>%s</strong> `, html.EscapeString(label))
			continue
		}
		fmt.Fprintf(&b, `<a href="/dash/runed/%s">%s</a> `,
			html.EscapeString(q), html.EscapeString(label))
	}
	b.WriteString(`</p>`)
	return b.String()
}

// isLive reports whether a run can still be killed; the same test Manager.Kill
// applies.
func isLive(state string) bool { return state == "running" || state == "queued" }

// runTable renders the recent runs, with a kill button on the live ones only.
func runTable(rows []Spawn) string {
	if len(rows) == 0 {
		return `<p class="ok">No runs.</p>`
	}
	var b strings.Builder
	b.WriteString(`<table><thead><tr><th>run</th><th>folder</th><th>kind</th>` +
		`<th>state</th><th>outcome</th><th>started</th><th></th></tr></thead><tbody>`)
	for _, sp := range rows {
		action := ""
		if isLive(sp.State) {
			action = fmt.Sprintf(
				`<form method="post" action="/dash/runed/kill">`+
					`<input type="hidden" name="run_id" value="%s">`+
					`<button type="submit">kill</button></form>`,
				html.EscapeString(sp.RunID))
		}
		fmt.Fprintf(&b, `<tr><td><code>%s</code></td><td>%s</td><td>%s</td>`+
			`<td class="status-%s">%s</td><td>%s</td><td>%s</td><td>%s</td></tr>`,
			html.EscapeString(sp.RunID), html.EscapeString(sp.Folder),
			html.EscapeString(sp.Kind), html.EscapeString(sp.State),
			html.EscapeString(sp.State), html.EscapeString(sp.Outcome),
			html.EscapeString(sp.StartedAt), action)
	}
	b.WriteString(`</tbody></table>`)
	return b.String()
}

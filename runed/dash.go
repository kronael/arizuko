package runed

// dash.go is runed's operator dashboard at GET /dash/runed/ (spec 6/28, 6/30).
// Same shape as routd's: one handler in the daemon that owns the data, gated by
// auth.RequireOperator, reading the in-process readers its /v1 handlers read.
//
// dashd rendered this page from a direct handle on runed.db and killed runs by
// POSTing to runed's own /v1 — so the read crossed a DB boundary the write
// already respected. Both live here now.

import (
	"fmt"
	"html"
	"html/template"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/theme"
)

// runsShown caps the run table. A busy instance accumulates thousands of
// spawns; the overview is the recent window, not the archive.
const runsShown = 40

// handleDash renders capacity plus the recent run table.
func (s *Server) handleDash(w http.ResponseWriter, r *http.Request) {
	if !auth.RequireOperator(w, r, s.dashKeys, "runed") {
		return
	}
	// state= narrows the table. Empty is every state, which is the landing view.
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	runs, err := s.db.ListSpawns(state, strings.TrimSpace(r.URL.Query().Get("folder")), runsShown)
	if err != nil {
		slog.Error("dash: list spawns", "err", err)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, theme.Page("runed",
			template.HTML(`<div class="banner-err">Could not read the spawn table.</div>`)))
		return
	}

	var b strings.Builder
	fmt.Fprint(&b, `<div class="crumb"><a href="/dash/services/">arizuko hub</a> › runed</div>`)
	fmt.Fprint(&b, `<h1>runed</h1>`)

	active, _ := s.db.ActiveCount()
	agents, _ := s.db.ActiveAgentCount()
	fmt.Fprint(&b, `<div class="tiles">`)
	runedTile(&b, "Live spawns", active)
	runedTile(&b, "Live agents", agents)
	runedTile(&b, "Recent runs", len(runs))
	fmt.Fprint(&b, `</div>`)

	b.WriteString(stateFilter(state))
	b.WriteString(runTable(runs))
	fmt.Fprint(w, theme.Page("runed", template.HTML(b.String())))
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
	// Kill is idempotent by design — killing an exited run is a 200 no-op — so
	// this reports what it did rather than implying the container was running.
	if err := s.mgr.Kill(runID); err != nil {
		slog.Error("dash kill", "run_id", runID, "err", err)
		http.Error(w, "kill failed", http.StatusInternalServerError)
		return
	}
	slog.Info("dash kill", "run_id", runID)
	http.Redirect(w, r, "/dash/runed/", http.StatusSeeOther)
}

func runedTile(b *strings.Builder, label string, n int) {
	fmt.Fprintf(b, `<div class="tile"><div class="tile-n">%d</div><div class="tile-l">%s</div></div>`,
		n, html.EscapeString(label))
}

// runStates are the filter links. Listed rather than derived from the rows on
// screen: a state with no current runs must still be selectable, or the filter
// disappears exactly when an operator wants it.
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

// isLive reports whether a run can still be killed. Only these two states hold
// a container and a folder's slot — the same test Manager.Kill applies.
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

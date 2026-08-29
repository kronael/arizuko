package routd

// dash.go is routd's operator dashboard at GET /dash/routd/ (spec 6/28, 6/30).
//
// It is the FIRST per-daemon dashboard outside onbod and timed, so it is the
// shape the other six copy: one handler in the daemon that owns the data,
// gated by auth.RequireOperator, rendered with theme, reading the same
// in-process readers routd's own /v1 handlers read. No direct read of another
// daemon's DB, and no fourth surface — this is a third face on the handler set
// 5/17 already established.
//
// 6/30 lists six pages for routd (overview, queue, channels, errored, routes,
// cost) and the /v1 additions four of them need. This file ships the overview,
// which needs none: every figure on it comes from a reader routd already has.
// The remaining five are listed in 6/30 and stay unbuilt rather than stubbed —
// a page that renders an empty table is worse than a missing link, because the
// hub tile goes green either way.

import (
	"fmt"
	"html"
	"html/template"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/theme"
)

// erroredPreview caps the errored-chat table. An instance with a broken adapter
// can carry hundreds of errored chats and the overview is a summary, not the
// errored page 6/30 specifies.
const erroredPreview = 15

// dashSnapshot is everything the overview renders. Built in one place so the
// template has no logic and a missing reader is a compile error, not a blank
// panel.
type dashSnapshot struct {
	Groups   int
	Routes   int
	Errored  []ErroredChat
	Pending  int
	Instance string
}

// snapshot reads routd's own state for the overview. Every field comes from a
// reader that already backs a /v1 handler, so the page cannot drift from the
// API: there is one reader per figure, not a dashboard copy of it.
func (s *Server) snapshot() dashSnapshot {
	snap := dashSnapshot{Instance: s.instanceName()}
	snap.Groups = len(s.db.AllGroups())
	if routes, err := s.db.Routes(); err == nil {
		snap.Routes = len(routes)
	}
	// isRoot: the dashboard is operator-only, so the unscoped read is the
	// correct one — the gate above decided the caller may see every folder.
	if errored, err := s.db.ErroredChats("", true); err == nil {
		snap.Errored = errored
	}
	if pending, err := s.db.PendingOutbound(time.Now(), 500); err == nil {
		snap.Pending = len(pending)
	}
	return snap
}

// instanceName is the deployment's name for the breadcrumb. Identity is
// configured, never derived from a path (CLAUDE.md).
func (s *Server) instanceName() string {
	if s.webHost != "" {
		return s.webHost
	}
	return "routd"
}

// handleDash renders the overview.
func (s *Server) handleDash(w http.ResponseWriter, r *http.Request) {
	if !auth.RequireOperator(w, r, s.dashKeys, "routd") {
		return
	}
	snap := s.snapshot()

	var b strings.Builder
	fmt.Fprintf(&b, `<div class="crumb"><a href="/dash/services/">arizuko hub</a> › routd</div>`)
	fmt.Fprintf(&b, `<h1>routd</h1><p class="dim">%s</p>`, html.EscapeString(snap.Instance))

	b.WriteString(`<div class="tiles">`)
	tile(&b, "Groups", snap.Groups, "")
	tile(&b, "Routes", snap.Routes, "")
	tile(&b, "Errored chats", len(snap.Errored), warnIf(len(snap.Errored) > 0))
	tile(&b, "Pending outbound", snap.Pending, warnIf(snap.Pending > 0))
	b.WriteString(`</div>`)

	b.WriteString(erroredTable(snap.Errored))
	fmt.Fprint(w, theme.Page("routd", template.HTML(b.String())))
}

// warnIf returns the tile's state class. Two states only: a dashboard that
// grades severity invents thresholds nobody agreed on.
func warnIf(bad bool) string {
	if bad {
		return "warn"
	}
	return ""
}

// tile renders one count.
func tile(b *strings.Builder, label string, n int, state string) {
	fmt.Fprintf(b, `<div class="tile %s"><div class="tile-n">%d</div><div class="tile-l">%s</div></div>`,
		html.EscapeString(state), n, html.EscapeString(label))
}

// erroredTable renders the errored-chat preview, newest first. Errored chats
// are the one figure an operator acts on from the overview, so they get rows
// rather than only a count.
func erroredTable(rows []ErroredChat) string {
	if len(rows) == 0 {
		return `<p class="ok">No errored chats.</p>`
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].LastAt.After(rows[j].LastAt) })
	shown := rows
	if len(shown) > erroredPreview {
		shown = shown[:erroredPreview]
	}
	var b strings.Builder
	b.WriteString(`<h2>Errored chats</h2><table><thead><tr>` +
		`<th>chat</th><th>folder</th><th>count</th><th>last</th></tr></thead><tbody>`)
	for _, e := range shown {
		fmt.Fprintf(&b, `<tr><td>%s</td><td>%s</td><td>%d</td><td>%s</td></tr>`,
			html.EscapeString(e.ChatJID), html.EscapeString(e.RoutedTo), e.Count,
			html.EscapeString(e.LastAt.Format(time.RFC3339)))
	}
	b.WriteString(`</tbody></table>`)
	if len(rows) > len(shown) {
		fmt.Fprintf(&b, `<p class="dim">%d more not shown.</p>`, len(rows)-len(shown))
	}
	return b.String()
}

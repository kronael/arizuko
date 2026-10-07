package routd

// dash.go is routd's operator dashboard at GET /dash/routd/ (spec 6/28, 6/30):
// one handler in the daemon that owns the data, gated by auth.RequireOperator,
// reading the same in-process readers routd's /v1 handlers read — a third face
// on the handler set, never a read of another daemon's DB. Only the overview
// is built; the other five pages 6/30 lists stay unbuilt rather than stubbed,
// because a page that renders an empty table turns the hub tile green anyway.

import (
	"fmt"
	"html"
	"html/template"
	"net/http"
	"sort"
	"strings"
	"time"

	"log/slog"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/theme"
)

// erroredPreview caps the errored-chat table: the overview is a summary, not
// the errored page 6/30 specifies.
const erroredPreview = 15

// dashSnapshot is everything the overview renders, built in one place so the
// template has no logic and a missing reader is a compile error.
type dashSnapshot struct {
	Groups   int
	Routes   int
	Errored  []ErroredChat
	Dead     []core.Message
	Pending  int
	Instance string
}

// snapshot reads routd's own state; every field comes from a reader that
// already backs a /v1 handler, so the page cannot drift from the API.
func (s *Server) snapshot() dashSnapshot {
	snap := dashSnapshot{Instance: s.instanceName()}
	snap.Groups = len(s.db.AllGroups())
	if routes, err := s.db.Routes(); err == nil {
		snap.Routes = len(routes)
	}
	// The dashboard is operator-only, so the unscoped read is the correct one.
	if errored, err := s.db.ErroredChats("", true); err == nil {
		snap.Errored = errored
	}
	if pending, err := s.db.PendingOutbound(time.Now(), 500); err == nil {
		snap.Pending = len(pending)
	}
	if dead, err := s.db.DeadLetters("", erroredPreview); err == nil {
		snap.Dead = make([]core.Message, len(dead))
		for i, p := range dead {
			snap.Dead[i] = p.Message
		}
	}
	return snap
}

// instanceName is the deployment's name for the breadcrumb; identity is
// configured, never derived from a path.
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
	b.WriteString(deadLetterTable(snap.Dead))
	_, _ = fmt.Fprint(w, theme.Page("routd", template.HTML(b.String())))
}

// handleDashRetry clears a chat's errored rows so the loop feeds them again.
// RequireWrite is RequireOperator plus the same-origin check: this is a
// cookie-authenticated mutation reachable from a browser.
func (s *Server) handleDashRetry(w http.ResponseWriter, r *http.Request) {
	if !auth.RequireWrite(w, r, s.dashKeys, "routd") {
		return
	}
	chatJID := strings.TrimSpace(r.FormValue("chat_jid"))
	if chatJID == "" {
		http.Error(w, "chat_jid required", http.StatusBadRequest)
		return
	}
	n, err := s.db.ClearErrored(chatJID)
	if err != nil {
		slog.Error("dash retry", "chat_jid", chatJID, "err", err)
		http.Error(w, "write failed", http.StatusInternalServerError)
		return
	}
	// A retry that cleared nothing is not a retry — say so.
	if n == 0 {
		auth.Forbidden(w, "routd", "No errored rows for "+chatJID+".")
		return
	}
	slog.Info("dash retry", "chat_jid", chatJID, "rows", n)
	http.Redirect(w, r, "/dash/routd/", http.StatusSeeOther)
}

// handleDashReplay re-queues one dead-lettered outbound message.
func (s *Server) handleDashReplay(w http.ResponseWriter, r *http.Request) {
	if !auth.RequireWrite(w, r, s.dashKeys, "routd") {
		return
	}
	id := strings.TrimSpace(r.FormValue("id"))
	if id == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	msg, found := s.db.DeadLetterByID(id)
	if !found {
		auth.Forbidden(w, "routd", "No dead-lettered message "+id+".")
		return
	}
	ok, err := s.db.ReplayDeadLetter(msg)
	if err != nil {
		slog.Error("dash replay", "id", id, "err", err)
		http.Error(w, "write failed", http.StatusInternalServerError)
		return
	}
	if !ok {
		auth.Forbidden(w, "routd", "No dead-lettered message "+id+".")
		return
	}
	slog.Info("dash replay", "id", id)
	http.Redirect(w, r, "/dash/routd/", http.StatusSeeOther)
}

// deadLetterTable renders the dead-lettered outbound queue with a replay
// button per row: past every retry, the operator's action is the only thing
// that moves them.
func deadLetterTable(rows []core.Message) string {
	if len(rows) == 0 {
		return `<p class="ok">No dead-lettered outbound.</p>`
	}
	var b strings.Builder
	b.WriteString(`<h2>Dead-lettered outbound</h2><table><thead><tr>` +
		`<th>chat</th><th>content</th><th></th></tr></thead><tbody>`)
	for _, m := range rows {
		fmt.Fprintf(&b, `<tr><td>%s</td><td>%s</td><td>`+
			`<form method="post" action="/dash/routd/replay">`+
			`<input type="hidden" name="id" value="%s">`+
			`<button type="submit">replay</button></form></td></tr>`,
			html.EscapeString(m.ChatJID), html.EscapeString(truncate(m.Content, 80)),
			html.EscapeString(m.ID))
	}
	b.WriteString(`</tbody></table>`)
	return b.String()
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

// erroredTable renders the errored-chat preview, newest first — the one
// figure an operator acts on from the overview, so it gets rows, not a count.
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
		`<th>chat</th><th>folder</th><th>count</th><th>last</th><th></th></tr></thead><tbody>`)
	for _, e := range shown {
		fmt.Fprintf(&b, `<tr><td>%s</td><td>%s</td><td>%d</td><td>%s</td><td>`+
			`<form method="post" action="/dash/routd/retry">`+
			`<input type="hidden" name="chat_jid" value="%s">`+
			`<button type="submit">retry</button></form></td></tr>`,
			html.EscapeString(e.ChatJID), html.EscapeString(e.RoutedTo), e.Count,
			html.EscapeString(e.LastAt.Format(time.RFC3339)), html.EscapeString(e.ChatJID))
	}
	b.WriteString(`</tbody></table>`)
	if len(rows) > len(shown) {
		fmt.Fprintf(&b, `<p class="dim">%d more not shown.</p>`, len(rows)-len(shown))
	}
	return b.String()
}

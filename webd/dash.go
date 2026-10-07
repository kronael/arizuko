package main

// dash.go is webd's operator dashboard at GET /dash/webd/ (spec 6/28, 6/30).
//
// webd had no dashd page to migrate, so this is a pure addition: the SSE hub's
// state lives only in webd's memory, which is exactly why 6/28 puts the page in
// the owning daemon — a DB snapshot cannot see a live subscriber.
//
// It is read-only by design. Route tokens and web routes are routd-owned and
// rendered on routd's page; duplicating their controls here would be the second
// renderer 6/28 exists to prevent.

import (
	"fmt"
	"html"
	"html/template"
	"net/http"
	"sort"
	"strings"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/theme"
)

// hubStatus is one SSE key's live subscriber count.
type hubStatus struct {
	Key  string
	Subs int
}

// status snapshots the hub. Memory-only, and taken under the hub's own lock so
// the page cannot read a half-updated map.
func (h *hub) status() []hubStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]hubStatus, 0, len(h.subs))
	for k, chans := range h.subs {
		out = append(out, hubStatus{Key: k, Subs: len(chans)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func (s *server) handleDash(w http.ResponseWriter, r *http.Request) {
	if !auth.RequireOperator(w, r, s.ks, "webd") {
		return
	}
	var keys []hubStatus
	if s.hub != nil {
		keys = s.hub.status()
	}
	total := 0
	for _, k := range keys {
		total += k.Subs
	}

	var b strings.Builder
	fmt.Fprint(&b, `<div class="crumb"><a href="/dash/services/">arizuko hub</a> › webd</div>`)
	fmt.Fprint(&b, `<h1>webd</h1>`)
	fmt.Fprint(&b, `<div class="tiles">`)
	webdTile(&b, "SSE keys", len(keys), maxHubKeys)
	webdTile(&b, "Subscribers", total, 0)
	fmt.Fprint(&b, `</div>`)
	b.WriteString(hubTable(keys))
	_, _ = fmt.Fprint(w, theme.Page("webd", template.HTML(b.String())))
}

// webdTile renders a count, with its ceiling when it has one — a subscriber
// count means little without the cap it is approaching.
func webdTile(b *strings.Builder, label string, n, capacity int) {
	val := fmt.Sprintf("%d", n)
	if capacity > 0 {
		val = fmt.Sprintf("%d/%d", n, capacity)
	}
	state := ""
	if capacity > 0 && n >= capacity {
		state = "warn"
	}
	fmt.Fprintf(b, `<div class="tile %s"><div class="tile-n">%s</div><div class="tile-l">%s</div></div>`,
		html.EscapeString(state), html.EscapeString(val), html.EscapeString(label))
}

// hubTable lists the live SSE keys. A key is `<chat_jid>|<topic>`, which is
// how publish addresses them — rendered verbatim rather than split, because the
// key IS the thing an operator matches against a stuck browser.
func hubTable(keys []hubStatus) string {
	if len(keys) == 0 {
		return `<p class="ok">No live SSE subscribers.</p>`
	}
	var b strings.Builder
	b.WriteString(`<h2>Live SSE keys</h2><table><thead><tr><th>key</th>` +
		`<th>subscribers</th></tr></thead><tbody>`)
	for _, k := range keys {
		fmt.Fprintf(&b, `<tr><td><code>%s</code></td><td>%d</td></tr>`,
			html.EscapeString(k.Key), k.Subs)
	}
	b.WriteString(`</tbody></table>`)
	return b.String()
}

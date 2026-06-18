package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kronael/arizuko/audit"
)

// handleRoutd renders GET /dash/routd/ — the message-router cockpit: errored
// chats (with a per-chat retry), a pending-outbound count, and route/group
// totals. Errored messages + pending outbound come from messages.db (d.db);
// the route/group totals come from routd.db (d.dbRoutd). Operator-only.
func (d *dash) handleRoutd(w http.ResponseWriter, r *http.Request) {
	if !d.requireOperator(w, r) {
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageTopFor(w, r, "routd",
		struct{ Href, Label string }{"/dash/services/", "Services"},
		struct{ Href, Label string }{"", "routd"},
	)

	if d.db == nil {
		fmt.Fprint(w, htmlBanner("err", "messages store unavailable"))
		pageClose(w, r)
		return
	}

	if msg := strings.TrimSpace(r.URL.Query().Get("msg")); msg == "retried" {
		fmt.Fprint(w, htmlBanner("ok", "errored messages cleared — they will be re-dispatched"))
	}

	// Stats — route + group totals from routd.db.
	routeCount, groupCount := -1, -1
	if d.dbRoutd != nil {
		if err := d.dbRoutd.QueryRow(`SELECT COUNT(*) FROM routes`).Scan(&routeCount); err != nil {
			slog.Warn("routd page: route count", "err", err)
		}
		if err := d.dbRoutd.QueryRow(`SELECT COUNT(*) FROM groups`).Scan(&groupCount); err != nil {
			slog.Warn("routd page: group count", "err", err)
		}
	}

	// Pending outbound — bot messages still queued for delivery.
	pending := -1
	if err := d.db.QueryRow(
		`SELECT COUNT(*) FROM messages WHERE status='pending' AND is_bot_message=1`,
	).Scan(&pending); err != nil {
		slog.Warn("routd page: pending count", "err", err)
	}

	fmt.Fprintf(w, `<p class="dim">%s &middot; %s &middot; %s</p>`,
		statLabel("routes", routeCount),
		statLabel("groups", groupCount),
		statLabel("pending outbound", pending),
	)

	// Errored chats — group by chat, newest error first.
	fmt.Fprint(w, `<h2>Errored chats</h2>`)
	rows, err := d.db.Query(
		`SELECT chat_jid, COUNT(*) AS cnt, MAX(timestamp) AS last_err
		 FROM messages WHERE errored=1
		 GROUP BY chat_jid ORDER BY last_err DESC LIMIT 50`)
	if err != nil {
		slog.Warn("routd page: errored query", "err", err)
		fmt.Fprint(w, htmlBanner("err", "errored chats error: "+err.Error()))
		pageClose(w, r)
		return
	}
	defer rows.Close()

	var tableRows [][]string
	for rows.Next() {
		var chatJID, lastErr string
		var cnt int
		if err := rows.Scan(&chatJID, &cnt, &lastErr); err != nil {
			slog.Warn("routd page: errored scan", "err", err)
			continue
		}
		retry := fmt.Sprintf(
			`<form method="post" action="/dash/routd/retry" class="form-inline">`+
				`<input type="hidden" name="chat_jid" value="%s">`+
				`<button class="btn" type="submit">retry</button></form>`,
			esc(chatJID))
		tableRows = append(tableRows, []string{
			d.chatJIDCell(chatJID),
			fmt.Sprintf("%d", cnt),
			fmt.Sprintf(`<abbr title="%s">%s</abbr>`, esc(lastErr), esc(relativeTS(lastErr))),
			retry,
		})
	}
	if err := rows.Err(); err != nil {
		slog.Warn("routd page: errored rows", "err", err)
	}
	fmt.Fprint(w, htmlTable([]string{"Chat", "Errors", "Last", ""}, tableRows))

	pageClose(w, r)
}

// handleRoutdRetry handles POST /dash/routd/retry — clear the errored flag on a
// chat's messages so routd re-dispatches them. Operator-only.
func (d *dash) handleRoutdRetry(w http.ResponseWriter, r *http.Request) {
	if !d.requireOperator(w, r) {
		return
	}
	if !requireSameOrigin(w, r) {
		return
	}
	if d.db == nil {
		http.Error(w, "messages store unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	chatJID := strings.TrimSpace(r.FormValue("chat_jid"))
	if chatJID == "" {
		http.Error(w, "chat_jid required", http.StatusBadRequest)
		return
	}
	res, err := d.db.Exec(`UPDATE messages SET errored=0 WHERE chat_jid=? AND errored=1`, chatJID)
	if err != nil {
		slog.Warn("routd retry: update", "chat_jid", chatJID, "err", err)
		http.Error(w, "write failed", http.StatusInternalServerError)
		return
	}
	n, _ := res.RowsAffected()
	audit.Emit(context.Background(), audit.Event{
		Category: audit.CategoryMutation,
		Action:   "routd.retry",
		Actor:    "rest:dashd",
		Surface:  audit.SurfaceREST,
		Resource: "messages/" + chatJID,
		Outcome:  audit.OutcomeOK,
		ParamsSummary: map[string]any{
			"cleared": n,
		},
	})
	slog.Info("routd retry", "chat_jid", chatJID, "cleared", n)
	http.Redirect(w, r, "/dash/routd/?msg=retried", http.StatusSeeOther)
}

// statLabel renders "<n> <label>" with a dim "?" when the count is unavailable
// (negative sentinel = the source DB was missing or the query failed).
func statLabel(label string, n int) string {
	if n < 0 {
		return fmt.Sprintf(`<span class="dim">? %s</span>`, esc(label))
	}
	return fmt.Sprintf("%d %s", n, esc(label))
}

// chatJIDCell renders a chat_jid as a group link when its folder is resolvable
// (web:/hook: prefixes carry the folder; platform JIDs resolve via the routes
// table), else as a plain <code> JID.
func (d *dash) chatJIDCell(jid string) string {
	if folder := d.jidFolder(jid); folder != "" {
		return fmt.Sprintf(`<a href="/dash/chat/%s/"><code>%s</code></a>`,
			folderPath(folder), esc(jid))
	}
	return `<code>` + esc(jid) + `</code>`
}

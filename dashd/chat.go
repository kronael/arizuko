package main

import (
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kronael/arizuko/store"
)

// Chat portal: bridges the dashboard to the web chat widget served by webd at
// /chat/<token>/. Mounted at:
//
//	GET  /dash/chat/              — browse accessible groups, each links to its chat page
//	GET  /dash/chat/{folder}/     — sessions (web: route tokens) + new-session form
//	POST /dash/chat/{folder}/     — mint a chat token, redirect to /chat/<token>/
//
// A "session" is a web: route token (jid web:<folder>); the widget keys its
// SSE stream and history off the token. Tokens here are minted with the same
// audit-free raw INSERT as route_tokens.go (routd.db has no audit_log table).

// dispatchChatGet routes GET /dash/chat/{folder...}: an empty tail renders the
// portal (group browser); a folder tail renders that group's chat page. One
// handler because Go's mux forbids a bare /dash/chat/ alongside {folder...}.
func (d *dash) dispatchChatGet(w http.ResponseWriter, r *http.Request) {
	if strings.Trim(r.PathValue("folder"), "/") == "" {
		d.handleChatPortal(w, r)
		return
	}
	d.handleChatGroup(w, r)
}

// handleChatPortal lists the groups the caller may chat with. Each tile links
// to the group's chat page. Operators see every group; others see their
// granted folders (visible).
func (d *dash) handleChatPortal(w http.ResponseWriter, r *http.Request) {
	allowed, operator := d.callerScope(r)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageTopFor(w, r, "Chat")
	fmt.Fprint(w, `<p class="dim">Open a web chat session with any group's agent, or continue an existing one.</p>`)

	rows, err := d.adminDB().Query(`SELECT folder FROM groups ORDER BY folder LIMIT 500`)
	if err != nil {
		slog.Warn("chat: groups query", "err", err)
		fmt.Fprint(w, htmlBanner("err", "error: "+err.Error()))
		pageClose(w, r)
		return
	}
	defer rows.Close()
	var folders []string
	for rows.Next() {
		var folder string
		if err := rows.Scan(&folder); err != nil {
			slog.Warn("chat: scan row", "err", err)
			continue
		}
		if !visible(allowed, operator, folder) {
			continue
		}
		folders = append(folders, folder)
	}
	if err := rows.Err(); err != nil {
		slog.Warn("chat: rows", "err", err)
		fmt.Fprint(w, htmlBanner("err", "rows error: "+err.Error()))
		pageClose(w, r)
		return
	}

	if len(folders) == 0 {
		fmt.Fprint(w, `<p class="empty">No groups available. Ask an operator for access.</p>`)
		pageClose(w, r)
		return
	}

	fmt.Fprint(w, `<div class="tiles">`)
	for _, folder := range folders {
		count, last := d.webChatStats(folder)
		sub := "no sessions yet"
		if count > 0 {
			sub = fmt.Sprintf("%d message%s", count, pluralS(count))
			if last != "" {
				sub += " · last " + relativeTS(last)
			}
		}
		fmt.Fprintf(w,
			`<a class="tile" href="/dash/chat/%s/"><h2>%s</h2><p>%s</p></a>`,
			esc(folderPath(folder)), esc(folder), esc(sub))
	}
	fmt.Fprint(w, `</div>`)
	pageClose(w, r)
}

// handleChatGroup renders one group's chat page: existing sessions (web: route
// tokens with share links), a new-session form, and the most recent web
// messages. GET only; the form POSTs to handleChatNew at the same path.
func (d *dash) handleChatGroup(w http.ResponseWriter, r *http.Request) {
	folder, ok := chatFolder(w, r)
	if !ok {
		return
	}
	if !d.requireVisible(w, r, folder) {
		return
	}
	st := store.New(d.adminDB())

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageTopFor(w, r, "Chat — "+folder,
		struct{ Href, Label string }{"/dash/chat/", "chat"},
		struct{ Href, Label string }{"", folder})

	fmt.Fprint(w, htmlSection("Start a session",
		fmt.Sprintf(`<form method="post" action="/dash/chat/%s/">`, folderPath(folder))+
			htmlFormRow("Label (optional)", `<input name="label" type="text" placeholder="design review">`)+
			`<p><button type="submit" class="btn btn-primary">New chat session</button></p>`+
			`</form>`+
			`<p class="dim">Opens the chat widget in a new session. Share the link to let others join.</p>`))

	// A session is a web: route token. Tokens are stored hashed, so the page
	// can count + date existing sessions but cannot reconstruct their links —
	// the link is shown once at mint time (the redirect into the widget) and
	// thereafter held only by whoever copied it. Listing them keeps the count
	// honest and surfaces stale sessions for revocation under /dash/tokens/.
	var chatTokens int
	for _, t := range st.ListRouteTokens(folder) {
		if store.RouteTokenKind(t.JID) == store.RouteTokenKindChat {
			chatTokens++
		}
	}
	if chatTokens > 0 {
		fmt.Fprintf(w, `<h2>Sessions <span class="dim" style="font-weight:normal">%d active</span></h2>`, chatTokens)
		fmt.Fprintf(w,
			`<p class="dim">Web chat tokens are stored hashed; a session reopens only via the link copied when it was created. `+
				`Manage or revoke tokens under <a href="/dash/tokens/%s/">tokens</a>.</p>`,
			esc(folderPath(folder)))
	} else {
		fmt.Fprint(w, htmlSection("Sessions", `<p class="empty">No chat sessions yet. Start one above.</p>`))
	}

	d.writeRecentWebMessages(w, folder)
	pageClose(w, r)
}

// handleChatNew mints a web: route token for the folder and redirects the
// browser straight into the chat widget at /chat/<token>/. Admin-gated (a
// token is a write that grants chat access). The raw token leaks only via the
// redirect URL — the same one-shot exposure as the widget's own bootstrap.
func (d *dash) handleChatNew(w http.ResponseWriter, r *http.Request) {
	folder, ok := chatFolder(w, r)
	if !ok {
		return
	}
	if _, ok := d.requireAdmin(w, r, folder); !ok {
		return
	}
	if d.adminDB() == nil {
		http.Error(w, "read-only", http.StatusServiceUnavailable)
		return
	}
	raw := store.GenRouteToken()
	jid := "web:" + folder
	// Raw INSERT (not store.InsertRouteToken): routd.db has no audit_log table,
	// so the audited writer would roll back. Same discipline as route_tokens.go.
	if _, err := d.adminDB().Exec(
		`INSERT INTO route_tokens (token_hash, jid, owner_folder, created_at) VALUES (?, ?, ?, ?)`,
		store.HashRouteToken(raw), jid, folder, time.Now().Format(time.RFC3339Nano)); err != nil {
		slog.Warn("chat: mint token", "folder", folder, "err", err)
		http.Error(w, "mint failed", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/chat/"+raw+"/", http.StatusSeeOther)
}

// writeRecentWebMessages renders the last 10 web-chat messages for the folder
// (chat_jid = web:<folder>). Empty when the group has no web traffic yet.
func (d *dash) writeRecentWebMessages(w http.ResponseWriter, folder string) {
	rows, err := d.adminDB().Query(
		`SELECT timestamp, sender, substr(content,1,80)
		 FROM messages WHERE chat_jid = ? ORDER BY timestamp DESC LIMIT 10`,
		"web:"+folder)
	if err != nil {
		slog.Warn("chat: recent messages", "folder", folder, "err", err)
		return
	}
	defer rows.Close()
	var msgRows [][]string
	for rows.Next() {
		var ts, sender, content string
		if err := rows.Scan(&ts, &sender, &content); err != nil {
			slog.Warn("chat: message scan", "err", err)
			continue
		}
		msgRows = append(msgRows, []string{
			fmt.Sprintf(`<abbr title="%s">%s</abbr>`, esc(ts), esc(relativeTS(ts))),
			`<code>` + esc(sender) + `</code>`,
			esc(content),
		})
	}
	if len(msgRows) == 0 {
		return
	}
	fmt.Fprint(w, htmlSection("Recent web messages",
		htmlTable([]string{"Time", "Sender", "Content"}, msgRows)))
}

// webChatStats returns the web-chat message count and last-activity timestamp
// for a folder (chat_jid = web:<folder>). Zero/empty on any error.
func (d *dash) webChatStats(folder string) (count int, last string) {
	var lastNull sql.NullString
	if err := d.adminDB().QueryRow(
		`SELECT COUNT(*), MAX(timestamp) FROM messages WHERE chat_jid = ?`,
		"web:"+folder).Scan(&count, &lastNull); err != nil {
		slog.Warn("chat: web stats", "folder", folder, "err", err)
		return 0, ""
	}
	return count, lastNull.String
}

// chatFolder extracts and validates the {folder...} path value for the chat
// handlers, URL-decoding it. Writes 400 and returns false on a bad path.
func chatFolder(w http.ResponseWriter, r *http.Request) (string, bool) {
	raw := strings.TrimSuffix(r.PathValue("folder"), "/")
	folder, err := url.PathUnescape(raw)
	if err != nil || folder == "" {
		http.Error(w, "bad folder", http.StatusBadRequest)
		return "", false
	}
	return folder, true
}

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
//	GET  /dash/chat/              — ChatGPT-style conversation list (all visible)
//	GET  /dash/chat/{folder}/     — one group's sessions + recent web messages
//	POST /dash/chat/{folder}/     — mint a chat token, redirect to /chat/<token>/
//
// A "session" is a web: route token (jid web:<folder>); the widget keys its
// SSE stream and history off the token. Tokens are stored HASHED in routd.db,
// so the portal cannot reconstruct a continue link from route_tokens alone.
// The chat_sessions table (messages.db) records the RAW token at mint time so
// the portal can offer "continue" links — the same one-shot exposure as the
// widget's own bootstrap, just persisted for the operator who minted it.

// ensureChatSessionsTable creates chat_sessions in messages.db. Idempotent
// (CREATE TABLE IF NOT EXISTS is a cheap metadata no-op once the table exists,
// so it runs per-call rather than holding cross-DB state in a sync.Once);
// errors are returned for the caller to log + swallow (a missing table degrades
// the portal to "no continue links", never a 500).
func ensureChatSessionsTable(db *sql.DB) error {
	if db == nil {
		return nil
	}
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS chat_sessions (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		folder     TEXT NOT NULL,
		token      TEXT NOT NULL UNIQUE,
		label      TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL
	)`)
	return err
}

// chatSession is one row of chat_sessions joined with its derived title.
type chatSession struct {
	folder    string
	token     string
	label     string
	createdAt string // RFC3339Nano
	title     string // label, else first user message, else "Chat · <date>"
}

// dispatchChatGet routes GET /dash/chat/{folder...}: an empty tail renders the
// conversation list; a folder tail renders that group's chat page. One handler
// because Go's mux forbids a bare /dash/chat/ alongside {folder...}.
func (d *dash) dispatchChatGet(w http.ResponseWriter, r *http.Request) {
	if strings.Trim(r.PathValue("folder"), "/") == "" {
		d.handleChatPortal(w, r)
		return
	}
	d.handleChatGroup(w, r)
}

// handleChatPortal renders a ChatGPT-style conversation list: a search box, a
// new-conversation form, and past sessions grouped by recency (Today /
// Yesterday / Past 7 days / Older). Sessions come from chat_sessions; each row
// reopens its conversation via the raw token. Operators see every group's
// sessions; others see their granted folders.
func (d *dash) handleChatPortal(w http.ResponseWriter, r *http.Request) {
	allowed, operator := d.callerScope(r)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	groupFilter := strings.TrimSpace(r.URL.Query().Get("group"))

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageTopFor(w, r, "Chat")
	fmt.Fprint(w, convStyle)

	folders := d.visibleFolders(allowed, operator)

	// New-conversation form (top). The select's onchange points the form at the
	// chosen folder's POST path, reusing handleChatNew unchanged.
	d.writeNewConvForm(w, folders, groupFilter)

	// Search + group filter (GET form back to this page).
	writeConvSearch(w, q, groupFilter, folders)

	sessions := d.loadChatSessions(folders, q, groupFilter)
	if len(sessions) == 0 {
		fmt.Fprint(w, `<p class="empty">No conversations yet. Start one above.</p>`)
		pageClose(w, r)
		return
	}
	writeConvGroups(w, sessions)
	pageClose(w, r)
}

// visibleFolders lists the folders the caller may chat with (operator → all).
func (d *dash) visibleFolders(allowed []string, operator bool) []string {
	rows, err := d.adminDB().Query(`SELECT folder FROM groups ORDER BY folder LIMIT 500`)
	if err != nil {
		slog.Warn("chat: groups query", "err", err)
		return nil
	}
	defer rows.Close()
	var folders []string
	for rows.Next() {
		var folder string
		if err := rows.Scan(&folder); err != nil {
			continue
		}
		if visible(allowed, operator, folder) {
			folders = append(folders, folder)
		}
	}
	return folders
}

// loadChatSessions reads chat_sessions for the visible folders (newest first),
// then derives a display title per row from the first user message. Filters by
// the search query (?q matches title/folder) and group (?group). Best-effort:
// a missing table or DB returns an empty list.
func (d *dash) loadChatSessions(folders []string, q, groupFilter string) []chatSession {
	if d.db == nil || len(folders) == 0 {
		return nil
	}
	if err := ensureChatSessionsTable(d.db); err != nil {
		slog.Warn("chat: ensure sessions table", "err", err)
		return nil
	}
	allow := make(map[string]bool, len(folders))
	for _, f := range folders {
		allow[f] = true
	}

	rows, err := d.db.Query(
		`SELECT folder, token, label, created_at FROM chat_sessions
		 ORDER BY created_at DESC LIMIT 200`)
	if err != nil {
		slog.Warn("chat: sessions query", "err", err)
		return nil
	}
	defer rows.Close()
	var out []chatSession
	for rows.Next() {
		var s chatSession
		if err := rows.Scan(&s.folder, &s.token, &s.label, &s.createdAt); err != nil {
			continue
		}
		if !allow[s.folder] {
			continue // not visible to this caller
		}
		if groupFilter != "" && s.folder != groupFilter {
			continue
		}
		s.title = d.sessionTitle(s)
		if q != "" && !strings.Contains(strings.ToLower(s.title), strings.ToLower(q)) &&
			!strings.Contains(strings.ToLower(s.folder), strings.ToLower(q)) {
			continue
		}
		out = append(out, s)
	}
	return out
}

// sessionTitle derives a conversation title: the explicit label, else the
// first user message in the folder's web chat at/after the session start, else
// a dated fallback. The message lives in routd.db (adminDB), the session row in
// messages.db — joined here in Go.
func (d *dash) sessionTitle(s chatSession) string {
	if s.label != "" {
		return s.label
	}
	var first string
	if err := d.adminDB().QueryRow(
		`SELECT substr(content,1,60) FROM messages
		 WHERE chat_jid = ? AND is_bot_message = 0 AND timestamp >= ?
		 ORDER BY timestamp ASC LIMIT 1`,
		"web:"+s.folder, s.createdAt).Scan(&first); err == nil {
		if first = strings.TrimSpace(first); first != "" {
			return first
		}
	}
	return "Chat · " + shortTS(s.createdAt)
}

// writeNewConvForm emits the "start a conversation" form. The folder <select>
// rewrites the form action on change so the POST hits /dash/chat/{folder}/.
func (d *dash) writeNewConvForm(w http.ResponseWriter, folders []string, selected string) {
	if len(folders) == 0 {
		fmt.Fprint(w, `<p class="empty">No groups available. Ask an operator for access.</p>`)
		return
	}
	first := folders[0]
	if selected != "" {
		for _, f := range folders {
			if f == selected {
				first = f
				break
			}
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<form method="post" id="new-conv-form" action="/dash/chat/%s/" class="conv-new">`,
		folderPath(first))
	b.WriteString(`<select name="_group" aria-label="group" onchange="document.getElementById('new-conv-form').action='/dash/chat/'+encodeURIComponent(this.value)+'/'">`)
	for _, f := range folders {
		sel := ""
		if f == first {
			sel = ` selected`
		}
		fmt.Fprintf(&b, `<option value="%s"%s>%s</option>`, esc(f), sel, esc(f))
	}
	b.WriteString(`</select>`)
	b.WriteString(`<input name="label" type="text" placeholder="What's this about? (optional)" size="40">`)
	b.WriteString(`<button type="submit" class="btn btn-primary">Start conversation</button>`)
	b.WriteString(`</form>`)
	w.Write([]byte(b.String()))
}

// writeConvSearch emits the search + group-filter GET form. A "clear" link
// shows when a group filter is active.
func writeConvSearch(w http.ResponseWriter, q, groupFilter string, folders []string) {
	var b strings.Builder
	b.WriteString(`<form method="get" action="/dash/chat/" class="conv-search">`)
	fmt.Fprintf(&b, `<input name="q" type="search" placeholder="Search conversations" value="%s">`, esc(q))
	b.WriteString(`<select name="group" aria-label="filter by group"><option value="">all groups</option>`)
	for _, f := range folders {
		sel := ""
		if f == groupFilter {
			sel = ` selected`
		}
		fmt.Fprintf(&b, `<option value="%s"%s>%s</option>`, esc(f), sel, esc(f))
	}
	b.WriteString(`</select>`)
	b.WriteString(`<button type="submit" class="btn btn-secondary">Search</button>`)
	if groupFilter != "" || q != "" {
		b.WriteString(` <a href="/dash/chat/" class="dim">× clear</a>`)
	}
	b.WriteString(`</form>`)
	w.Write([]byte(b.String()))
}

// writeConvGroups renders sessions (already newest-first) under date-group
// headers: Today / Yesterday / Past 7 days / Older.
func writeConvGroups(w http.ResponseWriter, sessions []chatSession) {
	var cur string
	open := false
	for _, s := range sessions {
		g := convDateGroup(s.createdAt)
		if g != cur {
			if open {
				fmt.Fprint(w, `</div>`)
			}
			fmt.Fprintf(w, `<h3 class="conv-date-group">%s</h3><div class="conv-list">`, esc(g))
			cur = g
			open = true
		}
		fmt.Fprintf(w,
			`<a class="conv-row" href="/chat/%s/">`+
				`<div class="conv-meta"><span class="conv-folder dim"><code>%s</code></span>`+
				`<span class="conv-time dim"><abbr title="%s">%s</abbr></span></div>`+
				`<div class="conv-title">%s</div></a>`,
			esc(s.token), esc(s.folder), esc(s.createdAt), esc(relativeTS(s.createdAt)), esc(s.title))
	}
	if open {
		fmt.Fprint(w, `</div>`)
	}
}

// convDateGroup buckets a timestamp into Today / Yesterday / Past 7 days /
// Older relative to local midnight. Unparseable → "Older".
func convDateGroup(ts string) string {
	t, ok := parseTS(ts)
	if !ok {
		return "Older"
	}
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	switch {
	case !t.Before(today):
		return "Today"
	case !t.Before(today.AddDate(0, 0, -1)):
		return "Yesterday"
	case !t.Before(today.AddDate(0, 0, -7)):
		return "Past 7 days"
	default:
		return "Older"
	}
}

// handleChatGroup renders one group's chat page: a new-session form, its past
// conversations (from messages grouped by topic), and recent web messages.
// GET only; the form POSTs to handleChatNew at the same path.
func (d *dash) handleChatGroup(w http.ResponseWriter, r *http.Request) {
	folder, ok := chatFolder(w, r)
	if !ok {
		return
	}
	if !d.requireVisible(w, r, folder) {
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageTopFor(w, r, "Chat — "+folder,
		struct{ Href, Label string }{"/dash/chat/", "chat"},
		struct{ Href, Label string }{"", folder})
	fmt.Fprint(w, convStyle)

	fmt.Fprint(w, htmlSection("Start a session",
		fmt.Sprintf(`<form method="post" action="/dash/chat/%s/">`, folderPath(folder))+
			htmlFormRow("Label (optional)", `<input name="label" type="text" placeholder="design review">`)+
			`<p><button type="submit" class="btn btn-primary">New chat session</button></p>`+
			`</form>`+
			`<p class="dim">Opens the chat widget in a new session. Share the link to let others join.</p>`))

	d.writeFolderConversations(w, folder)
	d.writeRecentWebMessages(w, folder)
	pageClose(w, r)
}

// writeFolderConversations lists the folder's web-chat conversations grouped by
// topic (newest first), plus a "continue" link when a chat_sessions row exists.
// Topics from before chat_sessions existed still show — without a continue link.
func (d *dash) writeFolderConversations(w http.ResponseWriter, folder string) {
	topics, err := store.New(d.adminDB()).Topics(folder)
	if err != nil {
		slog.Warn("chat: topics", "folder", folder, "err", err)
		return
	}
	if len(topics) == 0 {
		fmt.Fprint(w, htmlSection("Conversations", `<p class="empty">No conversations yet. Start one above.</p>`))
		return
	}
	tokens := d.folderSessionTokens(folder)
	var rows [][]string
	for _, t := range topics {
		title := t.Preview
		if title == "" {
			title = t.ID
		}
		cont := "—"
		if tok := tokens[t.ID]; tok != "" {
			cont = fmt.Sprintf(`<a href="/chat/%s/">continue</a>`, esc(tok))
		}
		rows = append(rows, []string{
			esc(title),
			fmt.Sprintf("%d", t.MessageCount),
			fmt.Sprintf(`<abbr title="%s">%s</abbr>`, esc(t.LastAt.Format(time.RFC3339)), esc(relativeTS(t.LastAt.Format(time.RFC3339Nano)))),
			cont,
		})
	}
	fmt.Fprint(w, htmlSection("Conversations",
		htmlTable([]string{"Title", "Messages", "Last", ""}, rows)))
}

// folderSessionTokens maps topic → raw token for the folder's chat_sessions.
// A session's topic is the first message's topic in its conversation, looked up
// in routd.db. Empty on any error (continue links simply won't render).
func (d *dash) folderSessionTokens(folder string) map[string]string {
	out := map[string]string{}
	if d.db == nil {
		return out
	}
	if err := ensureChatSessionsTable(d.db); err != nil {
		return out
	}
	rows, err := d.db.Query(
		`SELECT token, created_at FROM chat_sessions WHERE folder = ? ORDER BY created_at DESC LIMIT 200`,
		folder)
	if err != nil {
		return out
	}
	defer rows.Close()
	// Rows are newest-first (ORDER BY created_at DESC). The topic of a session
	// is the topic of the first user message at/after its creation; the first
	// (newest) session to claim a topic wins.
	for rows.Next() {
		var tok, createdAt string
		if rows.Scan(&tok, &createdAt) != nil {
			continue
		}
		var topic string
		if err := d.adminDB().QueryRow(
			`SELECT COALESCE(topic,'') FROM messages
			 WHERE chat_jid = ? AND timestamp >= ? AND topic != ''
			 ORDER BY timestamp ASC LIMIT 1`,
			"web:"+folder, createdAt).Scan(&topic); err == nil && topic != "" {
			if _, seen := out[topic]; !seen {
				out[topic] = tok
			}
		}
	}
	return out
}

// handleChatNew mints a web: route token for the folder, records it (raw) in
// chat_sessions for a continue link, and redirects into the chat widget at
// /chat/<token>/. Admin-gated (a token is a write that grants chat access).
// The folder comes from the path, so the portal's new-conversation form points
// its action at /dash/chat/{selected-folder}/ — no separate generic handler.
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
	now := time.Now().Format(time.RFC3339Nano)
	// Raw INSERT (not store.InsertRouteToken): routd.db has no audit_log table,
	// so the audited writer would roll back. Same discipline as route_tokens.go.
	if _, err := d.adminDB().Exec(
		`INSERT INTO route_tokens (token_hash, jid, owner_folder, created_at) VALUES (?, ?, ?, ?)`,
		store.HashRouteToken(raw), jid, folder, now); err != nil {
		slog.Warn("chat: mint token", "folder", folder, "err", err)
		http.Error(w, "mint failed", http.StatusInternalServerError)
		return
	}
	// Record the raw token so the portal can offer a continue link. Best-effort:
	// a failed insert costs the continue link, not the session.
	if err := ensureChatSessionsTable(d.db); err != nil {
		slog.Warn("chat: ensure sessions table", "err", err)
	} else if d.db != nil {
		label := strings.TrimSpace(r.FormValue("label"))
		if _, err := d.db.Exec(
			`INSERT OR IGNORE INTO chat_sessions (folder, token, label, created_at) VALUES (?, ?, ?, ?)`,
			folder, raw, label, now); err != nil {
			slog.Warn("chat: record session", "folder", folder, "err", err)
		}
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

// convStyle is the conversation-list CSS, scoped to .conv-* classes and using
// only theme CSS vars (--border, --accent, --dim). Emitted inline after
// pageTopFor; idempotent if a page emits it twice (last wins, identical rules).
const convStyle = `<style>
.conv-list { display:flex; flex-direction:column; gap:.25em; margin-top:.5em; }
.conv-row { display:block; padding:.6em .8em; border-radius:6px; border:1px solid var(--border); text-decoration:none; color:inherit; }
.conv-row:hover { border-color:var(--accent); background:var(--border); }
.conv-meta { display:flex; justify-content:space-between; font-size:.8em; margin-bottom:.2em; }
.conv-title { font-size:.95em; white-space:nowrap; overflow:hidden; text-overflow:ellipsis; }
.conv-date-group { font-size:.8em; font-weight:600; color:var(--dim); margin:.8em 0 .3em; text-transform:uppercase; letter-spacing:.05em; }
.conv-search { display:flex; gap:.5em; margin-bottom:1em; align-items:center; flex-wrap:wrap; }
.conv-new { display:flex; gap:.5em; margin-bottom:1em; align-items:center; flex-wrap:wrap; }
</style>`

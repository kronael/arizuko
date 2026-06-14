package main

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
)

const meHead = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>arizuko</title>
<script src="https://unpkg.com/htmx.org@2.0.4/dist/htmx.min.js"></script>
<script src="https://unpkg.com/htmx-ext-sse@2.2.2/sse.js"></script>
<link rel="stylesheet" href="/static/style.css">
</head>
`

// meShell writes the three-pane shell open tags; caller writes main content then meShellClose.
func meShellOpen(w http.ResponseWriter, r *http.Request, title string) {
	sub := userSub(r)
	name := userName(r)
	if name == "" {
		name = sub
	}
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, meHead)
	fmt.Fprintf(w, `<body class="me-shell">
<header class="topbar">
  <a href="/me/" class="logo">&#x25A0; arizuko</a>
  <nav class="topnav">
    <a href="/me/">home</a>
    <a href="/me/chats">chats</a>
    <a href="/me/settings">settings</a>
    <span class="sub">%s</span>
    <form action="/auth/logout" method="POST" style="display:inline">
      <button type="submit">logout</button>
    </form>
  </nav>
</header>
<div class="me-layout">
  <aside class="me-sidebar" id="me-sidebar">
    <button class="sidebar-toggle" onclick="document.getElementById('me-sidebar').classList.toggle('open')">&#9776;</button>
    <div class="tree" hx-get="/me/x/folders" hx-trigger="load" hx-swap="innerHTML"></div>
  </aside>
  <main class="me-main">
`, htmlEscape(name))
	_ = title
}

func meShellClose(w http.ResponseWriter) {
	fmt.Fprint(w, `  </main>
</div>
</body></html>`)
}

// GET /me/
func (s *server) handleMeIndex(w http.ResponseWriter, r *http.Request) {
	meShellOpen(w, r, "home")
	fmt.Fprint(w, `<div class="chat-list">
  <h2 style="margin-bottom:1rem">Recent chats</h2>
  <div hx-get="/me/x/chats" hx-trigger="load" hx-swap="innerHTML"></div>
  <p style="margin-top:1.5rem"><a href="/me/chats/new">+ new chat</a></p>
</div>`)
	meShellClose(w)
}

// GET /me/chats
func (s *server) handleMeChats(w http.ResponseWriter, r *http.Request) {
	folder := r.URL.Query().Get("folder")
	meShellOpen(w, r, "chats")
	if folder != "" {
		fmt.Fprintf(w, `<div class="chat-list">
  <h2 style="margin-bottom:1rem">%s</h2>
  <div hx-get="/me/x/chats?folder=%s" hx-trigger="load" hx-swap="innerHTML"></div>
  <p style="margin-top:1.5rem"><a href="/me/chats/new">+ new chat</a></p>
</div>`, htmlEscape(groupfolder.NameOf(folder)), url.QueryEscape(folder))
	} else {
		fmt.Fprint(w, `<div class="chat-list">
  <h2 style="margin-bottom:1rem">All chats</h2>
  <div hx-get="/me/x/chats" hx-trigger="load" hx-swap="innerHTML"></div>
  <p style="margin-top:1.5rem"><a href="/me/chats/new">+ new chat</a></p>
</div>`)
	}
	meShellClose(w)
}

// GET /me/x/folders — HTMX partial: folder tree
// groups lives in routd.db (stRoutd) — split ownership
func (s *server) handleMeXFolders(w http.ResponseWriter, r *http.Request) {
	grants := userGroups(r)
	groups := s.stRoutd.AllGroups()
	w.Header().Set("Content-Type", "text/html")

	// collect allowed folders, sorted
	var folders []string
	for folder := range groups {
		if userAllowedFolder(grants, folder) {
			folders = append(folders, folder)
		}
	}
	sort.Strings(folders)

	// group by top-level prefix
	type node struct {
		name     string
		children []string
	}
	tops := map[string]*node{}
	var topOrder []string
	for _, f := range folders {
		parts := strings.SplitN(f, "/", 2)
		top := parts[0]
		if _, ok := tops[top]; !ok {
			tops[top] = &node{name: top}
			topOrder = append(topOrder, top)
		}
		tops[top].children = append(tops[top].children, f)
	}

	for _, top := range topOrder {
		n := tops[top]
		if len(n.children) == 1 && n.children[0] == top {
			// leaf: single folder with no children
			fmt.Fprintf(w, `<a class="tree-leaf" href="/me/chats?folder=%s">%s</a>`,
				url.QueryEscape(top), htmlEscape(groupfolder.NameOf(top)))
		} else {
			fmt.Fprintf(w, `<details open><summary>%s</summary><ul>`, htmlEscape(top))
			for _, f := range n.children {
				fmt.Fprintf(w, `<li><a href="/me/chats?folder=%s">%s</a></li>`,
					url.QueryEscape(f), htmlEscape(groupfolder.NameOf(f)))
			}
			fmt.Fprint(w, `</ul></details>`)
		}
	}
}

// GET /me/x/chats — HTMX partial: conversation list
func (s *server) handleMeXChats(w http.ResponseWriter, r *http.Request) {
	grants := userGroups(r)
	filterFolder := r.URL.Query().Get("folder")
	groups := s.stRoutd.AllGroups()
	w.Header().Set("Content-Type", "text/html")

	type chatRow struct {
		Folder  string
		Topic   string
		Preview string
		LastAt  time.Time
	}
	var rows []chatRow

	for folder := range groups {
		if !userAllowedFolder(grants, folder) {
			continue
		}
		if filterFolder != "" && folder != filterFolder && !strings.HasPrefix(folder, filterFolder+"/") {
			continue
		}
		topics, err := s.stRoutd.Topics(folder)
		if err != nil {
			continue
		}
		for _, t := range topics {
			rows = append(rows, chatRow{
				Folder:  folder,
				Topic:   t.ID,
				Preview: t.Preview,
				LastAt:  t.LastAt,
			})
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].LastAt.After(rows[j].LastAt) })

	for _, row := range rows {
		preview := strings.TrimSpace(row.Preview)
		if len(preview) > 60 {
			preview = preview[:60] + "…"
		}
		when := row.LastAt.Format("Jan 2")
		if time.Since(row.LastAt) < 24*time.Hour {
			when = row.LastAt.Format("15:04")
		}
		topicShort := row.Topic
		if len(topicShort) > 20 {
			topicShort = topicShort[:20] + "…"
		}
		fmt.Fprintf(w,
			`<a class="chat-item" href="/me/chats/%s/%s">
  <div><strong>%s</strong> <small>%s</small></div>
  <div class="meta">%s · %s</div>
</a>`,
			url.PathEscape(row.Folder), url.PathEscape(row.Topic),
			htmlEscape(groupfolder.NameOf(row.Folder)), htmlEscape(topicShort),
			htmlEscape(preview), when)
	}
	if len(rows) == 0 {
		fmt.Fprint(w, `<p style="color:#999">No chats yet.</p>`)
	}
}

// GET /me/x/thread?folder=<f>&topic=<t>&before=<ts> — HTMX partial: thread messages
func (s *server) handleMeXThread(w http.ResponseWriter, r *http.Request) {
	folder := r.URL.Query().Get("folder")
	topic := r.URL.Query().Get("topic")
	before := time.Now()
	if bs := r.URL.Query().Get("before"); bs != "" {
		if t, err := time.Parse(time.RFC3339, bs); err == nil {
			before = t
		}
	}
	// messages lives in routd.db (stRoutd) — split ownership
	msgs, _ := s.stRoutd.MessagesByTopic(folder, topic, before, 50)
	w.Header().Set("Content-Type", "text/html")
	for i := len(msgs) - 1; i >= 0; i-- {
		writeMsgDiv(w, msgs[i])
	}
}

func writeMsgDiv(w http.ResponseWriter, m core.Message) {
	role := messageRole(m)
	when := m.Timestamp.Format("15:04")
	fmt.Fprintf(w,
		`<div class="msg %s" id="msg-%s"><div class="body">%s</div><time>%s</time></div>`,
		role, m.ID, htmlEscape(m.Content), when)
}

// GET /me/chats/{folder...}/{topic}
func (s *server) handleMeThread(w http.ResponseWriter, r *http.Request) {
	folder, topic := meFolderTopic(r)
	if folder == "" || topic == "" {
		http.NotFound(w, r)
		return
	}
	if !userAllowedFolder(userGroups(r), folder) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	msgs, _ := s.stRoutd.MessagesByTopic(folder, topic, time.Now(), 50)

	meShellOpen(w, r, groupfolder.NameOf(folder))

	folderE := url.PathEscape(folder)
	topicE := url.PathEscape(topic)
	sseURL := fmt.Sprintf("/me/chats/%s/%s/sse", folderE, topicE)
	sendURL := fmt.Sprintf("/me/chats/%s/%s/send", folderE, topicE)

	fmt.Fprintf(w, `<div class="thread">
  <div id="messages" hx-ext="sse" sse-connect="%s" sse-swap="message" hx-swap="beforeend">`,
		htmlEscape(sseURL))

	for i := len(msgs) - 1; i >= 0; i-- {
		writeMsgDiv(w, msgs[i])
	}

	fmt.Fprintf(w, `</div>
  <form class="send-form" hx-post="%s" hx-target="#messages" hx-swap="beforeend"
        hx-on::after-request="this.reset()">
    <textarea name="message" placeholder="type a message…" rows="2"
              onkeydown="if(event.key==='Enter'&&!event.shiftKey){event.preventDefault();htmx.trigger(this.closest('form'),'submit')}"></textarea>
    <button type="submit">send</button>
  </form>
</div>`, htmlEscape(sendURL))

	meShellClose(w)
}

// POST /me/chats/{folder...}/{topic}/send
func (s *server) handleMeSend(w http.ResponseWriter, r *http.Request) {
	folder, topic := meFolderTopic(r)
	if folder == "" || topic == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !userAllowedFolder(userGroups(r), folder) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	content := strings.TrimSpace(r.FormValue("message"))
	if content == "" {
		http.Error(w, "message required", http.StatusBadRequest)
		return
	}

	sub := userSub(r)
	name := userName(r)
	if name == "" {
		name = sub
	}
	jid := "web:" + folder

	m, _, err := s.injectRouteMessage(jid, folder, content, topic, sub, name, "")
	if err != nil {
		http.Error(w, "send failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html")
	writeMsgDiv(w, m)
}

// GET /me/chats/{folder...}/{topic}/sse
func (s *server) handleMeSSE(w http.ResponseWriter, r *http.Request) {
	folder, topic := meFolderTopic(r)
	if folder == "" || topic == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !userAllowedFolder(userGroups(r), folder) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	if !s.hub.canSubscribe() {
		http.Error(w, "too many subscriptions", http.StatusServiceUnavailable)
		return
	}
	ch, unsub := s.hub.subscribe(folder, topic)
	defer unsub()
	serveSSE(w, r, ch)
}

// GET /me/chats/new
func (s *server) handleMeNewChat(w http.ResponseWriter, r *http.Request) {
	grants := userGroups(r)
	groups := s.stRoutd.AllGroups()

	meShellOpen(w, r, "new chat")
	fmt.Fprint(w, `<div style="padding:1rem;max-width:480px">
  <h2 style="margin-bottom:1rem">New chat</h2>
  <form method="POST" action="/me/chats/new">
    <label>Folder<br>
      <select name="folder" required style="width:100%;padding:.4rem;margin:.5rem 0 1rem">`)

	var folders []string
	for folder := range groups {
		if userAllowedFolder(grants, folder) {
			folders = append(folders, folder)
		}
	}
	sort.Strings(folders)
	for _, f := range folders {
		fmt.Fprintf(w, `<option value="%s">%s</option>`, htmlEscape(f), htmlEscape(f))
	}
	fmt.Fprint(w, `</select></label>
    <button type="submit" style="padding:.5rem 1.5rem">Start chat</button>
  </form>
</div>`)
	meShellClose(w)
}

// POST /me/chats/new
func (s *server) handleMeNewChatPost(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	folder := strings.TrimSpace(r.FormValue("folder"))
	if folder == "" {
		http.Error(w, "folder required", http.StatusBadRequest)
		return
	}
	if !userAllowedFolder(userGroups(r), folder) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	topic := fmt.Sprintf("t%d", time.Now().UnixMilli())
	http.Redirect(w, r,
		fmt.Sprintf("/me/chats/%s/%s", url.PathEscape(folder), url.PathEscape(topic)),
		http.StatusSeeOther)
}

// GET /me/folders/{folder...}
func (s *server) handleMeFolder(w http.ResponseWriter, r *http.Request) {
	folder := strings.TrimPrefix(r.PathValue("folder"), "/")
	if !userAllowedFolder(userGroups(r), folder) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	if _, ok := s.stRoutd.GroupByFolder(folder); !ok {
		http.NotFound(w, r)
		return
	}
	topics, _ := s.stRoutd.Topics(folder)
	meShellOpen(w, r, groupfolder.NameOf(folder))
	fmt.Fprintf(w,
		`<div style="padding:1rem"><h2>%s</h2><p style="margin:.5rem 0;color:#666">%s</p>`,
		htmlEscape(groupfolder.NameOf(folder)), htmlEscape(folder))
	fmt.Fprintf(w, `<p style="margin-top:1rem"><strong>%d</strong> conversations</p>`, len(topics))
	fmt.Fprint(w, `</div>`)
	meShellClose(w)
}

// GET /me/folders/{folder...}/files
func (s *server) handleMeFiles(w http.ResponseWriter, r *http.Request) {
	// strip trailing /files from folder path value
	raw := strings.TrimPrefix(r.PathValue("folder"), "/")
	folder := strings.TrimSuffix(raw, "/files")
	if !userAllowedFolder(userGroups(r), folder) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	// Files live on davd, not in the store — return a minimal page.
	meShellOpen(w, r, groupfolder.NameOf(folder)+" files")
	fmt.Fprintf(w, `<div style="padding:1rem"><h2>%s — files</h2>
<p style="margin-top:.5rem;color:#666">Files are accessible via the WebDAV interface.</p>
</div>`, htmlEscape(groupfolder.NameOf(folder)))
	meShellClose(w)
}

// GET /me/settings
func (s *server) handleMeSettings(w http.ResponseWriter, r *http.Request) {
	sub := userSub(r)
	name := userName(r)
	meShellOpen(w, r, "settings")
	fmt.Fprintf(w, `<div style="padding:1rem;max-width:480px">
  <h2 style="margin-bottom:1rem">Settings</h2>
  <form hx-patch="/me/settings" hx-target="#settings-msg" hx-swap="innerHTML">
    <label>Display name<br>
      <input type="text" name="name" value="%s"
             style="width:100%%;padding:.4rem;margin:.3rem 0 1rem;border:1px solid #ccc;border-radius:4px">
    </label>
    <p style="color:#999;font-size:.85em;margin-bottom:1rem">sub: %s</p>
    <button type="submit" style="padding:.5rem 1.5rem">Save</button>
    <span id="settings-msg" style="margin-left:.5rem;color:#666"></span>
  </form>
</div>`, htmlEscape(name), htmlEscape(sub))
	meShellClose(w)
}

// PATCH /me/settings
func (s *server) handleMeSettingsPatch(w http.ResponseWriter, r *http.Request) {
	// Settings are proxyd-managed (identity headers); webd can't persist them.
	// Return a no-op acknowledgement.
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, "saved")
}

// handleMeThreadOrList dispatches GET/POST /me/chats/{folder...} to
// thread view, SSE, send, or new-chat redirect based on path suffix.
func (s *server) handleMeThreadOrList(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimPrefix(r.PathValue("folder"), "/")
	switch {
	case strings.HasSuffix(raw, "/sse"):
		s.handleMeSSE(w, r)
	case strings.HasSuffix(raw, "/send") && r.Method == http.MethodPost:
		s.handleMeSend(w, r)
	default:
		s.handleMeThread(w, r)
	}
}

// handleMeFolderOrFiles dispatches GET /me/folders/{folder...} to
// folder detail or file listing based on path suffix.
func (s *server) handleMeFolderOrFiles(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimPrefix(r.PathValue("folder"), "/")
	if strings.HasSuffix(raw, "/files") {
		s.handleMeFiles(w, r)
	} else {
		s.handleMeFolder(w, r)
	}
}

// meFolderTopic extracts folder and topic from a path like
// /me/chats/{folder...}/{topic} or /me/chats/{folder...}/{topic}/send.
// The last path segment is the topic; everything before it is the folder.
func meFolderTopic(r *http.Request) (folder, topic string) {
	// path value "folder" contains "folder/topic" or "folder/topic/send" etc.
	raw := strings.TrimPrefix(r.PathValue("folder"), "/")
	// strip known suffixes used by sub-routes
	for _, sfx := range []string{"/send", "/sse"} {
		if strings.HasSuffix(raw, sfx) {
			raw = raw[:len(raw)-len(sfx)]
			break
		}
	}
	idx := strings.LastIndex(raw, "/")
	if idx < 0 {
		return "", ""
	}
	return raw[:idx], raw[idx+1:]
}

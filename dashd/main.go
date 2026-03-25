package main

import (
	"database/sql"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"

	"github.com/onvos/arizuko/auth"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	dataDir := os.Getenv("DATA_DIR")
	groupsDir := filepath.Join(dataDir, "groups")

	dsn := os.Getenv("DB_PATH")
	if dsn == "" {
		if dataDir == "" {
			slog.Error("DB_PATH or DATA_DIR env required")
			os.Exit(1)
		}
		dsn = filepath.Join(dataDir, "store", "messages.db")
	}

	port := os.Getenv("DASH_PORT")
	if port == "" {
		port = ":8090"
	}
	if !strings.HasPrefix(port, ":") {
		port = ":" + port
	}

	authSecret := os.Getenv("AUTH_SECRET")
	webHost := os.Getenv("WEB_HOST")

	db, err := sql.Open("sqlite", dsn+"?mode=ro&_busy_timeout=5000")
	if err != nil {
		slog.Error("open db", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	slog.Info("dashd started", "db", dsn, "port", port)

	mux := http.NewServeMux()
	d := &dash{db: db, dbPath: dsn, groupsDir: groupsDir, secret: []byte(authSecret), webHost: webHost}
	d.registerRoutes(mux)

	srv := &http.Server{Addr: port, Handler: mux}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	go func() {
		<-stop
		slog.Info("dashd stopped")
		srv.Close()
	}()

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("listen", "err", err)
		os.Exit(1)
	}
}

type dash struct {
	db        *sql.DB
	dbPath    string
	groupsDir string
	secret    []byte
	webHost   string
}

func (d *dash) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /health", d.handleHealth)
	mux.HandleFunc("GET /dash/", d.requireAuth(d.handlePortal))
	mux.HandleFunc("GET /dash/status/", d.requireAuth(d.handleStatus))
	mux.HandleFunc("GET /dash/tasks/", d.requireAuth(d.handleTasks))
	mux.HandleFunc("GET /dash/activity/", d.requireAuth(d.handleActivity))
	mux.HandleFunc("GET /dash/groups/", d.requireAuth(d.handleGroups))
	mux.HandleFunc("GET /dash/memory/", d.requireAuth(d.handleMemory))
	// HTMX partials
	mux.HandleFunc("GET /dash/tasks/x/list", d.requireAuth(d.handleTasksPartial))
	mux.HandleFunc("GET /dash/activity/x/recent", d.requireAuth(d.handleActivityPartial))
}

func (d *dash) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if len(d.secret) == 0 {
			next(w, r)
			return
		}
		hdr := r.Header.Get("Authorization")
		if !strings.HasPrefix(hdr, "Bearer ") {
			http.Redirect(w, r, d.webHost+"/auth/login", http.StatusSeeOther)
			return
		}
		token := strings.TrimPrefix(hdr, "Bearer ")
		if _, err := auth.VerifyJWT(d.secret, token); err != nil {
			http.Redirect(w, r, d.webHost+"/auth/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

func (d *dash) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintln(w, `{"ok":true}`)
}

const portalHTML = `<!DOCTYPE html><html><head>
<meta charset=utf-8><title>Dashboard</title>
<script src="https://unpkg.com/htmx.org@1.9.12"></script>
<style>
body{font-family:monospace;max-width:900px;margin:2rem auto;padding:0 1rem}
.tiles{display:grid;grid-template-columns:repeat(3,1fr);gap:1rem}
.tile{border:1px solid #ccc;padding:1rem;border-radius:4px}
.tile h2{margin:0 0 .5rem;font-size:1rem}
.dot{display:inline-block;width:10px;height:10px;border-radius:50%}
.ok{background:#0a0}.warn{background:#fa0}.err{background:#d00}
a{color:inherit;text-decoration:none}.tile:hover{background:#f5f5f5}
</style>
</head><body>
<h1>arizuko</h1>
<div class="tiles">
  <a class="tile" href="/dash/status/"><h2>Status</h2><p>service health</p></a>
  <a class="tile" href="/dash/tasks/"><h2>Tasks</h2><p>scheduled jobs</p></a>
  <a class="tile" href="/dash/activity/"><h2>Activity</h2><p>message flow</p></a>
  <a class="tile" href="/dash/groups/"><h2>Groups</h2><p>group hierarchy</p></a>
  <a class="tile" href="/dash/memory/"><h2>Memory</h2><p>knowledge browser</p></a>
</div>
</body></html>`

func (d *dash) handlePortal(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/dash/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, portalHTML)
}

const pageTop = `<!DOCTYPE html><html><head>
<meta charset=utf-8><title>%s — arizuko</title>
<script src="https://unpkg.com/htmx.org@1.9.12"></script>
<style>
body{font-family:monospace;max-width:1100px;margin:2rem auto;padding:0 1rem}
table{border-collapse:collapse;width:100%%}
th,td{text-align:left;padding:.3rem .6rem;border-bottom:1px solid #eee}
th{background:#f5f5f5}
nav{margin-bottom:1rem}
nav a{margin-right:1rem;color:#555;text-decoration:none}
nav a:hover{text-decoration:underline}
pre{background:#f5f5f5;padding:1rem;overflow:auto;max-height:400px}
details summary{cursor:pointer}
</style>
</head><body>
<nav><a href="/dash/">portal</a><a href="/dash/status/">status</a>
<a href="/dash/tasks/">tasks</a><a href="/dash/activity/">activity</a>
<a href="/dash/groups/">groups</a><a href="/dash/memory/">memory</a></nav>
<h1>%s</h1>`

const pageBot = `</body></html>`

func (d *dash) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, pageTop, "Status", "Status")

	var groupCount, sessionCount int
	d.db.QueryRow(`SELECT COUNT(*) FROM registered_groups`).Scan(&groupCount)
	d.db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&sessionCount)

	fmt.Fprintf(w, `<p><b>DB:</b> %s</p>`, template.HTMLEscapeString(d.dbPath))
	fmt.Fprintf(w, `<p>Groups: %d &nbsp; Active sessions: %d</p>`, groupCount, sessionCount)

	rows, err := d.db.Query(`SELECT name, url FROM channels ORDER BY name`)
	if err == nil {
		defer rows.Close()
		fmt.Fprint(w, `<h2>Channels</h2><table><tr><th>Name</th><th>URL</th></tr>`)
		for rows.Next() {
			var name, url string
			rows.Scan(&name, &url)
			fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td></tr>`,
				template.HTMLEscapeString(name), template.HTMLEscapeString(url))
		}
		fmt.Fprint(w, `</table>`)
	}

	fmt.Fprint(w, pageBot)
}

func (d *dash) handleTasks(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, pageTop, "Tasks", "Tasks")
	fmt.Fprint(w, `<table hx-get="/dash/tasks/x/list" hx-trigger="every 10s" hx-target="tbody" hx-swap="innerHTML">
<thead><tr><th>ID</th><th>Group</th><th>Cron</th><th>Status</th><th>Created</th><th>Next Run</th></tr></thead>
<tbody>`)
	writeTaskRows(d.db, w)
	fmt.Fprint(w, `</tbody></table>`)
	fmt.Fprint(w, pageBot)
}

func (d *dash) handleTasksPartial(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	writeTaskRows(d.db, w)
}

func writeTaskRows(db *sql.DB, w http.ResponseWriter) {
	rows, err := db.Query(
		`SELECT id, owner, cron, status, created_at, next_run
		 FROM scheduled_tasks ORDER BY owner, id`)
	if err != nil {
		fmt.Fprintf(w, `<tr><td colspan=6>error: %s</td></tr>`, template.HTMLEscapeString(err.Error()))
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id, owner, status, createdAt string
		var cron, nextRun sql.NullString
		rows.Scan(&id, &owner, &cron, &status, &createdAt, &nextRun)
		fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>`,
			template.HTMLEscapeString(id),
			template.HTMLEscapeString(owner),
			template.HTMLEscapeString(nullStr(cron)),
			template.HTMLEscapeString(status),
			template.HTMLEscapeString(createdAt),
			template.HTMLEscapeString(nullStr(nextRun)),
		)
	}
}

func (d *dash) handleActivity(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, pageTop, "Activity", "Activity")
	fmt.Fprint(w, `<table hx-get="/dash/activity/x/recent" hx-trigger="every 10s" hx-target="tbody" hx-swap="innerHTML">
<thead><tr><th>Time</th><th>Source</th><th>Chat</th><th>Sender</th><th>Group</th><th>Verb</th><th>Content</th></tr></thead>
<tbody>`)
	writeActivityRows(d.db, w)
	fmt.Fprint(w, `</tbody></table>`)
	fmt.Fprint(w, pageBot)
}

func (d *dash) handleActivityPartial(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	writeActivityRows(d.db, w)
}

func writeActivityRows(db *sql.DB, w http.ResponseWriter) {
	rows, err := db.Query(
		`SELECT timestamp, source, chat_jid, sender, group_folder, verb, substr(content,1,80)
		 FROM messages ORDER BY timestamp DESC LIMIT 50`)
	if err != nil {
		fmt.Fprintf(w, `<tr><td colspan=7>error: %s</td></tr>`, template.HTMLEscapeString(err.Error()))
		return
	}
	defer rows.Close()
	for rows.Next() {
		var ts, chatJID, sender, content string
		var source, groupFolder, verb sql.NullString
		rows.Scan(&ts, &source, &chatJID, &sender, &groupFolder, &verb, &content)
		fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>`,
			template.HTMLEscapeString(ts),
			template.HTMLEscapeString(nullStr(source)),
			template.HTMLEscapeString(chatJID),
			template.HTMLEscapeString(sender),
			template.HTMLEscapeString(nullStr(groupFolder)),
			template.HTMLEscapeString(nullStr(verb)),
			template.HTMLEscapeString(content),
		)
	}
}

func (d *dash) handleGroups(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, pageTop, "Groups", "Groups")

	rows, err := d.db.Query(`SELECT folder, parent, name, state FROM registered_groups ORDER BY folder`)
	if err != nil {
		fmt.Fprintf(w, `<p>error: %s</p>`, template.HTMLEscapeString(err.Error()))
		fmt.Fprint(w, pageBot)
		return
	}
	defer rows.Close()

	fmt.Fprint(w, `<pre>`)
	for rows.Next() {
		var folder, name, state string
		var parent sql.NullString
		rows.Scan(&folder, &parent, &name, &state)
		depth := strings.Count(folder, "/")
		indent := strings.Repeat("  ", depth)
		label := ""
		if !parent.Valid || parent.String == "" {
			label = "(root) "
		}
		fmt.Fprintf(w, "%s%-30s %s%s\n",
			indent,
			template.HTMLEscapeString(folder),
			label,
			template.HTMLEscapeString(state),
		)
	}
	fmt.Fprint(w, `</pre>`)
	fmt.Fprint(w, pageBot)
}

func (d *dash) handleMemory(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	selectedGroup := r.URL.Query().Get("group")

	fmt.Fprintf(w, pageTop, "Memory", "Memory")

	// group dropdown
	rows, err := d.db.Query(`SELECT folder FROM registered_groups ORDER BY folder`)
	if err == nil {
		defer rows.Close()
		fmt.Fprint(w, `<form method="get">
<select name="group" onchange="this.form.submit()">
<option value="">-- select group --</option>`)
		for rows.Next() {
			var folder string
			rows.Scan(&folder)
			sel := ""
			if folder == selectedGroup {
				sel = ` selected`
			}
			fmt.Fprintf(w, `<option value="%s"%s>%s</option>`,
				template.HTMLEscapeString(folder),
				sel,
				template.HTMLEscapeString(folder),
			)
		}
		fmt.Fprint(w, `</select></form>`)
	}

	if selectedGroup != "" {
		d.renderMemorySection(w, selectedGroup)
	}

	fmt.Fprint(w, pageBot)
}

func (d *dash) renderMemorySection(w http.ResponseWriter, folder string) {
	groupDir := filepath.Join(d.groupsDir, filepath.Clean(folder))
	if !strings.HasPrefix(groupDir, d.groupsDir+string(filepath.Separator)) &&
		groupDir != d.groupsDir {
		fmt.Fprint(w, `<p>Invalid group path.</p>`)
		return
	}

	fmt.Fprint(w, `<h2>MEMORY.md</h2>`)
	memPath := filepath.Join(groupDir, "MEMORY.md")
	if content, err := os.ReadFile(memPath); err == nil {
		info, _ := os.Stat(memPath)
		mtime := ""
		if info != nil {
			mtime = info.ModTime().Format("2006-01-02 15:04")
		}
		fmt.Fprintf(w, `<p><small>%d bytes, modified %s</small></p><pre>%s</pre>`,
			len(content), mtime, template.HTMLEscapeString(string(content)))
	} else {
		fmt.Fprint(w, `<p><em>MEMORY.md not found</em></p>`)
	}

	claudePath := filepath.Join(groupDir, "CLAUDE.md")
	if content, err := os.ReadFile(claudePath); err == nil {
		fmt.Fprintf(w, `<details><summary>CLAUDE.md</summary><pre>%s</pre></details>`,
			template.HTMLEscapeString(string(content)))
	}

	diaryDir := filepath.Join(groupDir, "diary")
	entries, err := os.ReadDir(diaryDir)
	if err == nil {
		fmt.Fprint(w, `<h2>Diary</h2><ul>`)
		// reverse (newest first)
		for i := len(entries) - 1; i >= 0; i-- {
			e := entries[i]
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			firstLine := ""
			if data, err := os.ReadFile(filepath.Join(diaryDir, e.Name())); err == nil {
				lines := strings.SplitN(string(data), "\n", 2)
				if len(lines) > 0 {
					firstLine = strings.TrimSpace(lines[0])
				}
			}
			fmt.Fprintf(w, `<li><b>%s</b> %s</li>`,
				template.HTMLEscapeString(e.Name()),
				template.HTMLEscapeString(firstLine),
			)
		}
		fmt.Fprint(w, `</ul>`)
	}
}

func nullStr(n sql.NullString) string {
	if n.Valid {
		return n.String
	}
	return ""
}

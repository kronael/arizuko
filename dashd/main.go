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
	"syscall"

	_ "modernc.org/sqlite"
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

	db, err := sql.Open("sqlite", dsn+"?mode=ro&_busy_timeout=5000")
	if err != nil {
		slog.Error("open db", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	slog.Info("dashd started", "db", dsn, "port", port)

	mux := http.NewServeMux()
	d := &dash{db: db, dbPath: dsn, groupsDir: groupsDir}
	d.registerRoutes(mux)

	srv := &http.Server{Addr: port, Handler: mux}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
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
}

func (d *dash) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /health", d.handleHealth)
	mux.HandleFunc("GET /dash/", d.handlePortal)
	mux.HandleFunc("GET /dash/status/", d.handleStatus)
	mux.HandleFunc("GET /dash/tasks/", d.handleTasks)
	mux.HandleFunc("GET /dash/activity/", d.handleActivity)
	mux.HandleFunc("GET /dash/groups/", d.handleGroups)
	mux.HandleFunc("GET /dash/memory/", d.handleMemory)
	mux.HandleFunc("GET /dash/tasks/x/list", d.handleTasksPartial)
	mux.HandleFunc("GET /dash/activity/x/recent", d.handleActivityPartial)
}

func (d *dash) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintln(w, `{"ok":true}`)
}

var portalTmpl = template.Must(template.New("portal").Parse(`<!DOCTYPE html><html><head>
<meta charset=utf-8><title>Dashboard</title>
<script src="https://unpkg.com/htmx.org@1.9.12"></script>
<style>
body{font-family:monospace;max-width:900px;margin:2rem auto;padding:0 1rem}
.tiles{display:grid;grid-template-columns:repeat(3,1fr);gap:1rem}
.tile{border:1px solid #ccc;padding:1rem;border-radius:4px}
.tile h2{margin:0 0 .5rem;font-size:1rem}
.dot{display:inline-block;width:10px;height:10px;border-radius:50%;margin-left:.5rem}
.ok{background:#0a0}.warn{background:#fa0}.err{background:#d00}
a{color:inherit;text-decoration:none}.tile:hover{background:#f5f5f5}
</style>
</head><body>
<h1>arizuko</h1>
<div class="tiles">
  <a class="tile" href="/dash/status/"><h2>Status <span class="dot {{.StatusDot}}"></span></h2><p>service health</p></a>
  <a class="tile" href="/dash/tasks/"><h2>Tasks <span class="dot {{.TasksDot}}"></span></h2><p>scheduled jobs</p></a>
  <a class="tile" href="/dash/activity/"><h2>Activity</h2><p>message flow</p></a>
  <a class="tile" href="/dash/groups/"><h2>Groups</h2><p>group hierarchy</p></a>
  <a class="tile" href="/dash/memory/"><h2>Memory</h2><p>knowledge browser</p></a>
</div>
</body></html>`))

func (d *dash) handlePortal(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/dash/" {
		http.NotFound(w, r)
		return
	}

	var chanCount, erroredCount, failedTasks int
	d.db.QueryRow(`SELECT COUNT(*) FROM channels`).Scan(&chanCount)
	d.db.QueryRow(`SELECT COUNT(*) FROM chats WHERE errored=1`).Scan(&erroredCount)
	d.db.QueryRow(
		`SELECT COUNT(*) FROM task_run_logs WHERE status='error' AND run_at > datetime('now','-1 day')`,
	).Scan(&failedTasks)

	statusDot := "ok"
	if chanCount == 0 {
		statusDot = "err"
	} else if erroredCount > 0 {
		statusDot = "warn"
	}

	tasksDot := "ok"
	if failedTasks >= 3 {
		tasksDot = "err"
	} else if failedTasks > 0 {
		tasksDot = "warn"
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	portalTmpl.Execute(w, struct {
		StatusDot string
		TasksDot  string
	}{statusDot, tasksDot})
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
.banner-ok,.banner-warn,.banner-err{padding:.5rem 1rem;margin-bottom:1rem;border-radius:4px;font-weight:bold}
.banner-ok{background:#d4edda;color:#155724}
.banner-warn{background:#fff3cd;color:#856404}
.banner-err{background:#f8d7da;color:#721c24}
.group-detail{margin:.5rem 0 .5rem 1rem;font-size:.9em}
.group-detail td{padding:.2rem .4rem}
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

	var groupCount, sessionCount, chanCount, erroredCount int
	d.db.QueryRow(`SELECT COUNT(*) FROM groups`).Scan(&groupCount)
	d.db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&sessionCount)
	d.db.QueryRow(`SELECT COUNT(*) FROM channels`).Scan(&chanCount)
	d.db.QueryRow(`SELECT COUNT(*) FROM chats WHERE errored=1`).Scan(&erroredCount)

	bannerClass := "banner-ok"
	bannerText := fmt.Sprintf("%d channels, %d groups, %d errored chats", chanCount, groupCount, erroredCount)
	if chanCount == 0 {
		bannerClass = "banner-err"
	} else if erroredCount > 0 {
		bannerClass = "banner-warn"
	}
	fmt.Fprintf(w, `<div class="%s">%s</div>`, bannerClass, template.HTMLEscapeString(bannerText))

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
	d.writeTaskRows(w)
	fmt.Fprint(w, `</tbody></table>`)
	fmt.Fprint(w, pageBot)
}

func (d *dash) handleTasksPartial(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	d.writeTaskRows(w)
}

func (d *dash) writeTaskRows(w http.ResponseWriter) {
	rows, err := d.db.Query(
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
<thead><tr><th>Time</th><th>Source</th><th>Chat</th><th>Sender</th><th>Verb</th><th>Content</th></tr></thead>
<tbody>`)
	d.writeActivityRows(w)
	fmt.Fprint(w, `</tbody></table>`)
	fmt.Fprint(w, pageBot)
}

func (d *dash) handleActivityPartial(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	d.writeActivityRows(w)
}

func (d *dash) writeActivityRows(w http.ResponseWriter) {
	rows, err := d.db.Query(
		`SELECT timestamp, source, chat_jid, sender, verb, substr(content,1,80)
		 FROM messages ORDER BY timestamp DESC LIMIT 50`)
	if err != nil {
		fmt.Fprintf(w, `<tr><td colspan=6>error: %s</td></tr>`, template.HTMLEscapeString(err.Error()))
		return
	}
	defer rows.Close()
	for rows.Next() {
		var ts, source, chatJID, sender, content string
		var verb sql.NullString
		rows.Scan(&ts, &source, &chatJID, &sender, &verb, &content)
		fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>`,
			template.HTMLEscapeString(ts),
			template.HTMLEscapeString(source),
			template.HTMLEscapeString(chatJID),
			template.HTMLEscapeString(sender),
			template.HTMLEscapeString(nullStr(verb)),
			template.HTMLEscapeString(content),
		)
	}
}

func (d *dash) handleGroups(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, pageTop, "Groups", "Groups")

	rows, err := d.db.Query(`SELECT folder, parent, name, state FROM groups ORDER BY folder`)
	if err != nil {
		fmt.Fprintf(w, `<p>error: %s</p>`, template.HTMLEscapeString(err.Error()))
		fmt.Fprint(w, pageBot)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var folder, name, state string
		var parent sql.NullString
		rows.Scan(&folder, &parent, &name, &state)
		label := ""
		if !parent.Valid || parent.String == "" {
			label = " (root)"
		}
		fmt.Fprintf(w, `<details><summary>%s — %s%s [%s]</summary><div class="group-detail">`,
			template.HTMLEscapeString(folder),
			template.HTMLEscapeString(name),
			label,
			template.HTMLEscapeString(state),
		)
		fmt.Fprintf(w, `<p>Folder: %s &nbsp; Parent: %s</p>`,
			template.HTMLEscapeString(folder),
			template.HTMLEscapeString(nullStr(parent)),
		)
		d.writeGroupRoutes(w, folder)
		fmt.Fprint(w, `</div></details>`)
	}
	fmt.Fprint(w, pageBot)
}

func (d *dash) writeGroupRoutes(w http.ResponseWriter, folder string) {
	rows, err := d.db.Query(
		`SELECT seq, match, target FROM routes WHERE target=? OR target LIKE ? ORDER BY seq`,
		folder, folder+"/%")
	if err != nil {
		return
	}
	defer rows.Close()
	var n int
	for rows.Next() {
		if n == 0 {
			fmt.Fprint(w, `<table><tr><th>Seq</th><th>Match</th><th>Target</th></tr>`)
		}
		var seq int
		var match, target string
		rows.Scan(&seq, &match, &target)
		fmt.Fprintf(w, `<tr><td>%d</td><td>%s</td><td>%s</td></tr>`,
			seq,
			template.HTMLEscapeString(match),
			template.HTMLEscapeString(target),
		)
		n++
	}
	if n > 0 {
		fmt.Fprint(w, `</table>`)
	} else {
		fmt.Fprint(w, `<p><em>no routes</em></p>`)
	}
}

func (d *dash) handleMemory(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	selectedGroup := r.URL.Query().Get("group")

	fmt.Fprintf(w, pageTop, "Memory", "Memory")

	rows, err := d.db.Query(`SELECT folder FROM groups ORDER BY folder`)
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
	// Resolve symlinks and re-check the prefix. Without this, a symlink
	// inside a group folder can escape the sandbox and expose host files
	// (a compromised agent could `ln -s /etc /home/node/escape` then hit
	// /dash/memory/?group=<folder>/escape). EvalSymlinks fails cleanly on
	// missing paths — treat that as "nothing to show" and return.
	if real, err := filepath.EvalSymlinks(groupDir); err == nil {
		if !strings.HasPrefix(real, d.groupsDir+string(filepath.Separator)) &&
			real != d.groupsDir {
			fmt.Fprint(w, `<p>Invalid group path.</p>`)
			return
		}
		groupDir = real
	} else if !os.IsNotExist(err) {
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
		fmt.Fprint(w, `<h2>Diary</h2><details open><summary>entries</summary><ul>`)
		for i := len(entries) - 1; i >= 0; i-- {
			e := entries[i]
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			summary := mdSummary(filepath.Join(diaryDir, e.Name()))
			fmt.Fprintf(w, `<li><b>%s</b> %s</li>`,
				template.HTMLEscapeString(e.Name()),
				template.HTMLEscapeString(summary),
			)
		}
		fmt.Fprint(w, `</ul></details>`)
	}

	renderMdDir(w, groupDir, "episodes", "Episodes")
	renderMdDir(w, groupDir, "users", "Users")
	renderMdDir(w, groupDir, "facts", "Facts")
}

func mdSummary(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	content := string(data)
	if strings.HasPrefix(content, "---\n") {
		end := strings.Index(content[4:], "\n---")
		if end >= 0 {
			lines := strings.Split(content[4:4+end], "\n")
			for i, line := range lines {
				trimmed := strings.TrimSpace(line)
				if !strings.HasPrefix(trimmed, "summary:") {
					continue
				}
				val := strings.TrimSpace(strings.TrimPrefix(trimmed, "summary:"))
				if val != "" && val != "|" && val != ">" {
					return strings.Trim(val, `"'`)
				}
				var parts []string
				for _, bl := range lines[i+1:] {
					if len(bl) == 0 {
						continue
					}
					if bl[0] == ' ' || bl[0] == '\t' {
						parts = append(parts, strings.TrimSpace(bl))
					} else {
						break
					}
				}
				return strings.Join(parts, " ")
			}
		}
	}
	for _, l := range strings.Split(content, "\n") {
		l = strings.TrimSpace(l)
		if l != "" && l != "---" {
			return l
		}
	}
	return ""
}

func renderMdDir(w http.ResponseWriter, groupDir, sub, title string) {
	dir := filepath.Join(groupDir, sub)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var mdFiles []os.DirEntry
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			mdFiles = append(mdFiles, e)
		}
	}
	if len(mdFiles) == 0 {
		return
	}
	fmt.Fprintf(w, `<h2>%s</h2><details><summary>%d files</summary><ul>`,
		template.HTMLEscapeString(title), len(mdFiles))
	for i := len(mdFiles) - 1; i >= 0; i-- {
		e := mdFiles[i]
		summary := mdSummary(filepath.Join(dir, e.Name()))
		fmt.Fprintf(w, `<li><b>%s</b> %s</li>`,
			template.HTMLEscapeString(e.Name()),
			template.HTMLEscapeString(summary),
		)
	}
	fmt.Fprint(w, `</ul></details>`)
}

func nullStr(n sql.NullString) string {
	if n.Valid {
		return n.String
	}
	return ""
}

package main

import (
	"database/sql"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/onvos/arizuko/diary"
	"github.com/onvos/arizuko/theme"
	_ "modernc.org/sqlite"
)

// Caps bound memory from adversarial writes by a compromised agent.
const (
	maxFileBytes  = 1 << 20
	maxDirEntries = 100
)

var errEscape = errors.New("path escapes sandbox")

func readCapped(path string) ([]byte, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return nil, false, err
	}
	truncated := len(data) > maxFileBytes
	if truncated {
		data = data[:maxFileBytes]
	}
	return data, truncated, nil
}

// safeJoin resolves base+leaf through symlinks and rejects any escape.
// Missing paths propagate os.ErrNotExist.
func safeJoin(base, leaf string) (string, error) {
	p := filepath.Join(base, leaf)
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", err
	}
	if real != base && !strings.HasPrefix(real, base+string(filepath.Separator)) {
		return "", errEscape
	}
	return real, nil
}

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
		port = ":8080"
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

const dashNav = `<nav><a href="/dash/">arizuko</a><a href="/dash/status/">status</a><a href="/dash/tasks/">tasks</a><a href="/dash/activity/">activity</a><a href="/dash/groups/">groups</a><a href="/dash/memory/">memory</a></nav><button class="theme-toggle"></button>`

func dashHead(title string) string {
	return `<head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">` +
		`<title>` + title + ` — arizuko</title>` +
		`<script src="https://unpkg.com/htmx.org@1.9.12"></script>` +
		`<style>` + theme.CSS + `</style>` + theme.ThemeScript + theme.ToggleScript + `</head>`
}

var portalTmpl = template.Must(template.New("portal").Parse(`<!DOCTYPE html><html>{{.Head}}<body>
<div class="page-wide">` + dashNav + `
<h1>arizuko</h1>
<p style="color:var(--dim);margin-bottom:1em">operator dashboard</p>
<div class="tiles">
<a class="tile" href="/dash/status/"><h2>status<span class="dot {{.StatusDot}}"></span></h2><p>service health</p></a>
<a class="tile" href="/dash/tasks/"><h2>tasks<span class="dot {{.TasksDot}}"></span></h2><p>scheduled jobs</p></a>
<a class="tile" href="/dash/activity/"><h2>activity</h2><p>message flow</p></a>
<a class="tile" href="/dash/groups/"><h2>groups</h2><p>group hierarchy</p></a>
<a class="tile" href="/dash/memory/"><h2>memory</h2><p>knowledge browser</p></a>
</div>
</div></body></html>`))

func (d *dash) handlePortal(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/dash/" {
		http.NotFound(w, r)
		return
	}

	var chanCount, erroredCount, failedTasks int
	for _, q := range []struct {
		sql string
		dst *int
	}{
		{`SELECT COUNT(*) FROM channels`, &chanCount},
		{`SELECT COUNT(*) FROM chats WHERE errored=1`, &erroredCount},
		{`SELECT COUNT(*) FROM task_run_logs WHERE status='error' AND run_at > datetime('now','-1 day')`, &failedTasks},
	} {
		if err := d.db.QueryRow(q.sql).Scan(q.dst); err != nil {
			slog.Warn("portal: scan", "sql", q.sql, "err", err)
		}
	}

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
	if err := portalTmpl.Execute(w, struct {
		Head      template.HTML
		StatusDot string
		TasksDot  string
	}{template.HTML(dashHead("arizuko")), statusDot, tasksDot}); err != nil {
		slog.Warn("portal: template execute", "err", err)
	}
}

func pageTop(w http.ResponseWriter, title string) {
	fmt.Fprintf(w, `<!DOCTYPE html><html>%s<body><div class="page-wide">%s<h1>%s</h1>`,
		dashHead(title), dashNav, template.HTMLEscapeString(title))
}

const pageBot = `</div></body></html>`

func (d *dash) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageTop(w, "Status")

	var groupCount, sessionCount, chanCount, erroredCount int
	for _, q := range []struct {
		sql string
		dst *int
	}{
		{`SELECT COUNT(*) FROM groups`, &groupCount},
		{`SELECT COUNT(*) FROM sessions`, &sessionCount},
		{`SELECT COUNT(*) FROM channels`, &chanCount},
		{`SELECT COUNT(*) FROM chats WHERE errored=1`, &erroredCount},
	} {
		if err := d.db.QueryRow(q.sql).Scan(q.dst); err != nil {
			slog.Warn("status: scan", "sql", q.sql, "err", err)
		}
	}

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
			if err := rows.Scan(&name, &url); err != nil {
				slog.Warn("status: scan channels row", "err", err)
				continue
			}
			fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td></tr>`,
				template.HTMLEscapeString(name), template.HTMLEscapeString(url))
		}
		if err := rows.Err(); err != nil {
			slog.Warn("status: channels rows", "err", err)
			fmt.Fprintf(w, `<tr><td colspan=2>rows error: %s</td></tr>`,
				template.HTMLEscapeString(err.Error()))
		}
		fmt.Fprint(w, `</table>`)
	} else {
		slog.Warn("status: query channels", "err", err)
		fmt.Fprintf(w, `<p class="banner-err">channels query error: %s</p>`,
			template.HTMLEscapeString(err.Error()))
	}

	fmt.Fprint(w, pageBot)
}

func (d *dash) handleTasks(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageTop(w, "Tasks")
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
		slog.Warn("tasks: query", "err", err)
		fmt.Fprintf(w, `<tr><td colspan=6>error: %s</td></tr>`, template.HTMLEscapeString(err.Error()))
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id, owner, status, createdAt string
		var cron, nextRun sql.NullString
		if err := rows.Scan(&id, &owner, &cron, &status, &createdAt, &nextRun); err != nil {
			slog.Warn("tasks: scan row", "err", err)
			fmt.Fprintf(w, `<tr><td colspan=6>scan error: %s</td></tr>`,
				template.HTMLEscapeString(err.Error()))
			continue
		}
		fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>`,
			template.HTMLEscapeString(id),
			template.HTMLEscapeString(owner),
			template.HTMLEscapeString(nullStr(cron)),
			template.HTMLEscapeString(status),
			template.HTMLEscapeString(createdAt),
			template.HTMLEscapeString(nullStr(nextRun)),
		)
	}
	if err := rows.Err(); err != nil {
		slog.Warn("tasks: rows", "err", err)
		fmt.Fprintf(w, `<tr><td colspan=6>rows error: %s</td></tr>`,
			template.HTMLEscapeString(err.Error()))
	}
}

func (d *dash) handleActivity(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageTop(w, "Activity")
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
		slog.Warn("activity: query", "err", err)
		fmt.Fprintf(w, `<tr><td colspan=6>error: %s</td></tr>`, template.HTMLEscapeString(err.Error()))
		return
	}
	defer rows.Close()
	for rows.Next() {
		var ts, source, chatJID, sender, content string
		var verb sql.NullString
		if err := rows.Scan(&ts, &source, &chatJID, &sender, &verb, &content); err != nil {
			slog.Warn("activity: scan row", "err", err)
			fmt.Fprintf(w, `<tr><td colspan=6>scan error: %s</td></tr>`,
				template.HTMLEscapeString(err.Error()))
			continue
		}
		fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>`,
			template.HTMLEscapeString(ts),
			template.HTMLEscapeString(source),
			template.HTMLEscapeString(chatJID),
			template.HTMLEscapeString(sender),
			template.HTMLEscapeString(nullStr(verb)),
			template.HTMLEscapeString(content),
		)
	}
	if err := rows.Err(); err != nil {
		slog.Warn("activity: rows", "err", err)
		fmt.Fprintf(w, `<tr><td colspan=6>rows error: %s</td></tr>`,
			template.HTMLEscapeString(err.Error()))
	}
}

func (d *dash) handleGroups(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageTop(w, "Groups")

	rows, err := d.db.Query(`SELECT folder, parent, name, state FROM groups ORDER BY folder`)
	if err != nil {
		slog.Warn("groups: query", "err", err)
		fmt.Fprintf(w, `<p>error: %s</p>`, template.HTMLEscapeString(err.Error()))
		fmt.Fprint(w, pageBot)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var folder, name, state string
		var parent sql.NullString
		if err := rows.Scan(&folder, &parent, &name, &state); err != nil {
			slog.Warn("groups: scan row", "err", err)
			fmt.Fprintf(w, `<p class="banner-err">scan error: %s</p>`,
				template.HTMLEscapeString(err.Error()))
			continue
		}
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
	if err := rows.Err(); err != nil {
		slog.Warn("groups: rows", "err", err)
		fmt.Fprintf(w, `<p class="banner-err">rows error: %s</p>`,
			template.HTMLEscapeString(err.Error()))
	}
	fmt.Fprint(w, pageBot)
}

func (d *dash) writeGroupRoutes(w http.ResponseWriter, folder string) {
	rows, err := d.db.Query(
		`SELECT seq, match, target FROM routes WHERE target=? OR target LIKE ? ORDER BY seq`,
		folder, folder+"/%")
	if err != nil {
		slog.Warn("groups: routes query", "err", err, "folder", folder)
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
		if err := rows.Scan(&seq, &match, &target); err != nil {
			slog.Warn("groups: routes scan row", "err", err, "folder", folder)
			continue
		}
		fmt.Fprintf(w, `<tr><td>%d</td><td>%s</td><td>%s</td></tr>`,
			seq,
			template.HTMLEscapeString(match),
			template.HTMLEscapeString(target),
		)
		n++
	}
	if err := rows.Err(); err != nil {
		slog.Warn("groups: routes rows", "err", err, "folder", folder)
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

	pageTop(w, "Memory")

	rows, err := d.db.Query(`SELECT folder FROM groups ORDER BY folder`)
	if err == nil {
		defer rows.Close()
		fmt.Fprint(w, `<form method="get">
<select name="group" onchange="this.form.submit()">
<option value="">-- select group --</option>`)
		for rows.Next() {
			var folder string
			if err := rows.Scan(&folder); err != nil {
				slog.Warn("memory: scan row", "err", err)
				continue
			}
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
		if err := rows.Err(); err != nil {
			slog.Warn("memory: rows", "err", err)
		}
		fmt.Fprint(w, `</select></form>`)
	} else {
		slog.Warn("memory: query groups", "err", err)
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
	// Re-check prefix after EvalSymlinks to reject symlink escapes.
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
	renderCappedFile(w, groupDir, "MEMORY.md", true)

	renderCappedFile(w, groupDir, "CLAUDE.md", false)

	renderEntries(w, groupDir, "diary", "Diary", true)
	renderEntries(w, groupDir, "episodes", "Episodes", false)
	renderEntries(w, groupDir, "users", "Users", false)
	renderEntries(w, groupDir, "facts", "Facts", false)
}

func renderCappedFile(w http.ResponseWriter, groupDir, leaf string, showMissing bool) {
	path, err := safeJoin(groupDir, leaf)
	if err != nil {
		if errors.Is(err, errEscape) {
			slog.Warn("memory: symlink escape", "file", leaf, "groupDir", groupDir)
			fmt.Fprintf(w, `<p><em>%s unavailable (symlink escape)</em></p>`,
				template.HTMLEscapeString(leaf))
			return
		}
		if os.IsNotExist(err) && showMissing {
			fmt.Fprintf(w, `<p><em>%s not found</em></p>`, template.HTMLEscapeString(leaf))
		}
		return
	}
	data, truncated, err := readCapped(path)
	if err != nil {
		slog.Warn("memory: read file", "err", err, "path", path)
		if showMissing {
			fmt.Fprintf(w, `<p><em>%s read error</em></p>`, template.HTMLEscapeString(leaf))
		}
		return
	}
	info, _ := os.Stat(path)
	mtime := ""
	if info != nil {
		mtime = info.ModTime().Format("2006-01-02 15:04")
	}
	truncNote := ""
	if truncated {
		truncNote = fmt.Sprintf(" (truncated at %d bytes)", maxFileBytes)
	}
	if leaf == "MEMORY.md" {
		fmt.Fprintf(w, `<p><small>%d bytes, modified %s%s</small></p><pre>%s</pre>`,
			len(data), mtime, truncNote, template.HTMLEscapeString(string(data)))
	} else {
		fmt.Fprintf(w, `<details><summary>%s%s</summary><pre>%s</pre></details>`,
			template.HTMLEscapeString(leaf), truncNote, template.HTMLEscapeString(string(data)))
	}
}

func renderEntries(w http.ResponseWriter, groupDir, sub, title string, openDetails bool) {
	dirPath, err := safeJoin(groupDir, sub)
	if err != nil {
		if errors.Is(err, errEscape) {
			slog.Warn("memory: dir symlink escape", "sub", sub, "groupDir", groupDir)
		}
		return
	}
	entries, err := os.ReadDir(dirPath)
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
	total := len(mdFiles)
	limit := total
	if limit > maxDirEntries {
		limit = maxDirEntries
	}
	summaryLabel := fmt.Sprintf("%d files", total)
	if total > maxDirEntries {
		summaryLabel = fmt.Sprintf("%d files (showing newest %d)", total, maxDirEntries)
	}
	openAttr := ""
	if openDetails {
		openAttr = " open"
	}
	fmt.Fprintf(w, `<h2>%s</h2><details%s><summary>%s</summary><ul>`,
		template.HTMLEscapeString(title), openAttr, template.HTMLEscapeString(summaryLabel))
	shown := 0
	for i := total - 1; i >= 0 && shown < limit; i-- {
		e := mdFiles[i]
		leafPath, err := safeJoin(dirPath, e.Name())
		if err != nil {
			if errors.Is(err, errEscape) {
				slog.Warn("memory: entry symlink escape", "file", e.Name(), "dir", dirPath)
			}
			continue
		}
		summary := mdSummary(leafPath)
		fmt.Fprintf(w, `<li><b>%s</b> %s</li>`,
			template.HTMLEscapeString(e.Name()),
			template.HTMLEscapeString(summary),
		)
		shown++
	}
	fmt.Fprint(w, `</ul></details>`)
}

func mdSummary(path string) string {
	if s := diary.ExtractSummary(path); s != "" {
		return s
	}
	data, _, err := readCapped(path)
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(string(data), "\n") {
		l = strings.TrimSpace(l)
		if l != "" && l != "---" {
			return l
		}
	}
	return ""
}

func nullStr(n sql.NullString) string {
	if n.Valid {
		return n.String
	}
	return ""
}

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

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/diary"
	"github.com/kronael/arizuko/theme"
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

var esc = template.HTMLEscapeString

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

	srv := &http.Server{Addr: port, Handler: chanlib.LogMiddleware(mux)}

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
	mux.HandleFunc("GET /dash/profile/", d.handleProfile)
	mux.HandleFunc("PUT /dash/memory/", d.handleMemoryWrite)
	mux.HandleFunc("DELETE /dash/memory/", d.handleMemoryDelete)
	mux.HandleFunc("GET /dash/tasks/x/list", d.handleTasksPartial)
	mux.HandleFunc("GET /dash/activity/x/recent", d.handleActivityPartial)
}

func (d *dash) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintln(w, `{"ok":true}`)
}

const dashNav = `<nav><a href="/dash/">arizuko</a><a href="/dash/status/">status</a><a href="/dash/tasks/">tasks</a><a href="/dash/activity/">activity</a><a href="/dash/groups/">groups</a><a href="/dash/memory/">memory</a><a href="/dash/profile/">profile</a></nav><button class="theme-toggle"></button>`

func dashHead(title string) string {
	return `<head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">` +
		`<title>` + title + ` — arizuko</title>` +
		`<script src="https://unpkg.com/htmx.org@1.9.12"></script>` +
		`<style>` + theme.CSS + `</style>` + theme.ThemeScript + theme.ToggleScript + `</head>`
}

var portalTmpl = template.Must(template.New("portal").Parse(`<!DOCTYPE html><html>{{.Head}}<body>
<div class="page-wide">` + dashNav + `
<h1>arizuko</h1>
<p class="dim">Operator dashboard</p>
{{if .Err}}<div class="banner-err">portal: {{.Err}}</div>{{end}}
<div class="tiles">
<a class="tile" href="/dash/status/"><h2>status<span class="dot {{.StatusDot}}"></span></h2><p>service health</p></a>
<a class="tile" href="/dash/tasks/"><h2>tasks<span class="dot {{.TasksDot}}"></span></h2><p>scheduled jobs</p></a>
<a class="tile" href="/dash/activity/"><h2>activity</h2><p>message flow</p></a>
<a class="tile" href="/dash/groups/"><h2>groups</h2><p>group hierarchy</p></a>
<a class="tile" href="/dash/memory/"><h2>memory</h2><p>knowledge browser</p></a>
<a class="tile" href="/dash/profile/"><h2>profile</h2><p>linked accounts</p></a>
</div>
</div></body></html>`))

func (d *dash) handlePortal(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/dash/" {
		http.NotFound(w, r)
		return
	}

	var chanCount, erroredCount, failedTasks int
	var scanErrs []string
	for _, q := range []struct {
		sql string
		dst *int
	}{
		{`SELECT COUNT(*) FROM channels`, &chanCount},
		{`SELECT COUNT(DISTINCT chat_jid) FROM messages WHERE errored=1`, &erroredCount},
		{`SELECT COUNT(*) FROM task_run_logs WHERE status='error' AND run_at > datetime('now','-1 day')`, &failedTasks},
	} {
		if err := d.db.QueryRow(q.sql).Scan(q.dst); err != nil {
			slog.Warn("portal: scan", "sql", q.sql, "err", err)
			scanErrs = append(scanErrs, err.Error())
		}
	}

	errMsg := strings.Join(scanErrs, "; ")

	statusDot := "ok"
	if len(scanErrs) > 0 || chanCount == 0 {
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
		Err       string
	}{template.HTML(dashHead("arizuko")), statusDot, tasksDot, errMsg}); err != nil {
		slog.Warn("portal: template execute", "err", err)
	}
}

func pageTop(w http.ResponseWriter, title string) {
	fmt.Fprintf(w, `<!DOCTYPE html><html>%s<body><div class="page-wide">%s<h1>%s</h1>`,
		dashHead(title), dashNav, esc(title))
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
		{`SELECT COUNT(DISTINCT chat_jid) FROM messages WHERE errored=1`, &erroredCount},
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
	fmt.Fprintf(w, `<p class="dim">Service health</p>`)
	fmt.Fprintf(w, `<div class="%s">%s</div>`, bannerClass, esc(bannerText))

	fmt.Fprintf(w, `<table><tr><th>DB</th><td><code>%s</code></td></tr>`, esc(d.dbPath))
	fmt.Fprintf(w, `<tr><th>Groups</th><td>%d</td></tr>`, groupCount)
	fmt.Fprintf(w, `<tr><th>Active sessions</th><td>%d</td></tr></table>`, sessionCount)

	rows, err := d.db.Query(`SELECT name, url FROM channels ORDER BY name LIMIT 500`)
	if err == nil {
		defer rows.Close()
		fmt.Fprint(w, `<h2>Channels</h2><table><thead><tr><th>Name</th><th>URL</th></tr></thead><tbody>`)
		var n int
		for rows.Next() {
			var name, url string
			if err := rows.Scan(&name, &url); err != nil {
				slog.Warn("status: scan channels row", "err", err)
				continue
			}
			fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td></tr>`,
				esc(name), esc(url))
			n++
		}
		if err := rows.Err(); err != nil {
			slog.Warn("status: channels rows", "err", err)
			fmt.Fprintf(w, `<tr><td colspan=2 class="empty">rows error: %s</td></tr>`,
				esc(err.Error()))
		} else if n == 0 {
			fmt.Fprint(w, `<tr><td colspan=2 class="empty">No channels registered.</td></tr>`)
		}
		fmt.Fprint(w, `</tbody></table>`)
	} else {
		slog.Warn("status: query channels", "err", err)
		fmt.Fprintf(w, `<div class="banner-err">channels query error: %s</div>`,
			esc(err.Error()))
	}

	fmt.Fprint(w, pageBot)
}

func (d *dash) handleTasks(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageTop(w, "Tasks")
	fmt.Fprint(w, `<p class="dim">Scheduled jobs. Auto-refreshes every 10s.</p>`)
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
		 FROM scheduled_tasks ORDER BY owner, id LIMIT 500`)
	if err != nil {
		slog.Warn("tasks: query", "err", err)
		fmt.Fprintf(w, `<tr><td colspan=6 class="empty">error: %s</td></tr>`, esc(err.Error()))
		return
	}
	defer rows.Close()
	var n int
	for rows.Next() {
		var id, owner, status, createdAt string
		var cron, nextRun sql.NullString
		if err := rows.Scan(&id, &owner, &cron, &status, &createdAt, &nextRun); err != nil {
			slog.Warn("tasks: scan row", "err", err)
			fmt.Fprintf(w, `<tr><td colspan=6 class="empty">scan error: %s</td></tr>`,
				esc(err.Error()))
			continue
		}
		dot := "dot-ok"
		if status != "active" {
			dot = "dot-warn"
		}
		fmt.Fprintf(w, `<tr><td><code>%s</code></td><td>%s</td><td><code>%s</code></td>`+
			`<td><span class="dot %s"></span>%s</td><td>%s</td><td>%s</td></tr>`,
			esc(id),
			esc(owner),
			esc(cron.String),
			dot, esc(status),
			esc(createdAt),
			esc(nextRun.String),
		)
		n++
	}
	if err := rows.Err(); err != nil {
		slog.Warn("tasks: rows", "err", err)
		fmt.Fprintf(w, `<tr><td colspan=6 class="empty">rows error: %s</td></tr>`,
			esc(err.Error()))
		return
	}
	if n == 0 {
		fmt.Fprint(w, `<tr><td colspan=6 class="empty">No scheduled tasks. `+
			`Ask the agent to schedule one (e.g. "remind me every morning at 8am").</td></tr>`)
	}
}

func (d *dash) handleActivity(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageTop(w, "Activity")
	fmt.Fprint(w, `<p class="dim">Last 50 messages across all channels. Auto-refreshes every 10s.</p>`)
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
		fmt.Fprintf(w, `<tr><td colspan=6 class="empty">error: %s</td></tr>`, esc(err.Error()))
		return
	}
	defer rows.Close()
	var n int
	for rows.Next() {
		var ts, source, chatJID, sender, content string
		var verb sql.NullString
		if err := rows.Scan(&ts, &source, &chatJID, &sender, &verb, &content); err != nil {
			slog.Warn("activity: scan row", "err", err)
			fmt.Fprintf(w, `<tr><td colspan=6 class="empty">scan error: %s</td></tr>`,
				esc(err.Error()))
			continue
		}
		fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td><td><code>%s</code></td><td><code>%s</code></td><td>%s</td><td>%s</td></tr>`,
			esc(ts),
			esc(source),
			esc(chatJID),
			esc(sender),
			esc(verb.String),
			esc(content),
		)
		n++
	}
	if err := rows.Err(); err != nil {
		slog.Warn("activity: rows", "err", err)
		fmt.Fprintf(w, `<tr><td colspan=6 class="empty">rows error: %s</td></tr>`,
			esc(err.Error()))
		return
	}
	if n == 0 {
		fmt.Fprint(w, `<tr><td colspan=6 class="empty">No messages yet.</td></tr>`)
	}
}

func (d *dash) handleGroups(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageTop(w, "Groups")
	fmt.Fprint(w, `<p class="dim">Group hierarchy. Expand a row to see routing rules.</p>`)

	rows, err := d.db.Query(`SELECT folder, parent, name FROM groups ORDER BY folder LIMIT 500`)
	if err != nil {
		slog.Warn("groups: query", "err", err)
		fmt.Fprintf(w, `<div class="banner-err">error: %s</div>`, esc(err.Error()))
		fmt.Fprint(w, pageBot)
		return
	}
	defer rows.Close()

	var n int
	for rows.Next() {
		var folder, name string
		var parent sql.NullString
		if err := rows.Scan(&folder, &parent, &name); err != nil {
			slog.Warn("groups: scan row", "err", err)
			fmt.Fprintf(w, `<div class="banner-err">scan error: %s</div>`,
				esc(err.Error()))
			continue
		}
		label := ""
		if !parent.Valid || parent.String == "" {
			label = ` <span class="dim">(root)</span>`
		}
		fmt.Fprintf(w, `<details><summary><code>%s</code> — %s%s</summary><div class="group-detail">`,
			esc(folder),
			esc(name),
			label,
		)
		parentDisp := parent.String
		if parentDisp == "" {
			parentDisp = "—"
		}
		fmt.Fprintf(w, `<p class="dim">Folder <code>%s</code> &middot; Parent <code>%s</code></p>`,
			esc(folder),
			esc(parentDisp),
		)
		d.writeGroupRoutes(w, folder)
		fmt.Fprint(w, `</div></details>`)
		n++
	}
	if err := rows.Err(); err != nil {
		slog.Warn("groups: rows", "err", err)
		fmt.Fprintf(w, `<div class="banner-err">rows error: %s</div>`,
			esc(err.Error()))
	} else if n == 0 {
		fmt.Fprint(w, `<p class="empty">No groups configured. `+
			`Run <code>arizuko invite</code> to onboard a user.</p>`)
	}
	fmt.Fprint(w, pageBot)
}

func (d *dash) writeGroupRoutes(w http.ResponseWriter, folder string) {
	rows, err := d.db.Query(
		`SELECT seq, match, target FROM routes WHERE target=? OR target LIKE ? ORDER BY seq LIMIT 200`,
		folder, folder+"/%")
	if err != nil {
		slog.Warn("groups: routes query", "err", err, "folder", folder)
		fmt.Fprintf(w, `<p class="banner-err">routes error: %s</p>`,
			esc(err.Error()))
		return
	}
	defer rows.Close()
	var n int
	for rows.Next() {
		if n == 0 {
			fmt.Fprint(w, `<table><thead><tr><th>Seq</th><th>Match</th><th>Target</th></tr></thead><tbody>`)
		}
		var seq int
		var match, target string
		if err := rows.Scan(&seq, &match, &target); err != nil {
			slog.Warn("groups: routes scan row", "err", err, "folder", folder)
			continue
		}
		fmt.Fprintf(w, `<tr><td>%d</td><td><code>%s</code></td><td><code>%s</code></td></tr>`,
			seq,
			esc(match),
			esc(target),
		)
		n++
	}
	if err := rows.Err(); err != nil {
		slog.Warn("groups: routes rows", "err", err, "folder", folder)
	}
	if n > 0 {
		fmt.Fprint(w, `</tbody></table>`)
	} else {
		fmt.Fprint(w, `<p class="empty">no routes targeting this group</p>`)
	}
}

// Allowed: MEMORY.md, .claude/CLAUDE.md, *.md under diary/facts/users/episodes/ (no subdirs).
func memoryPathAllowed(rel string) bool {
	if rel == "" || strings.HasPrefix(rel, "/") || strings.Contains(rel, "..") {
		return false
	}
	clean := filepath.Clean(rel)
	if clean != rel {
		return false
	}
	if clean == "MEMORY.md" || clean == ".claude/CLAUDE.md" {
		return true
	}
	for _, sub := range []string{"diary/", "facts/", "users/", "episodes/"} {
		if strings.HasPrefix(clean, sub) && strings.HasSuffix(clean, ".md") &&
			!strings.ContainsAny(clean[len(sub):], "/") {
			return true
		}
	}
	return false
}

func parseMemoryPath(urlPath string) (string, string) {
	const prefix = "/dash/memory/"
	if !strings.HasPrefix(urlPath, prefix) {
		return "", ""
	}
	rest := strings.TrimPrefix(urlPath, prefix)
	i := strings.IndexByte(rest, '/')
	if i < 0 {
		return "", ""
	}
	folder := rest[:i]
	rel := rest[i+1:]
	if folder == "" || rel == "" {
		return "", ""
	}
	return folder, rel
}

func (d *dash) resolveMemoryFile(folder, rel string) (string, error) {
	if !memoryPathAllowed(rel) {
		return "", errors.New("path not allowed")
	}
	groupDir := filepath.Join(d.groupsDir, filepath.Clean(folder))
	if !strings.HasPrefix(groupDir, d.groupsDir+string(filepath.Separator)) &&
		groupDir != d.groupsDir {
		return "", errEscape
	}
	real, err := filepath.EvalSymlinks(groupDir)
	if err != nil {
		return "", err
	}
	if real != d.groupsDir &&
		!strings.HasPrefix(real, d.groupsDir+string(filepath.Separator)) {
		return "", errEscape
	}
	target := filepath.Join(real, rel)
	if st, err := os.Lstat(target); err == nil && st.Mode()&os.ModeSymlink != 0 {
		return "", errEscape
	}
	if !strings.HasPrefix(target, real+string(filepath.Separator)) {
		return "", errEscape
	}
	return target, nil
}

func (d *dash) handleMemoryWrite(w http.ResponseWriter, r *http.Request) {
	folder, rel := parseMemoryPath(r.URL.Path)
	if folder == "" {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	target, err := d.resolveMemoryFile(folder, rel)
	if err != nil {
		slog.Warn("memory write: resolve", "folder", folder, "rel", rel, "err", err)
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxFileBytes+1))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	if len(body) > maxFileBytes {
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		return
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		slog.Warn("memory write: mkdir", "path", target, "err", err)
		http.Error(w, "mkdir", http.StatusInternalServerError)
		return
	}
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		slog.Warn("memory write: tmp", "path", tmp, "err", err)
		http.Error(w, "write", http.StatusInternalServerError)
		return
	}
	if err := os.Rename(tmp, target); err != nil {
		os.Remove(tmp)
		slog.Warn("memory write: rename", "path", target, "err", err)
		http.Error(w, "rename", http.StatusInternalServerError)
		return
	}
	slog.Info("memory write", "folder", folder, "rel", rel, "bytes", len(body))
	w.WriteHeader(http.StatusNoContent)
}

func (d *dash) handleMemoryDelete(w http.ResponseWriter, r *http.Request) {
	folder, rel := parseMemoryPath(r.URL.Path)
	if folder == "" {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	target, err := d.resolveMemoryFile(folder, rel)
	if err != nil {
		slog.Warn("memory delete: resolve", "folder", folder, "rel", rel, "err", err)
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := os.Remove(target); err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		slog.Warn("memory delete: remove", "path", target, "err", err)
		http.Error(w, "remove", http.StatusInternalServerError)
		return
	}
	slog.Info("memory delete", "folder", folder, "rel", rel)
	w.WriteHeader(http.StatusNoContent)
}

func (d *dash) handleMemory(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	selectedGroup := r.URL.Query().Get("group")

	pageTop(w, "Memory")
	fmt.Fprint(w, `<p class="dim">Browse per-group MEMORY.md, CLAUDE.md, diary, episodes, users, facts.</p>`)

	rows, err := d.db.Query(`SELECT folder FROM groups ORDER BY folder LIMIT 500`)
	if err != nil {
		slog.Warn("memory: groups query", "err", err)
		fmt.Fprintf(w, `<div class="banner-err">groups query error: %s</div>`,
			esc(err.Error()))
	} else {
		defer rows.Close()
		fmt.Fprint(w, `<form method="get" style="max-width:420px">
<label class="dim" for="group">Group</label>
<select id="group" name="group" onchange="this.form.submit()">
<option value="">— select a group —</option>`)
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
				esc(folder),
				sel,
				esc(folder),
			)
		}
		if err := rows.Err(); err != nil {
			slog.Warn("memory: rows", "err", err)
		}
		fmt.Fprint(w, `</select></form>`)
	}

	if selectedGroup != "" {
		d.renderMemorySection(w, selectedGroup)
	} else if err == nil {
		fmt.Fprint(w, `<p class="empty">Select a group above to view its memory.</p>`)
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
	// Resolve symlinks to block escapes; ErrNotExist is fine (group dir may not exist yet).
	if real, err := filepath.EvalSymlinks(groupDir); err == nil {
		if real != d.groupsDir && !strings.HasPrefix(real, d.groupsDir+string(filepath.Separator)) {
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
				esc(leaf))
			return
		}
		if os.IsNotExist(err) && showMissing {
			fmt.Fprintf(w, `<p><em>%s not found</em></p>`, esc(leaf))
		}
		return
	}
	data, truncated, err := readCapped(path)
	if err != nil {
		slog.Warn("memory: read file", "err", err, "path", path)
		if showMissing {
			fmt.Fprintf(w, `<p><em>%s read error</em></p>`, esc(leaf))
		}
		return
	}
	mtime := ""
	if info, err := os.Stat(path); err == nil {
		mtime = info.ModTime().Format("2006-01-02 15:04")
	}
	truncNote := ""
	if truncated {
		truncNote = fmt.Sprintf(" (truncated at %d bytes)", maxFileBytes)
	}
	if leaf == "MEMORY.md" {
		fmt.Fprintf(w, `<p class="dim">%d bytes &middot; modified %s%s</p><pre>%s</pre>`,
			len(data), mtime, truncNote, esc(string(data)))
	} else {
		fmt.Fprintf(w, `<details><summary>%s%s</summary><pre>%s</pre></details>`,
			esc(leaf), truncNote, esc(string(data)))
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
		esc(title), openAttr, esc(summaryLabel))
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
			esc(e.Name()),
			esc(summary),
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

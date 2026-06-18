package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kronael/arizuko/audit"
)

// handleTaskDetail renders GET /dash/tasks/{id} — full task fields + last 20 run logs.
func (d *dash) handleTaskDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	if _, ok := requireUser(w, r); !ok {
		return
	}

	var owner, chatJID, prompt, status, createdAt string
	var cron, nextRun, contextMode sql.NullString
	err := d.adminDB().QueryRow(
		`SELECT owner, chat_jid, COALESCE(prompt,''), cron, next_run, status, created_at,
		        context_mode
		 FROM scheduled_tasks WHERE id = ?`, id,
	).Scan(&owner, &chatJID, &prompt, &cron, &nextRun, &status, &createdAt, &contextMode)
	if err == sql.ErrNoRows {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		slog.Warn("task detail: query", "id", id, "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	// Gate on the task's owning folder: a non-operator must not read another
	// tenant's task prompt / run history.
	allowed, operator := d.callerScope(r)
	if !visible(allowed, operator, owner) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageTopFor(w, r, "Task "+id,
		struct{ Href, Label string }{"/dash/tasks/", "tasks"},
		struct{ Href, Label string }{"", id},
	)

	dot := "ok"
	if status != "active" {
		dot = "warn"
	}

	fmt.Fprintf(w, `<table>%s%s%s%s%s%s%s%s</table>`,
		htmlDetail("ID", `<code>`+esc(id)+`</code>`),
		htmlDetail("Group", esc(owner)),
		htmlDetail("Chat JID", `<code>`+esc(chatJID)+`</code>`),
		htmlDetail("Cron", `<code>`+esc(cron.String)+`</code>`),
		htmlDetail("Status", `<span class="dot `+dot+`"></span>`+esc(status)),
		htmlDetail("Created", `<abbr title="`+esc(createdAt)+`">`+relativeTS(createdAt)+`</abbr>`),
		htmlDetail("Next Run", `<abbr title="`+esc(nextRun.String)+`">`+relativeTS(nextRun.String)+`</abbr>`),
		htmlDetail("Context Mode", esc(contextMode.String)),
	)

	fmt.Fprintf(w, `<h2>Prompt</h2><pre>%s</pre>`, esc(prompt))

	// Actions — POST forms (same-origin CSRF guard via requireAdmin).
	fmt.Fprint(w, `<h2>Actions</h2><div class="form-inline-group">`)
	if status == "active" {
		fmt.Fprintf(w,
			`<form method="post" action="/dash/tasks/%s/pause">`+
				`<button type="submit">pause</button></form>`, esc(id))
	}
	if status == "paused" {
		fmt.Fprintf(w,
			`<form method="post" action="/dash/tasks/%s/resume">`+
				`<button type="submit">resume</button></form>`, esc(id))
	}
	if status != "cancelled" {
		fmt.Fprintf(w,
			`<form method="post" action="/dash/tasks/%s/cancel"`+
				` onsubmit="return confirm('cancel task %s?')">`+
				`<button type="submit">cancel</button></form>`, esc(id), esc(id))
	}
	fmt.Fprint(w, `</div>`)

	// Last 20 run logs.
	fmt.Fprint(w, `<h2>Run history</h2>`)
	rows, err := d.adminDB().Query(
		`SELECT run_at, status, COALESCE(duration_ms,0), COALESCE(error,'')
		 FROM task_run_logs WHERE task_id = ? ORDER BY run_at DESC LIMIT 20`, id)
	if err != nil {
		slog.Warn("task detail: run logs query", "id", id, "err", err)
		fmt.Fprint(w, htmlBanner("err", "run logs error: "+err.Error()))
		pageClose(w, r)
		return
	}
	defer rows.Close()

	var logRows [][]string
	for rows.Next() {
		var runAt, runStatus, errMsg string
		var durMS int
		if err := rows.Scan(&runAt, &runStatus, &durMS, &errMsg); err != nil {
			slog.Warn("task detail: run logs scan", "err", err)
			continue
		}
		logRows = append(logRows, []string{
			`<abbr title="` + esc(runAt) + `">` + relativeTS(runAt) + `</abbr>`,
			esc(runStatus),
			fmt.Sprintf(`%d ms`, durMS),
			esc(errMsg),
		})
	}
	if err := rows.Err(); err != nil {
		slog.Warn("task detail: run logs rows", "err", err)
	}
	fmt.Fprint(w, htmlTable([]string{"Run At", "Status", "Duration", "Error"}, logRows))

	pageClose(w, r)
}

// handleTaskAction handles POST /dash/tasks/{id}/pause|resume|cancel.
func (d *dash) handleTaskAction(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	action := r.PathValue("action")
	if id == "" || action == "" {
		http.NotFound(w, r)
		return
	}
	if _, ok := d.requireAdmin(w, r, "**"); !ok {
		return
	}
	if d.adminDB() == nil {
		http.Error(w, "db unavailable", http.StatusServiceUnavailable)
		return
	}

	var newStatus string
	switch action {
	case "pause":
		newStatus = "paused"
	case "resume":
		newStatus = "active"
	case "cancel":
		newStatus = "cancelled"
	default:
		http.NotFound(w, r)
		return
	}

	res, err := d.adminDB().Exec(
		`UPDATE scheduled_tasks SET status = ? WHERE id = ?`, newStatus, id)
	if err != nil {
		slog.Warn("task action: update", "id", id, "action", action, "err", err)
		http.Error(w, "write failed", http.StatusInternalServerError)
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	audit.Emit(context.Background(), audit.Event{
		Category: audit.CategoryMutation,
		Action:   "task.update",
		Actor:    "rest:dashd",
		Surface:  audit.SurfaceREST,
		Resource: "scheduled_tasks/" + id,
		Outcome:  audit.OutcomeOK,
		ParamsSummary: map[string]any{
			"status": newStatus,
		},
	})
	slog.Info("task action", "id", id, "action", action, "status", newStatus)
	http.Redirect(w, r, "/dash/tasks/"+id, http.StatusSeeOther)
}

// handleTaskCreate handles POST /dash/tasks/ — create a new scheduled task.
func (d *dash) handleTaskCreate(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.requireAdmin(w, r, "**"); !ok {
		return
	}
	if d.adminDB() == nil {
		http.Error(w, "db unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	owner := strings.TrimSpace(r.FormValue("owner"))
	chatJID := strings.TrimSpace(r.FormValue("chat_jid"))
	prompt := strings.TrimSpace(r.FormValue("prompt"))
	cron := strings.TrimSpace(r.FormValue("cron"))
	if owner == "" || chatJID == "" || prompt == "" || cron == "" {
		http.Error(w, "owner, chat_jid, prompt, cron required", http.StatusBadRequest)
		return
	}

	// Generate a short unique ID (timestamp-based like timed does).
	var id string
	if err := d.adminDB().QueryRow(`SELECT lower(hex(randomblob(6)))`).Scan(&id); err != nil {
		slog.Warn("task create: gen id", "err", err)
		http.Error(w, "id gen failed", http.StatusInternalServerError)
		return
	}
	id = "t-" + id

	_, err := d.adminDB().Exec(
		`INSERT INTO scheduled_tasks (id, owner, chat_jid, prompt, cron, status, created_at)
		 VALUES (?, ?, ?, ?, ?, 'active', datetime('now'))`,
		id, owner, chatJID, prompt, cron)
	if err != nil {
		slog.Warn("task create: insert", "err", err)
		http.Error(w, "insert failed", http.StatusInternalServerError)
		return
	}
	audit.Emit(context.Background(), audit.Event{
		Category: audit.CategoryMutation,
		Action:   "task.create",
		Actor:    "rest:dashd",
		Surface:  audit.SurfaceREST,
		Resource: "scheduled_tasks/" + id,
		Folder:   owner,
		Outcome:  audit.OutcomeOK,
		ParamsSummary: map[string]any{
			"cron": cron,
		},
	})
	slog.Info("task created", "id", id, "owner", owner, "cron", cron)
	http.Redirect(w, r, "/dash/tasks/"+id, http.StatusSeeOther)
}

// truncate64 truncates s to ~60 chars for display, appending "…" if cut.
func truncate64(s string) string {
	const limit = 60
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + "…"
}

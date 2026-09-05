package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/core"
	"github.com/robfig/cron/v3"
)

// cronParser mirrors timed's parser (Minute|Hour|Dom|Month|Dow) so dashd
// validates cron expressions and computes next_run identically.
var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// taskTZ resolves the scheduler timezone from TZ env, falling back to UTC —
// same resolution timed uses.
func taskTZ() string {
	tz := os.Getenv("TZ")
	if _, err := time.LoadLocation(tz); tz == "" || err != nil {
		return "UTC"
	}
	return tz
}

// taskNextRun replicates timed's computeNextRun: an integer cronVal is an
// interval in ms (next = now + ms); empty is a one-shot (next = ""); otherwise
// it's a robfig cron expression. ok is false only when a non-empty,
// non-interval expression fails to parse — the caller rejects with 400.
func taskNextRun(cronVal string) (next string, ok bool) {
	if ms, err := strconv.ParseInt(cronVal, 10, 64); err == nil && ms > 0 {
		return time.Now().Add(time.Duration(ms) * time.Millisecond).Format(time.RFC3339), true
	}
	if cronVal == "" {
		return "", true
	}
	loc, err := time.LoadLocation(taskTZ())
	if err != nil {
		loc = time.UTC
	}
	s, err := cronParser.Parse(cronVal)
	if err != nil {
		return "", false
	}
	return s.Next(time.Now().In(loc)).Format(time.RFC3339), true
}

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
	// READ through routd's /v1 (spec 6/28 §"Read-path: /v1 only, no direct DB").
	//
	// This page authorizes AFTER reading, because the check needs the task's
	// owner and the owner is only known once the row is fetched — an ordering it
	// has always had, which a local DB read concealed. It is not a leak: a
	// forbidden caller still never sees the task. What changes is that an
	// unreachable routd fails before the refusal, so a cross-tenant request gets
	// an error rather than a 403. Every migrated page has that same property.
	//
	// The WRITES below still go through the DB: they carry the operator onto the
	// audit row via AsUser and dashd's service bearer does not, so moving them
	// waits on 6/29's identity decision.
	task, found, err := d.getTask(r.Context(), id)
	if err == nil && !found {
		http.NotFound(w, r)
		return
	}
	var owner, chatJID, prompt, status, createdAt string
	var cron, nextRun, contextMode sql.NullString
	if err == nil {
		owner, chatJID, prompt = task.Owner, task.ChatJID, task.Prompt
		status = task.Status
		createdAt = task.Created.UTC().Format(time.RFC3339)
		cron = sql.NullString{String: task.Cron, Valid: task.Cron != ""}
		contextMode = sql.NullString{String: task.ContextMode, Valid: task.ContextMode != ""}
		if task.NextRun != nil {
			nextRun = sql.NullString{String: task.NextRun.UTC().Format(time.RFC3339), Valid: true}
		}
	}
	if err != nil {
		// routd unreachable is a DEPENDENCY outage, not an internal fault: 503
		// says "ask again", 500 says "this is broken", and only one of those is
		// true. It is also what a dashd with no backend has always answered.
		slog.Warn("task detail: read", "id", id, "err", err)
		http.Error(w, "backend unavailable", http.StatusServiceUnavailable)
		return
	}
	// Gate on the task's owning folder: a non-operator must not read another
	// tenant's task prompt / run history.
	allowed, operator := d.callerScope(r)
	if !visible(allowed, operator, owner) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	pageTopFor(w, r, "Task "+id,
		crumb{"/dash/tasks/", "tasks"},
		crumb{"", id},
	)

	dot := "dot-ok"
	if status != "active" {
		dot = "dot-warn"
	}

	fmt.Fprintf(w, `<table>%s%s%s%s%s%s%s%s</table>`,
		htmlDetail("ID", `<code>`+esc(id)+`</code>`),
		htmlDetail("Group", esc(owner)),
		htmlDetail("Chat JID", `<code>`+esc(chatJID)+`</code>`),
		htmlDetail("Cron", `<code>`+esc(cron.String)+`</code>`),
		htmlDetail("Status", `<span class="dot `+dot+`"></span>`+esc(status)),
		htmlDetail("Created", abbrTS(createdAt)),
		htmlDetail("Next Run", abbrTS(nextRun.String)),
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

	fmt.Fprint(w, `<h2>Run history</h2>`)
	runs, err := d.taskRuns(r.Context(), id)
	if err != nil {
		slog.Warn("task detail: run logs", "id", id, "err", err)
		fmt.Fprint(w, htmlBanner("err", "could not reach routd for run history"))
		pageClose(w, r)
		return
	}

	var logRows [][]string
	for _, run := range runs {
		logRows = append(logRows, []string{
			abbrTS(run.RunAt),
			esc(run.Status),
			fmt.Sprintf(`%d ms`, run.DurationMS),
			esc(run.Error),
		})
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

	// State-machine guards live in the WHERE clause: a transition that does not
	// match the current status affects 0 rows → 409. pause: active→paused;
	// resume: paused→active (recompute next_run from cron); cancel: active|paused
	// →cancelled.
	// The write and its audit row commit together (audit.EmitInTx): a crash
	// between the two cannot leave a mutation without its trail.
	tx, err := d.adminDB().BeginTx(r.Context(), nil)
	if err != nil {
		slog.Warn("task action: begin", "id", id, "action", action, "err", err)
		http.Error(w, "write failed", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()
	var newStatus string
	var res sql.Result
	switch action {
	case "pause":
		newStatus = "paused"
		res, err = tx.Exec(
			`UPDATE scheduled_tasks SET status = 'paused' WHERE id = ? AND status = 'active'`, id)
	case "resume":
		newStatus = "active"
		var cronVal sql.NullString
		if qerr := tx.QueryRow(
			`SELECT cron FROM scheduled_tasks WHERE id = ?`, id).Scan(&cronVal); qerr != nil {
			if qerr == sql.ErrNoRows {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			slog.Warn("task action: load cron", "id", id, "err", qerr)
			http.Error(w, "write failed", http.StatusInternalServerError)
			return
		}
		nextRun, ok := taskNextRun(cronVal.String)
		if !ok {
			http.Error(w, "invalid cron expression", http.StatusBadRequest)
			return
		}
		res, err = tx.Exec(
			`UPDATE scheduled_tasks SET status = 'active', next_run = ? WHERE id = ? AND status = 'paused'`,
			nextRun, id)
	case "cancel":
		newStatus = "cancelled"
		res, err = tx.Exec(
			`UPDATE scheduled_tasks SET status = 'cancelled' WHERE id = ? AND status IN ('active','paused')`, id)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		slog.Warn("task action: update", "id", id, "action", action, "err", err)
		http.Error(w, "write failed", http.StatusInternalServerError)
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		http.Error(w, "invalid state transition", http.StatusConflict)
		return
	}
	err = audit.EmitInTx(r.Context(), tx, audit.Event{
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
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		slog.Warn("task action: commit", "id", id, "action", action, "err", err)
		http.Error(w, "write failed", http.StatusInternalServerError)
		return
	}
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
	cronVal := strings.TrimSpace(r.FormValue("cron"))
	if owner == "" || chatJID == "" || prompt == "" || cronVal == "" {
		http.Error(w, "owner, chat_jid, prompt, cron required", http.StatusBadRequest)
		return
	}
	// Validate cron with timed's parser and compute next_run so the task fires
	// without waiting for timed to backfill it. Empty cron = one-shot (next "").
	nextRun, ok := taskNextRun(cronVal)
	if !ok {
		http.Error(w, "invalid cron expression", http.StatusBadRequest)
		return
	}

	var id string
	if err := d.adminDB().QueryRow(`SELECT lower(hex(randomblob(6)))`).Scan(&id); err != nil {
		slog.Warn("task create: gen id", "err", err)
		http.Error(w, "id gen failed", http.StatusInternalServerError)
		return
	}
	id = "t-" + id

	// The insert and its audit row commit together (audit.EmitInTx).
	tx, err := d.adminDB().BeginTx(r.Context(), nil)
	if err != nil {
		slog.Warn("task create: begin", "err", err)
		http.Error(w, "insert failed", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()
	_, err = tx.Exec(
		`INSERT INTO scheduled_tasks (id, owner, chat_jid, prompt, cron, next_run, status, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, 'active', datetime('now'))`,
		id, owner, chatJID, prompt, cronVal, nextRun)
	if err == nil {
		err = audit.EmitInTx(r.Context(), tx, audit.Event{
			Category: audit.CategoryMutation,
			Action:   "task.create",
			Actor:    "rest:dashd",
			Surface:  audit.SurfaceREST,
			Resource: "scheduled_tasks/" + id,
			Folder:   owner,
			Outcome:  audit.OutcomeOK,
			ParamsSummary: map[string]any{
				"cron": cronVal,
			},
		})
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		slog.Warn("task create: insert", "err", err)
		http.Error(w, "insert failed", http.StatusInternalServerError)
		return
	}
	slog.Info("task created", "id", id, "owner", owner, "cron", cronVal)
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

// taskRunLog is one row of routd's GET /v1/tasks/{id}/runs.
type taskRunLog struct {
	RunAt      string `json:"run_at"`
	Status     string `json:"status"`
	DurationMS int    `json:"duration_ms"`
	Error      string `json:"error"`
}

// getTask reads one scheduled task from routd's /v1.
//
// found=false with a nil error is a genuine 404; an error means routd is
// unreachable. The caller must not conflate them — "no such task" and "we could
// not ask" look identical to an operator and only one of them means the task is
// gone.
func (d *dash) getTask(ctx context.Context, id string) (core.Task, bool, error) {
	body, status, err := d.bearerCall(ctx, d.routdURL, http.MethodGet,
		"/v1/tasks/"+url.PathEscape(id), nil)
	if err != nil {
		return core.Task{}, false, err
	}
	if status == http.StatusNotFound {
		return core.Task{}, false, nil
	}
	if status < 200 || status >= 300 {
		return core.Task{}, false, fmt.Errorf("routd /v1/tasks/%s: status %d", id, status)
	}
	var t core.Task
	if err := json.Unmarshal(body, &t); err != nil {
		return core.Task{}, false, err
	}
	return t, true, nil
}

// taskRuns reads a task's run history from routd's /v1.
func (d *dash) taskRuns(ctx context.Context, id string) ([]taskRunLog, error) {
	body, status, err := d.bearerCall(ctx, d.routdURL, http.MethodGet,
		"/v1/tasks/"+url.PathEscape(id)+"/runs", nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("routd /v1/tasks/%s/runs: status %d", id, status)
	}
	var runs []taskRunLog
	if err := json.Unmarshal(body, &runs); err != nil {
		return nil, err
	}
	return runs, nil
}

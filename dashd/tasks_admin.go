package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/dashd/dashstore"
)

// taskNextRun renders the next_run dashd stores for a cron an operator typed.
// ok is false only when a non-empty value will not parse — the caller rejects
// that with 400, so a bad expression never reaches a row.
func taskNextRun(cronVal string) (next string, ok bool) {
	t, err := core.NextRun(cronVal, core.Timezone())
	if errors.Is(err, core.ErrNoSchedule) {
		return "", true
	}
	if err != nil {
		return "", false
	}
	return t.Format(time.RFC3339), true
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
	var cronSpec, nextRun, contextMode string
	if err == nil {
		owner, chatJID, prompt = task.Owner, task.ChatJID, task.Prompt
		status = task.Status
		createdAt = task.Created.UTC().Format(time.RFC3339)
		cronSpec, contextMode = task.Cron, task.ContextMode
		if task.NextRun != nil {
			nextRun = task.NextRun.UTC().Format(time.RFC3339)
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

	_, _ = fmt.Fprintf(w, `<table>%s%s%s%s%s%s%s%s</table>`,
		htmlDetail("ID", `<code>`+esc(id)+`</code>`),
		htmlDetail("Group", esc(owner)),
		htmlDetail("Chat JID", `<code>`+esc(chatJID)+`</code>`),
		htmlDetail("Cron", `<code>`+esc(cronSpec)+`</code>`),
		htmlDetail("Status", `<span class="dot `+dot+`"></span>`+esc(status)),
		htmlDetail("Created", abbrTS(createdAt)),
		htmlDetail("Next Run", abbrTS(nextRun)),
		htmlDetail("Context Mode", esc(contextMode)),
	)

	_, _ = fmt.Fprintf(w, `<h2>Prompt</h2><pre>%s</pre>`, esc(prompt))

	// Actions — POST forms (same-origin CSRF guard via requireAdmin).
	_, _ = fmt.Fprint(w, `<h2>Actions</h2><div class="form-inline-group">`)
	if status == "active" {
		_, _ = fmt.Fprintf(w,
			`<form method="post" action="/dash/tasks/%s/pause">`+
				`<button type="submit">pause</button></form>`, esc(id))
	}
	if status == "paused" {
		_, _ = fmt.Fprintf(w,
			`<form method="post" action="/dash/tasks/%s/resume">`+
				`<button type="submit">resume</button></form>`, esc(id))
	}
	if status != "cancelled" {
		_, _ = fmt.Fprintf(w,
			`<form method="post" action="/dash/tasks/%s/cancel"`+
				` onsubmit="return confirm('cancel task %s?')">`+
				`<button type="submit">cancel</button></form>`, esc(id), esc(id))
	}
	_, _ = fmt.Fprint(w, `</div>`)

	_, _ = fmt.Fprint(w, `<h2>Run history</h2>`)
	runs, err := d.taskRuns(r.Context(), id)
	if err != nil {
		slog.Warn("task detail: run logs", "id", id, "err", err)
		_, _ = fmt.Fprint(w, htmlBanner("err", "could not reach routd for run history"))
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
	_, _ = fmt.Fprint(w, htmlTable([]string{"Run At", "Status", "Duration", "Error"}, logRows))

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
	if d.routdStore() == nil {
		http.Error(w, "db unavailable", http.StatusServiceUnavailable)
		return
	}

	// pause: active→paused; resume: paused→active (recompute next_run from
	// cron); cancel: active|paused→cancelled. A task not in the state a
	// transition starts from moves nowhere and answers 409. The status the
	// audit row records and the move that writes it come off the same case, so
	// a fourth action cannot be half-added.
	st := d.ds()
	var newStatus string
	var move func(audit.Event) error
	switch action {
	case "pause":
		newStatus = "paused"
		move = func(ev audit.Event) error { return st.PauseTask(r.Context(), id, ev) }
	case "resume":
		newStatus = "active"
		move = func(ev audit.Event) error { return st.ResumeTask(r.Context(), id, taskNextRun, ev) }
	case "cancel":
		newStatus = "cancelled"
		move = func(ev audit.Event) error { return st.CancelTask(r.Context(), id, ev) }
	default:
		http.NotFound(w, r)
		return
	}
	err := move(audit.Event{
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
	switch {
	case errors.Is(err, dashstore.ErrTaskMissing):
		http.Error(w, "not found", http.StatusNotFound)
		return
	case errors.Is(err, dashstore.ErrInvalidCron):
		http.Error(w, "invalid cron expression", http.StatusBadRequest)
		return
	case errors.Is(err, dashstore.ErrTaskState):
		http.Error(w, "invalid state transition", http.StatusConflict)
		return
	case err != nil:
		slog.Warn("task action", "id", id, "action", action, "err", err)
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
	if d.routdStore() == nil {
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

	st := d.ds()
	id, err := st.NewTaskID()
	if err != nil {
		slog.Warn("task create: gen id", "err", err)
		http.Error(w, "id gen failed", http.StatusInternalServerError)
		return
	}

	if err := st.CreateTask(r.Context(), dashstore.NewTask{
		ID:      id,
		Owner:   owner,
		ChatJID: chatJID,
		Prompt:  prompt,
		Cron:    cronVal,
		NextRun: nextRun,
	}, audit.Event{
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
	}); err != nil {
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

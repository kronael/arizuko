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

func (d *dash) handleTaskDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	if _, ok := requireUser(w, r); !ok {
		return
	}
	task, found, err := d.getTask(r.Context(), id)
	if err != nil {
		slog.Warn("task detail: read", "id", id, "err", err)
		http.Error(w, "backend unavailable", http.StatusServiceUnavailable)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	var nextRun string
	if task.NextRun != nil {
		nextRun = task.NextRun.UTC().Format(time.RFC3339)
	}
	allowed, _ := d.callerScope(r)
	if !visible(allowed, task.Owner) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	pageTopFor(w, r, "Task "+id,
		crumb{"/dash/tasks/", "tasks"},
		crumb{"", id},
	)

	dot := "dot-ok"
	if task.Status != "active" {
		dot = "dot-warn"
	}

	_, _ = fmt.Fprintf(w, `<table>%s%s%s%s%s%s%s%s</table>`,
		htmlDetail("ID", `<code>`+esc(id)+`</code>`),
		htmlDetail("Group", esc(task.Owner)),
		htmlDetail("Chat JID", `<code>`+esc(task.ChatJID)+`</code>`),
		htmlDetail("Cron", `<code>`+esc(task.Cron)+`</code>`),
		htmlDetail("Status", `<span class="dot `+dot+`"></span>`+esc(task.Status)),
		htmlDetail("Created", abbrTS(task.Created.UTC().Format(time.RFC3339))),
		htmlDetail("Next Run", abbrTS(nextRun)),
		htmlDetail("Context Mode", esc(task.ContextMode)),
	)

	_, _ = fmt.Fprintf(w, `<h2>Prompt</h2><pre>%s</pre>`, esc(task.Prompt))

	_, _ = fmt.Fprint(w, `<h2>Actions</h2><div class="form-inline-group">`)
	if task.Status == "active" {
		_, _ = fmt.Fprintf(w,
			`<form method="post" action="/dash/tasks/%s/pause">`+
				`<button type="submit">pause</button></form>`, esc(id))
	}
	if task.Status == "paused" {
		_, _ = fmt.Fprintf(w,
			`<form method="post" action="/dash/tasks/%s/resume">`+
				`<button type="submit">resume</button></form>`, esc(id))
	}
	if task.Status != "cancelled" {
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

	st := d.ds()
	var newStatus string
	var move func(audit.Event) error
	switch action {
	case "pause":
		newStatus = "paused"
		move = func(ev audit.Event) error { return st.PauseTask(r.Context(), id, ev) }
	case "resume":
		newStatus = "active"
		move = func(ev audit.Event) error { return st.ResumeTask(r.Context(), id, ev) }
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
	if _, err := core.NextRun(cronVal, core.Timezone()); err != nil {
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

func truncate64(s string) string {
	const limit = 60
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + "…"
}

type taskRunLog struct {
	RunAt      string `json:"run_at"`
	Status     string `json:"status"`
	DurationMS int    `json:"duration_ms"`
	Error      string `json:"error"`
}

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

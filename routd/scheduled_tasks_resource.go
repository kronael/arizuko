package routd

// scheduled_tasks_resource.go is the spec 5/44 step after web_routes +
// network_rules: the agent's task tools (schedule_task/pause_task/resume_task/
// cancel_task/list_tasks) ride ONE resreg.Resource instead of five hand-rolled
// ipc/ipc.go tool bodies.
//
// resreg owns the plumbing (handler dispatch + one tx wrapping each mutation AND
// its audit_log row); routd owns the auth POLICY. Two shapes are folded in:
//
//   - Custom actions: schedule/pause/resume/cancel are resource-specific verbs
//     (not the CRUD ActionCreate/Delete), so Mutates() is true and each opens
//     the mutation+audit tx; list reuses resreg.ActionList (read-only, no tx).
//     MCPNames maps every action back to the flat live tool name so no
//     in-container tool is renamed.
//   - PER-TASK-ID structural authz. Unlike web_routes (folder owns its own row)
//     and network_rules (target folder is a plain arg), pause/resume/cancel take
//     a task ID and MUST resolve the task's OWNER folder before the tier gate can
//     rule on it. The injected Gate does the tool-level grant (CheckAction +
//     db.Authorize); the HANDLER does the target-level auth.AuthorizeStructural
//     on the RESOLVED owner (GetTask(id).Owner for pause/resume/cancel,
//     DefaultFolderForJID(jid) for schedule) — mirroring the deleted ipc bodies
//     and inspect_tasks (inspect.go). It lives in the handler, not the Gate,
//     because that owner resolution is a DB read the handler already performs;
//     splitting it into the Gate would duplicate the read AND reorder the
//     "target group not registered" / "task not found" validation ahead of the
//     tier decision, diverging from the ipc bodies. The cap still runs before any
//     store write and rolls the tx back on denial, so an operator ACL grant can
//     open the TOOL but never widen the tier/containment cap.
//
// The operator REST face (/v1/tasks, tasks_http.go) is NOT mounted from this
// resource (agent-only, like the pilots); its tasks:read/write scope model is a
// separate 5/44 step. Endpoints here exist only to drive deriveMCPTools.

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/robfig/cron/v3"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	grantslib "github.com/kronael/arizuko/grants"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/store"
)

// Task actions are resource-specific verbs, not the CRUD shape. list reuses
// resreg.ActionList (== "list") so it stays read-only (Mutates() false, no tx);
// schedule/pause/resume/cancel are custom strings so Mutates() is true and each
// opens the mutation+audit tx.
const (
	tasksActionSchedule = resreg.Action("schedule")
	tasksActionPause    = resreg.Action("pause")
	tasksActionResume   = resreg.Action("resume")
	tasksActionCancel   = resreg.Action("cancel")
)

// tasksMCPNames maps each resreg action to the flat tool name the live agent
// already calls; keeping them avoids renaming an in-container tool. It is also
// the Gate's action→policy-name lookup and the handler's pause/resume/cancel
// policy-name source.
var tasksMCPNames = map[resreg.Action]string{
	tasksActionSchedule: "schedule_task",
	tasksActionPause:    "pause_task",
	tasksActionResume:   "resume_task",
	tasksActionCancel:   "cancel_task",
	resreg.ActionList:   "list_tasks",
}

// scheduledTasksResource is the single renderer for the agent's five task tools.
// Endpoints exist only to drive deriveMCPTools (Action ∩ MCPDoc) — the REST face
// (/v1/tasks) is NOT mounted from this resource (see file header). Store is a
// store.Store over routd.db so resreg.invoke opens the mutation+audit tx there.
func (s *Server) scheduledTasksResource() resreg.Resource {
	return resreg.Resource{
		Name: "scheduled_tasks",
		Endpoints: []resreg.Endpoint{
			{Verb: "POST", Path: "/v1/scheduled_tasks", Action: tasksActionSchedule},
			{Verb: "POST", Path: "/v1/scheduled_tasks/pause", Action: tasksActionPause},
			{Verb: "POST", Path: "/v1/scheduled_tasks/resume", Action: tasksActionResume},
			{Verb: "DELETE", Path: "/v1/scheduled_tasks", Action: tasksActionCancel},
			{Verb: "GET", Path: "/v1/scheduled_tasks", Action: resreg.ActionList},
		},
		MCPDoc: map[resreg.Action]string{
			tasksActionSchedule: "Create a scheduled prompt that fires against a target chat. Use when the user asks for reminders, recurring checks, or deferred work. `cron` accepts a 5-field cron expression, an integer millisecond interval, or an RFC3339 timestamp for a one-shot. Not for immediate execution (`send`/inject_message).",
			tasksActionPause:    "Mark a scheduled task paused so it stops firing but is preserved. Use when suspending a task temporarily. Not for permanent removal (cancel_task).",
			tasksActionResume:   "Re-activate a paused task so it resumes firing on its schedule. Use to undo pause_task. No effect on already-active or cancelled tasks.",
			tasksActionCancel:   "Permanently delete a scheduled task. Use when the task is no longer wanted. Not for temporary suspension (pause_task) — this cannot be undone.",
			resreg.ActionList:   "Return scheduled tasks visible to this group. Use for a plain task dump; prefer inspect_tasks when you also want task_run_logs or per-task history.",
		},
		MCPArgs: map[resreg.Action][]resreg.MCPArg{
			tasksActionSchedule: {
				{Name: "targetJid", Type: "string", Required: true},
				{Name: "prompt", Type: "string", Required: true},
				{Name: "cron", Type: "string"},
				{Name: "contextMode", Type: "string"},
			},
			tasksActionPause:  {{Name: "taskId", Type: "string", Required: true}},
			tasksActionResume: {{Name: "taskId", Type: "string", Required: true}},
			tasksActionCancel: {{Name: "taskId", Type: "string", Required: true}},
		},
		MCPNames: tasksMCPNames,
		Authz:    func(resreg.Caller, resreg.Action, resreg.Args) (string, map[string]string, error) { return "", nil, nil },
		Handler:  s.scheduledTasksHandler,
		Store:    store.New(s.db.SQL()),
	}
}

// scheduledTasksHandler runs schedule/pause/resume/cancel/list against routd.db,
// folding in the bespoke semantics the deleted ipc bodies enforced: cron/interval/
// one-shot parsing, active-task dedup, DefaultFolderForJID target resolution,
// contextMode normalization, and the PER-TASK-ID structural authz (see header).
func (s *Server) scheduledTasksHandler(ctx context.Context, x resreg.Execution) (any, error) {
	id := auth.Resolve(x.Caller.Folder)
	switch x.Action {
	case resreg.ActionList:
		// Root (tier 0) sees every task (owner filter empty); a child sees only
		// its own — exactly the deleted list_tasks body.
		return s.db.Tasks(x.Caller.Folder, id.Tier == 0), nil

	case tasksActionSchedule:
		targetJid := argString(x.Args, "targetJid")
		contextMode := argString(x.Args, "contextMode")
		if contextMode != "isolated" {
			contextMode = "group"
		}
		targetFolder := s.db.DefaultFolderForJID(targetJid)
		if targetFolder == "" {
			return nil, resreg.Errorf(http.StatusBadRequest, "target group not registered")
		}
		if err := auth.AuthorizeStructural(id, "schedule_task", auth.AuthzTarget{TaskOwner: targetFolder}); err != nil {
			return nil, resreg.Errorf(http.StatusForbidden, "%v", err)
		}
		nextRun, cronStore, err := parseTaskSchedule(argString(x.Args, "cron"))
		if err != nil {
			return nil, resreg.Errorf(http.StatusBadRequest, "%v", err)
		}
		prompt := argString(x.Args, "prompt")
		// Dedup: an active task with the same cron + prompt for the target folder
		// is returned as-is instead of creating a duplicate. Read on s.db before
		// the tx write (no lock contention) — the deleted ipc body's guard.
		if cronStore != "" {
			for _, t := range s.db.Tasks(targetFolder, false) {
				if t.Status == core.TaskActive && t.Cron == cronStore && t.Prompt == prompt {
					return map[string]any{"taskId": t.ID}, nil
				}
			}
		}
		taskID := fmt.Sprintf("task-%d-%s", time.Now().UnixMilli(), uuid.New().String()[:8])
		if err := insertTaskTx(ctx, x.Tx, core.Task{
			ID: taskID, Owner: targetFolder, ChatJID: targetJid,
			Prompt: prompt, Cron: cronStore, NextRun: nextRun,
			Status: core.TaskActive, Created: time.Now(), ContextMode: contextMode,
		}); err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		return map[string]any{"taskId": taskID}, nil

	case tasksActionPause, tasksActionResume, tasksActionCancel:
		taskID := argString(x.Args, "taskId")
		task, ok := s.db.GetTask(taskID)
		if !ok {
			return nil, resreg.Errorf(http.StatusNotFound, "task not found")
		}
		// PER-TASK-ID cap: resolve the task's owner and require the caller's tier
		// contain it (tier 0 any, tier 1 own world, tier 2 own folder, tier 3
		// none) — a folder must not pause/resume/cancel another folder's task.
		// Mirrors the deleted ipc body + inspect_tasks.
		name := tasksMCPNames[x.Action]
		if err := auth.AuthorizeStructural(id, name, auth.AuthzTarget{TaskOwner: task.Owner}); err != nil {
			return nil, resreg.Errorf(http.StatusForbidden, "%v", err)
		}
		var err error
		switch x.Action {
		case tasksActionPause:
			err = setTaskStatusTx(ctx, x.Tx, taskID, core.TaskPaused)
		case tasksActionResume:
			err = setTaskStatusTx(ctx, x.Tx, taskID, core.TaskActive)
		case tasksActionCancel:
			err = deleteTaskTx(ctx, x.Tx, taskID)
		}
		if err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		return map[string]any{"ok": true}, nil
	}
	return nil, resreg.Errorf(http.StatusBadRequest, "unknown action %q", x.Action)
}

// scheduledTasksPostBuild returns the ServeMCP seam that mounts the task tools on
// the agent socket, with the tier-aware Gate + MatchingRules visibility for this
// folder's grant rules injected. The Gate does the TOOL-level grant (CheckAction
// + db.Authorize); the per-task/target structural cap lives in the handler (see
// header). Only rules the socket already carries can widen visibility, so a
// denied tier still sees nothing new.
func (s *Server) scheduledTasksPostBuild(folder, callerSub string, rules []string) func(*mcpserver.MCPServer) {
	res := s.scheduledTasksResource()
	res.Gate = func(x resreg.Execution, _ string, _ map[string]string) error {
		name := tasksMCPNames[x.Action]
		if !grantslib.CheckAction(rules, name, nil) {
			return resreg.Errorf(http.StatusForbidden, "%s: not permitted", name)
		}
		if callerSub != "" && !s.db.Authorize(callerSub, folder, "mcp:"+name, nil) {
			return resreg.Errorf(http.StatusForbidden, "%s: not permitted", name)
		}
		return nil
	}
	callerFor := func(context.Context, mcp.CallToolRequest) (resreg.Caller, error) {
		return resreg.Caller{Sub: callerSub, Folder: folder}, nil
	}
	visible := func(name string) bool { return len(grantslib.MatchingRules(rules, name)) > 0 }
	return func(srv *mcpserver.MCPServer) {
		resreg.MCPTools(srv, res, callerFor, visible)
	}
}

// parseTaskSchedule reads the `cron` arg into (next_run, stored-cron) exactly as
// the deleted schedule_task body: a positive integer is a millisecond interval
// (stored verbatim), an RFC3339 timestamp is a one-shot (empty stored cron →
// timed marks it completed after one firing), else a 5-field cron expression.
// An empty arg leaves next_run nil (no schedule).
func parseTaskSchedule(cronExpr string) (*time.Time, string, error) {
	if cronExpr == "" {
		return nil, "", nil
	}
	if ms, err := strconv.ParseInt(cronExpr, 10, 64); err == nil && ms > 0 {
		t := time.Now().Add(time.Duration(ms) * time.Millisecond)
		return &t, cronExpr, nil
	}
	if t, err := time.Parse(time.RFC3339, cronExpr); err == nil {
		return &t, "", nil
	}
	p := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	sched, err := p.Parse(cronExpr)
	if err != nil {
		return nil, "", fmt.Errorf("invalid cron %q: %v", cronExpr, err)
	}
	t := sched.Next(time.Now())
	return &t, cronExpr, nil
}

// insertTaskTx inserts one scheduled_tasks row on tx (mirrors store.PutTaskRow so
// the mutation lands in resreg.invoke's tx alongside its audit_log row). An empty
// cron/next_run stores NULL, matching PutTaskRow.
func insertTaskTx(ctx context.Context, tx *sql.Tx, t core.Task) error {
	var nextRun *string
	if t.NextRun != nil {
		v := t.NextRun.Format(time.RFC3339)
		nextRun = &v
	}
	cm := t.ContextMode
	if cm == "" {
		cm = "group"
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO scheduled_tasks
		 (id, owner, chat_jid, prompt, cron, next_run, status, created_at, context_mode)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.Owner, t.ChatJID, t.Prompt, nilIfEmptyStr(t.Cron),
		nextRun, t.Status, t.Created.Format(time.RFC3339), cm)
	return err
}

// setTaskStatusTx flips one task's status on tx (mirrors store.SetTaskStatus).
func setTaskStatusTx(ctx context.Context, tx *sql.Tx, id, status string) error {
	_, err := tx.ExecContext(ctx, `UPDATE scheduled_tasks SET status = ? WHERE id = ?`, status, id)
	return err
}

// deleteTaskTx removes one task on tx (mirrors store.RemoveTask).
func deleteTaskTx(ctx context.Context, tx *sql.Tx, id string) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM scheduled_tasks WHERE id = ?`, id)
	return err
}

// nilIfEmptyStr returns nil for an empty string (SQL NULL) else the string,
// matching store.nilIfEmpty for the nullable cron column.
func nilIfEmptyStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

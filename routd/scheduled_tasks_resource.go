package routd

// scheduled_tasks_resource.go folds the agent's task tools onto ONE
// resreg.Resource (spec 5/16). PER-TASK-ID containment: pause/resume/cancel
// resolve the task's OWNER folder and the HANDLER runs the contain seam on
// it, not the Gate — the owner resolution is a read the handler already
// performs. Both faces read ONE resources.ScheduledTasksEndpoints, mounted
// verbatim (BUGS F21); the fire-loop endpoints stay hand-rolled in
// tasks_http.go: timed's control plane, not resource CRUD.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
	"uuid"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
)

// list reuses resreg.ActionList so it stays read-only (no tx); the rest are
// custom strings so Mutates() is true and each sends the mutation+audit batch.
const (
	tasksActionSchedule = resreg.Action("schedule")
	tasksActionPause    = resreg.Action("pause")
	tasksActionResume   = resreg.Action("resume")
	tasksActionCancel   = resreg.Action("cancel")
	// tasksActionPatch is the REST-only PATCH verb: {status} pause/resume +
	// {next_run} run-now. No MCPDoc entry, so it is never an agent tool.
	tasksActionPatch = resreg.Action("patch")
)

// tasksMCPNames is the action→flat-tool-name map, single-sourced in
// resreg/resources; the REST-only `patch` verb has no entry.
var tasksMCPNames = resources.ScheduledTasksMCPNames

// scheduledTasksResource is the single renderer for BOTH faces; contain is the
// per-face target-containment seam.
func (s *Server) scheduledTasksResource(contain containFn, elevated bool) resreg.Resource {
	r := resreg.Mount(resources.ScheduledTasksName)
	r.Store = s.db.store()
	r.Handler = func(ctx context.Context, x resreg.Execution) (any, error) {
		return s.scheduledTasksHandler(ctx, x, contain, elevated)
	}
	return r
}

// scheduledTasksHandler runs schedule/pause/resume/cancel/list: cron/interval/
// one-shot parsing, active-task dedup, DefaultFolderForJID target resolution.
// Per-task/target containment is the injected contain seam.
func (s *Server) scheduledTasksHandler(ctx context.Context, x resreg.Execution, contain containFn, elevated bool) (any, error) {
	switch x.Action {
	case resreg.ActionList:
		// The list-all key differs by surface. Agent: a root caller (the /root
		// elevation or the "" sentinel) sees every task. REST: only a
		// root/service token (empty jwt_folder claim) sees all; a folder-SCOPED
		// token, a top-level one included, lists ONLY its own. The REST caller is
		// detected by the jwt_folder claim it always sets; the agent keeps the
		// turn's EXPLICIT elevation, since a re-resolve of the caller folder
		// under /root from a deep folder read non-root.
		all := elevated || x.Caller.Folder == ""
		if _, rest := x.Caller.Claims["jwt_folder"]; rest {
			all = x.Caller.Folder == ""
		}
		tasks := s.db.Tasks(x.Caller.Folder, all)
		// REST ?status= filter (inert for MCP: list_tasks declares no status arg).
		if st := argString(x.Args, "status"); st != "" {
			kept := tasks[:0]
			for _, t := range tasks {
				if t.Status == st {
					kept = append(kept, t)
				}
			}
			tasks = kept
		}
		return tasks, nil

	case tasksActionSchedule:
		targetJid := argString(x.Args, "targetJid")
		contextMode := argString(x.Args, "contextMode")
		if contextMode != "isolated" {
			contextMode = "group"
		}
		var targetFolder string
		if strings.Contains(targetJid, ":") {
			targetFolder = s.db.DefaultFolderForJID(targetJid)
		} else if s.db.GroupExists(targetJid) {
			targetFolder = targetJid
		}
		if targetFolder == "" {
			return nil, resreg.Errorf(http.StatusBadRequest, "target group not registered")
		}
		if err := contain(x.Caller, tasksActionSchedule, targetFolder); err != nil {
			return nil, err
		}
		nextRun, cronStore, err := parseTaskSchedule(argString(x.Args, "cron"))
		if err != nil {
			return nil, resreg.Errorf(http.StatusBadRequest, "%v", err)
		}
		prompt := argString(x.Args, "prompt")
		// Dedup: an active task with the same cron + prompt is returned as-is.
		if cronStore != "" {
			for _, t := range s.db.Tasks(targetFolder, false) {
				if t.Status == core.TaskActive && t.Cron == cronStore && t.Prompt == prompt {
					return map[string]any{"taskId": t.ID}, nil
				}
			}
		}
		taskID := fmt.Sprintf("task-%d-%s", time.Now().UnixMilli(), uuid.New().String()[:8])
		if err := s.db.store().CreateTaskInto(x.Writes, core.Task{
			ID: taskID, Owner: targetFolder, ChatJID: targetJid,
			Prompt: prompt, Cron: cronStore, NextRun: nextRun,
			Status: core.TaskActive, Created: time.Now(), ContextMode: contextMode,
		}, resreg.Errorf(http.StatusConflict, "scheduled task %q already exists", taskID)); err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		return map[string]any{"taskId": taskID}, nil

	case tasksActionPause, tasksActionResume, tasksActionCancel:
		taskID := argString(x.Args, "taskId")
		task, ok := s.db.GetTask(taskID)
		if !ok {
			return nil, resreg.Errorf(http.StatusNotFound, "task not found")
		}
		// PER-TASK-ID cap: the caller must contain the task's owner.
		if err := contain(x.Caller, x.Action, task.Owner); err != nil {
			return nil, err
		}
		// A status flip is an Update, so a task removed between the read above
		// and the batch refuses it — the 404 the read would have given.
		gone := resreg.Errorf(http.StatusNotFound, "task not found")
		var err error
		switch x.Action {
		case tasksActionPause:
			err = s.db.store().SetTaskStatusInto(x.Writes, taskID, core.TaskPaused, gone)
		case tasksActionResume:
			err = s.db.store().SetTaskStatusInto(x.Writes, taskID, core.TaskActive, gone)
		case tasksActionCancel:
			err = s.db.store().DeleteTaskInto(x.Writes, taskID)
		}
		if err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		return map[string]any{"ok": true}, nil

	case resreg.ActionGet:
		// Get-one (REST only): the Gate's ownsFolder on the JWT folder is a
		// no-op for a per-task op, so the real cap is contain() on the task's
		// OWNER — a tenant must not read a task outside its subtree by guessing
		// its ID. A root/operator token still reads any task.
		task, ok := s.db.GetTask(argString(x.Args, "taskId"))
		if !ok {
			return nil, resreg.Errorf(http.StatusNotFound, "task not found")
		}
		if err := contain(x.Caller, resreg.ActionGet, task.Owner); err != nil {
			return nil, err
		}
		return task, nil

	case tasksActionPatch:
		// PATCH (REST only): {status} pause/resume + {next_run} run-now, one or
		// both; the same contain seam as pause/resume/cancel.
		taskID := argString(x.Args, "taskId")
		task, ok := s.db.GetTask(taskID)
		if !ok {
			return nil, resreg.Errorf(http.StatusNotFound, "task not found")
		}
		if err := contain(x.Caller, tasksActionPatch, task.Owner); err != nil {
			return nil, err
		}
		status := argString(x.Args, "status")
		nextRun := argString(x.Args, "next_run")
		if status == "" && nextRun == "" {
			return nil, resreg.Errorf(http.StatusBadRequest, "status or next_run required")
		}
		if err := s.db.store().PatchTaskInto(x.Writes, taskID, status, nextRun,
			resreg.Errorf(http.StatusNotFound, "task not found")); err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		// Echo the updated task: the batch has not been sent, so GetTask would
		// read the pre-patch row.
		if status != "" {
			task.Status = status
		}
		if nextRun != "" {
			if t, perr := time.Parse(time.RFC3339, nextRun); perr == nil {
				task.NextRun = &t
			}
		}
		return task, nil
	}
	return nil, resreg.Errorf(http.StatusBadRequest, "unknown action %q", x.Action)
}

// scheduledTasksPostBuild mounts the task tools on the agent socket. The Gate
// is a no-op; the per-task/target cap lives in the handler.
func (s *Server) scheduledTasksPostBuild(folder, callerSub string, authorize authorizeFn, visible func(string) bool, callerID auth.Identity) func(*mcpserver.MCPServer) {
	// One evaluator on the resolved task owner; /root elevates via authorize's
	// allow-all.
	contain := func(_ resreg.Caller, a resreg.Action, target string) error {
		name := tasksMCPNames[a]
		if !authorize(target, "mcp:"+name, nil) {
			return resreg.Errorf(http.StatusForbidden, "%s on %s: not permitted", name, target)
		}
		return nil
	}
	res := s.scheduledTasksResource(contain, callerID.IsRoot)
	res.Gate = agentAllowGate
	return mountAgentResource(res, callerSub, folder, visible)
}

// parseTaskSchedule reads the `cron` arg into (next_run, stored-cron): an
// RFC3339 timestamp is a one-shot (empty stored cron → timed marks it completed
// after one firing); anything else is core.NextRun's value space, stored
// verbatim. An empty arg leaves next_run nil.
func parseTaskSchedule(cronExpr string) (*time.Time, string, error) {
	if cronExpr == "" {
		return nil, "", nil
	}
	if t, err := time.Parse(time.RFC3339, cronExpr); err == nil {
		return &t, "", nil
	}
	t, err := core.NextRun(cronExpr, core.Timezone())
	if err != nil {
		return nil, "", fmt.Errorf("invalid cron %q: %v", cronExpr, err)
	}
	return &t, cronExpr, nil
}

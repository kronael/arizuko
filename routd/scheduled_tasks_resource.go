package routd

// scheduled_tasks_resource.go folds the agent's task tools onto ONE
// resreg.Resource (spec 5/16). PER-TASK-ID containment: pause/resume/cancel
// resolve the task's OWNER folder and the HANDLER runs the contain seam on
// it, not the Gate — the owner resolution is a read the handler already
// performs. Both faces read ONE resources.ScheduledTasksEndpoints, mounted
// verbatim (BUGS F21); the fire-loop endpoints stay hand-rolled in
// tasks_http.go: scheduler control plane, not resource CRUD.

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
	"github.com/kronael/arizuko/store"
)

const (
	tasksActionSchedule = resreg.Action("schedule")
	tasksActionPause    = resreg.Action("pause")
	tasksActionResume   = resreg.Action("resume")
	tasksActionCancel   = resreg.Action("cancel")

	tasksActionPatch = resreg.Action("patch")
)

var tasksMCPNames = resources.ScheduledTasksMCPNames

func (s *Server) scheduledTasksResource(contain containFn, elevated bool) resreg.Resource {
	r := resreg.Mount(resources.ScheduledTasksName)
	r.Store = s.db.store()
	r.Handler = func(ctx context.Context, x resreg.Execution) (any, error) {
		return s.scheduledTasksHandler(ctx, x, contain, elevated)
	}
	return r
}

func (s *Server) scheduledTasksHandler(ctx context.Context, x resreg.Execution, contain containFn, elevated bool) (any, error) {
	switch x.Action {
	case resreg.ActionList:

		all := elevated || x.Caller.Folder == ""
		if _, rest := x.Caller.Claims["jwt_folder"]; rest {
			all = x.Caller.Folder == ""
		}
		tasks, err := s.db.store().ListTasksChecked(x.Caller.Folder, all)
		if err != nil {
			return nil, err
		}

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
		if argString(x.Args, "cron") == "" {
			return nil, resreg.Errorf(http.StatusBadRequest, "cron required: a cron expression, an interval in ms, or an RFC3339 time")
		}
		nextRun, cronStore, err := core.ParseTaskSchedule(argString(x.Args, "cron"))
		if err != nil {
			return nil, resreg.Errorf(http.StatusBadRequest, "%v", err)
		}
		prompt := argString(x.Args, "prompt")

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

		if err := contain(x.Caller, x.Action, task.Owner); err != nil {
			return nil, err
		}

		gone := resreg.Errorf(http.StatusNotFound, "task not found")
		var err error
		switch x.Action {
		case tasksActionPause:
			err = s.db.store().SetTaskStatusInto(x.Writes, taskID, core.TaskPaused, gone)
		case tasksActionResume:
			err = s.db.store().ResumeTaskInto(x.Writes, task, gone)
		case tasksActionCancel:
			err = s.db.store().DeleteTaskInto(x.Writes, taskID)
		}
		if err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		return map[string]any{"ok": true}, nil

	case resreg.ActionGet:

		task, ok := s.db.GetTask(argString(x.Args, "taskId"))
		if !ok {
			return nil, resreg.Errorf(http.StatusNotFound, "task not found")
		}
		if err := contain(x.Caller, resreg.ActionGet, task.Owner); err != nil {
			return nil, err
		}
		return task, nil

	case tasksActionPatch:

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
		if _, err := time.Parse(time.RFC3339, nextRun); nextRun != "" && err != nil {
			return nil, resreg.Errorf(http.StatusBadRequest, "next_run must be RFC3339: %v", err)
		}
		gone := resreg.Errorf(http.StatusNotFound, "task not found")
		var err error
		switch {
		case status == core.TaskActive && nextRun == "" && task.Status == core.TaskPaused:
			err = s.db.store().ResumeTaskInto(x.Writes, task, gone)
		default:

			if status == "" && nextRun != "" &&
				(task.Status == core.TaskCompleted || task.Status == store.TaskStatusFiring) {
				status = core.TaskActive
			}
			err = s.db.store().PatchTaskInto(x.Writes, taskID, status, nextRun, gone)
		}
		if err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}

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

func (s *Server) scheduledTasksPostBuild(folder, callerSub string, authorize authorizeFn, visible func(string) bool, callerID auth.Identity) func(*mcpserver.MCPServer) {

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

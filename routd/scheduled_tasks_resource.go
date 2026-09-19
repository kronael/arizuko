package routd

// scheduled_tasks_resource.go folds the agent's task tools onto ONE resreg.Resource
// (spec 5/16). resreg owns the plumbing; routd owns the auth POLICY. Two shapes:
//
//   - Custom actions: schedule/pause/resume/cancel are resource-specific verbs, so
//     Mutates() is true and each sends the mutation+audit batch; list reuses ActionList.
//     MCPNames maps every action back to the flat live tool name.
//   - PER-TASK-ID containment. pause/resume/cancel take a task ID and MUST resolve
//     the task's OWNER folder before the caller can be ruled on. The HANDLER runs the
//     injected contain seam on the RESOLVED owner, not the Gate, because that owner
//     resolution is a DB read the handler already performs — splitting it would
//     duplicate the read AND reorder validation ahead of the containment decision.
//
// The four writes are store.CreateTaskTx, SetTaskStatusTx, PatchTaskTx and
// DeleteTaskInto, on resreg.invoke's write set so each lands with the call's single
// audit_log row. They live there because a task's coordinate is its id and its
// attribute names are store's — the same items timed's fire loop claims, which
// reads `next_run` as the operator's own RFC3339 text rather than as bytes.
//
// The operator REST face ALSO rides this handler. Both faces read ONE
// resources.ScheduledTasksEndpoints: the REST verbs carry a Verb+Path, the agent-only
// actions carry MCPOnly, and get/patch carry no MCPDoc. mountTasks mounts that slice
// verbatim — no override (BUGS F21). The fire-loop endpoints stay hand-rolled in
// tasks_http.go: they are timed's internal control plane, not resource CRUD.

import (
	"context"
	"fmt"
	"net/http"
	"time"
	"uuid"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
)

// Task actions are resource-specific verbs, not the CRUD shape. list reuses
// resreg.ActionList (== "list") so it stays read-only (Mutates() false, no tx);
// schedule/pause/resume/cancel are custom strings so Mutates() is true and each
// sends the mutation+audit batch.
const (
	tasksActionSchedule = resreg.Action("schedule")
	tasksActionPause    = resreg.Action("pause")
	tasksActionResume   = resreg.Action("resume")
	tasksActionCancel   = resreg.Action("cancel")
	// tasksActionPatch is the REST-only PATCH verb: {status} pause/resume +
	// {next_run} run-now. It has NO MCPDoc entry, so deriveMCPTools never
	// surfaces it as an agent tool; it exists only for the mountTasks REST face.
	tasksActionPatch = resreg.Action("patch")
)

// tasksMCPNames is the action→flat-tool-name map, single-sourced in resreg/
// resources (ScheduledTasksMCPNames). Aliased here for the Gate's action→policy-
// name lookup and the handler's pause/resume/cancel policy-name source. The
// REST-only `patch` verb has no entry (no agent tool).
var tasksMCPNames = resources.ScheduledTasksMCPNames

// scheduledTasksResource is the single renderer for BOTH faces: deriveMCPTools reads
// Endpoints ∩ MCPDoc for the agent's five tools, RegisterREST mounts the same slice's
// non-MCPOnly entries. Store is a store.Store over routd.db so resreg.invoke opens the
// mutation+audit batch there. contain is the per-face target-containment seam.
func (s *Server) scheduledTasksResource(contain containFn, elevated bool) resreg.Resource {
	r := resreg.Mount(resources.ScheduledTasksName)
	r.Store = s.db.store()
	r.Handler = func(ctx context.Context, x resreg.Execution) (any, error) {
		return s.scheduledTasksHandler(ctx, x, contain, elevated)
	}
	return r
}

// scheduledTasksHandler runs schedule/pause/resume/cancel/list against routd.db,
// folding in the bespoke semantics the deleted ipc bodies enforced: cron/interval/
// one-shot parsing, active-task dedup, DefaultFolderForJID target resolution, and
// contextMode normalization. Per-task/target containment is the injected contain
// seam (auth.Authorize on the target for the agent, ownsFolder for REST).
func (s *Server) scheduledTasksHandler(ctx context.Context, x resreg.Execution, contain containFn, elevated bool) (any, error) {
	switch x.Action {
	case resreg.ActionList:
		// The list-all key differs by surface, both through this one handler. On the
		// agent socket a root caller — the /root elevation or the "" sentinel — sees
		// every task. On REST only a root/service token (empty jwt_folder claim) sees
		// all: a folder-SCOPED token, even a top-level one, lists ONLY its own tasks,
		// never a sibling's. The REST caller is detected by the jwt_folder claim it
		// always sets; the agent sets none, so it keeps the elevation-based widening,
		// which rides the turn's EXPLICIT elevation rather than a re-resolve of the
		// caller folder that under /root from a deep folder wrongly read non-root.
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
		targetFolder := s.db.DefaultFolderForJID(targetJid)
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
		// PER-TASK-ID cap: resolve the task's owner and require the caller contain it
		// — a folder must not pause/resume/cancel a task outside its reach. The
		// per-face rule is the injected contain seam (agent auth.Authorize on the
		// owner / REST ownsFolder).
		if err := contain(x.Caller, x.Action, task.Owner); err != nil {
			return nil, err
		}
		// A status flip is an Update, so a task removed between the read above and
		// the batch refuses it — the 404 the read would have given.
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
		// Get-one (REST only): the Gate's ownsFolder on the JWT folder is a no-op for a
		// per-task op, so the real cap is contain() on the task's OWNER — a tenant must
		// not read a task outside its subtree by guessing its ID. A root/operator token
		// still reads any task, which the operator dashboard relies on.
		task, ok := s.db.GetTask(argString(x.Args, "taskId"))
		if !ok {
			return nil, resreg.Errorf(http.StatusNotFound, "task not found")
		}
		if err := contain(x.Caller, resreg.ActionGet, task.Owner); err != nil {
			return nil, err
		}
		return task, nil

	case tasksActionPatch:
		// PATCH (REST only) folds the retired handleTaskPatch: {status}
		// pause/resume + {next_run} run-now, one or both. Per-task containment is
		// the injected contain seam resolved on the task's owner — the same seam
		// pause/resume/cancel use.
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
		// Echo the updated task (the batch has not been sent, so a plain GetTask
		// would read the pre-patch row — mirror the write onto the fetched row).
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

// scheduledTasksPostBuild returns the ServeMCP seam that mounts the task tools on
// the agent socket, with the per-target contain seam + the turn's visibility view
// injected. The Gate is a no-op here; the per-task/target cap lives in the handler
// (see header). Only grants the caller already holds can widen visibility, so a
// denied caller sees nothing new.
func (s *Server) scheduledTasksPostBuild(folder, callerSub string, authorize authorizeFn, visible func(string) bool, callerID auth.Identity) func(*mcpserver.MCPServer) {
	// Agent face: one evaluator on the resolved task owner — the caller must hold the
	// tool scoped to cover it. Magnitude + containment in one; /root elevates via
	// authorize's allow-all.
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

// parseTaskSchedule reads the `cron` arg into (next_run, stored-cron): an RFC3339
// timestamp is a one-shot (empty stored cron → timed marks it completed after one
// firing), anything else is core.NextRun's value space and is stored verbatim. An
// empty arg leaves next_run nil (no schedule).
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

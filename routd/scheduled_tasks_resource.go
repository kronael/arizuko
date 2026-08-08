package routd

// scheduled_tasks_resource.go is the spec 5/16 step after web_routes +
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
//   - PER-TASK-ID containment. Unlike web_routes (folder owns its own row) and
//     network_rules (target folder is a plain arg), pause/resume/cancel take a
//     task ID and MUST resolve the task's OWNER folder before the caller can be
//     ruled on. The HANDLER runs the injected contain seam on the RESOLVED owner
//     (GetTask(id).Owner for pause/resume/cancel, DefaultFolderForJID(jid) for
//     schedule) — mirroring the deleted ipc bodies and inspect_tasks (inspect.go).
//     It lives in the handler, not the Gate, because that owner resolution is a DB
//     read the handler already performs; splitting it into the Gate would
//     duplicate the read AND reorder the "target group not registered" / "task not
//     found" validation ahead of the containment decision. The cap runs before any
//     store write and rolls the tx back on denial.
//
// The operator REST face (/v1/tasks CRUD) ALSO rides this handler — the 5/16
// REST fold: mountTasks REST-mounts list/get/patch/cancel with a
// tasks:read/write + JWT-folder Gate + Caller injected (verify → hasAnyScope +
// ownsFolder), exactly as web_routes folded its REST twin. Both faces read ONE
// resources.ScheduledTasksEndpoints: the REST verbs carry a Verb+Path, the
// agent-only schedule/pause/resume carry MCPOnly, and get/patch carry no MCPDoc
// entry. mountTasks mounts that slice verbatim — no override (BUGS F21). The
// fire-loop endpoints (/v1/tasks/due, /runs, /{id}/reschedule, run-logs) stay
// hand-rolled in tasks_http.go — they are timed's internal control plane, not
// resource CRUD.

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
	"time"
	"uuid"

	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/robfig/cron/v3"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
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

// scheduledTasksResource is the single renderer for BOTH faces: deriveMCPTools
// reads Endpoints ∩ MCPDoc for the agent's five task tools, RegisterREST mounts
// the same slice's non-MCPOnly entries for /v1/tasks. Store is a
// store.Store over routd.db so resreg.invoke opens the mutation+audit tx there.
// contain is the per-face target-containment seam (auth.Authorize on the target
// for the agent, ownsFolder for REST) closed into the handler — see containFn.
func (s *Server) scheduledTasksResource(contain containFn, elevated bool) resreg.Resource {
	r := resreg.Resource{
		Name:      resources.ScheduledTasksName,
		Endpoints: resources.ScheduledTasksEndpoints, // single source: doc + MCP read one list
		MCPDoc:    resources.ScheduledTasksMCPDoc,    // single source (resreg/resources)
		MCPArgs:   resources.ScheduledTasksMCPArgs,
		MCPNames:  tasksMCPNames,
		Authz: func(resreg.Caller, resreg.Action, resreg.Args) (string, map[string]string, error) {
			return "", nil, nil
		},
		Store: store.New(s.db.SQL()),
	}
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
		// The list-all key differs by surface, both through this one handler:
		//   agent (MCP): a root caller — the /root elevation or the "" sentinel —
		//     sees every task; the deleted list_tasks body's isRoot widening.
		//   operator (REST): only a root/service token (empty jwt_folder claim,
		//     for which tasksTarget resolved Caller.Folder to "") sees all. A
		//     folder-SCOPED token — even a top-level one — resolves to its own
		//     non-empty folder, so it lists ONLY its own tasks, never a
		//     sibling's (the 5/16 list-all leak guard). The REST caller is
		//     detected by the jwt_folder claim it always sets; the agent sets
		//     none, so it keeps the elevation-based widening.
		// Agent list-all widening rides the turn's EXPLICIT elevation (passed in) — not
		// a re-resolve of the caller folder, which under /root from a named deep folder
		// wrongly read non-root and lost the widening. The "" sentinel (operator/service)
		// still widens. REST detection stays on the jwt_folder claim.
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
		// PER-TASK-ID cap: resolve the task's owner and require the caller contain it
		// — a folder must not pause/resume/cancel a task outside its reach. The
		// per-face rule is the injected contain seam (agent auth.Authorize on the
		// owner / REST ownsFolder).
		if err := contain(x.Caller, x.Action, task.Owner); err != nil {
			return nil, err
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

	case resreg.ActionGet:
		// Get-one (REST only): scope-gated read + per-task containment. The Gate
		// checked tasks:read + ownsFolder on the JWT folder — a no-op for a per-task
		// op — so, exactly like pause/resume/cancel/patch, the real cap is contain()
		// on the task's OWNER: a tenant must not read a task outside its subtree by
		// guessing its ID. A root/operator token (empty folder) still reads any task
		// (ownsFolder("", _) is true), which the operator dashboard relies on.
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
		if err := patchTaskTx(ctx, x.Tx, taskID, status, nextRun); err != nil {
			return nil, resreg.Errorf(http.StatusInternalServerError, "%v", err)
		}
		// Echo the updated task (the tx has not committed, so a plain GetTask
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
		if !authorize(callerSub, target, "mcp:"+name, nil) {
			return resreg.Errorf(http.StatusForbidden, "%s on %s: not permitted", name, target)
		}
		return nil
	}
	res := s.scheduledTasksResource(contain, callerID.IsRoot)
	res.Gate = agentAllowGate
	return mountAgentResource(res, callerSub, folder, visible)
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
		t.ID, t.Owner, t.ChatJID, t.Prompt, nullStr(t.Cron),
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

// patchTaskTx applies the REST PATCH partial update on tx (mirrors
// store.PatchTask): status and/or next_run, at least one non-empty. next_run is
// stored verbatim (RFC3339). Backs the tasksActionPatch handler branch so the
// mutation lands in resreg.invoke's tx alongside its audit_log row.
func patchTaskTx(ctx context.Context, tx *sql.Tx, id, status, nextRun string) error {
	switch {
	case status != "" && nextRun != "":
		_, err := tx.ExecContext(ctx, `UPDATE scheduled_tasks SET status=?, next_run=? WHERE id=?`, status, nextRun, id)
		return err
	case status != "":
		_, err := tx.ExecContext(ctx, `UPDATE scheduled_tasks SET status=? WHERE id=?`, status, id)
		return err
	case nextRun != "":
		_, err := tx.ExecContext(ctx, `UPDATE scheduled_tasks SET next_run=? WHERE id=?`, nextRun, id)
		return err
	}
	return nil
}

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
// The operator REST face (/v1/tasks CRUD) now ALSO rides this handler — the
// 5/16 REST fold: mountTasks REST-mounts list/get/patch/cancel with a
// tasks:read/write + JWT-folder Gate + Caller injected (verify → hasAnyScope +
// ownsFolder), exactly as web_routes folded its REST twin. The Endpoints on the
// resource literal below still exist only to drive deriveMCPTools (the agent
// tools); the REST mount overrides them with the /v1/tasks CRUD verbs. The
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

	"github.com/google/uuid"
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

// scheduledTasksResource is the single renderer for the agent's five task tools.
// Endpoints exist only to drive deriveMCPTools (Action ∩ MCPDoc) — the REST face
// (/v1/tasks) is NOT mounted from this resource (see file header). Store is a
// store.Store over routd.db so resreg.invoke opens the mutation+audit tx there.
// contain is the per-face target-containment seam (tier model for the agent,
// ownsFolder for REST) closed into the handler — see containFn.
func (s *Server) scheduledTasksResource(contain containFn) resreg.Resource {
	r := resreg.Resource{
		Name:      "scheduled_tasks",
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
		return s.scheduledTasksHandler(ctx, x, contain)
	}
	return r
}

// scheduledTasksHandler runs schedule/pause/resume/cancel/list against routd.db,
// folding in the bespoke semantics the deleted ipc bodies enforced: cron/interval/
// one-shot parsing, active-task dedup, DefaultFolderForJID target resolution, and
// contextMode normalization. Per-task/target containment is the injected contain
// seam (tier model for the agent, ownsFolder for REST). id is resolved only for the
// list-all tier-0 widening (agent face).
func (s *Server) scheduledTasksHandler(ctx context.Context, x resreg.Execution, contain containFn) (any, error) {
	id := auth.Resolve(x.Caller.Folder)
	switch x.Action {
	case resreg.ActionList:
		// The list-all key differs by surface, both through this one handler:
		//   agent (MCP): a tier-0 (root) folder sees every task — the deleted
		//     list_tasks body's isRoot widening.
		//   operator (REST): only a root/service token (empty jwt_folder claim,
		//     for which tasksTarget resolved Caller.Folder to "") sees all. A
		//     folder-SCOPED token — even a tier-0 top-level one — resolves to
		//     its own non-empty folder, so it lists ONLY its own tasks, never a
		//     sibling's (the 5/16 list-all leak guard). The REST caller is
		//     detected by the jwt_folder claim it always sets; the agent sets
		//     none, so it keeps the tier-based widening.
		all := id.Tier == 0
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
		// per-face rule is the injected contain seam (agent tier model / REST
		// ownsFolder).
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
// the agent socket, with the tier-aware Gate + MatchingRules visibility for this
// folder's grant rules injected. The Gate does the TOOL-level grant (CheckAction
// + db.Authorize); the per-task/target structural cap lives in the handler (see
// header). Only rules the socket already carries can widen visibility, so a
// denied tier still sees nothing new.
func (s *Server) scheduledTasksPostBuild(folder, callerSub string, rules []string, authorize authorizeFn, callerID auth.Identity) func(*mcpserver.MCPServer) {
	// Agent face: the task tier cap on the resolved owner (tier 0 any, tier 1 own
	// world, tier 2 own folder, tier 3 none). callerID is tier 0 under /root (else
	// the socket folder's tier). Exactly the deleted ipc bodies' + inspect_tasks'
	// authzStructural.
	contain := func(_ resreg.Caller, a resreg.Action, target string) error {
		if err := auth.AuthorizeStructural(callerID, tasksMCPNames[a],
			auth.AuthzTarget{TaskOwner: target}); err != nil {
			return resreg.Errorf(http.StatusForbidden, "%v", err)
		}
		return nil
	}
	res := s.scheduledTasksResource(contain)
	res.Gate = func(x resreg.Execution, _ string, _ map[string]string) error {
		return toolGrant(rules, authorize, callerSub, folder, tasksMCPNames[x.Action])
	}
	return func(srv *mcpserver.MCPServer) {
		resreg.MCPTools(srv, res, agentCallerFor(callerSub, folder), agentVisible(rules))
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

// nilIfEmptyStr returns nil for an empty string (SQL NULL) else the string,
// matching store.nilIfEmpty for the nullable cron column.
func nilIfEmptyStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

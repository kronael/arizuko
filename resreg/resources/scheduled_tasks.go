package resources

import (
	"context"
	"database/sql"
	"reflect"

	"github.com/kronael/arizuko/resreg"
)

// ScheduledTasksRow mirrors scheduled_tasks. cron and next_run are
// nullable TEXT in DB; the Go field stays string with empty=NULL via
// ColumnOverride. context_mode has default 'group' enforced in hook.
type ScheduledTasksRow struct {
	ID          string `db:"id"           yaml:"id"           json:"id"`
	Owner       string `db:"owner"        yaml:"owner"        json:"owner"`
	ChatJID     string `db:"chat_jid"     yaml:"chat_jid"     json:"chat_jid"`
	Prompt      string `db:"prompt"       yaml:"prompt"       json:"prompt"`
	Cron        string `db:"cron"         yaml:"cron,omitempty" json:"cron,omitempty"`
	NextRun     string `db:"next_run"     yaml:"next_run,omitempty" json:"next_run,omitempty"`
	Status      string `db:"status"       yaml:"status,omitempty" json:"status,omitempty"`
	Created     string `db:"created_at"   yaml:"created_at,omitempty" json:"created_at,omitempty"`
	ContextMode string `db:"context_mode" yaml:"context_mode,omitempty" json:"context_mode,omitempty"`
}

// ScheduledTasksEndpoints is the single owner of the scheduled_tasks endpoint
// set that drives the agent's task tools (routd scheduled_tasks_resource.go
// references it). schedule/pause/resume are custom POST verbs and cancel is a
// body-addressed DELETE, so the real faces diverge from the PK-CRUD convention.
// (The operator /v1/tasks REST CRUD is a separate mount that overrides these.)
var ScheduledTasksEndpoints = []resreg.Endpoint{
	{Verb: "POST", Path: "/v1/scheduled_tasks", Action: resreg.Action("schedule")},
	{Verb: "POST", Path: "/v1/scheduled_tasks/pause", Action: resreg.Action("pause")},
	{Verb: "POST", Path: "/v1/scheduled_tasks/resume", Action: resreg.Action("resume")},
	{Verb: "DELETE", Path: "/v1/scheduled_tasks", Action: resreg.Action("cancel")},
	{Verb: "GET", Path: "/v1/scheduled_tasks", Action: resreg.ActionList},
}

// ScheduledTasksMCPNames maps each action to the flat tool name the live agent
// already calls; routd's scheduled_tasks_resource.go references it (agent socket
// derivation) and ipc.ListTools reads it via the registry walk. The REST-only
// `patch` verb has no entry here (no agent tool). Spec 5/16.
var ScheduledTasksMCPNames = map[resreg.Action]string{
	resreg.Action("schedule"): "schedule_task",
	resreg.Action("pause"):    "pause_task",
	resreg.Action("resume"):   "resume_task",
	resreg.Action("cancel"):   "cancel_task",
	resreg.ActionList:         "list_tasks",
}

// ScheduledTasksMCPDoc is the single owner of the task tools' agent-facing
// one-liners. Copy verbatim — the agent wire contract.
var ScheduledTasksMCPDoc = map[resreg.Action]string{
	resreg.Action("schedule"): "Create a scheduled prompt that fires against a target chat. Use when the user asks for reminders, recurring checks, or deferred work. `cron` accepts a 5-field cron expression, an integer millisecond interval, or an RFC3339 timestamp for a one-shot. Not for immediate execution (`send`/inject_message).",
	resreg.Action("pause"):    "Mark a scheduled task paused so it stops firing but is preserved. Use when suspending a task temporarily. Not for permanent removal (cancel_task).",
	resreg.Action("resume"):   "Re-activate a paused task so it resumes firing on its schedule. Use to undo pause_task. No effect on already-active or cancelled tasks.",
	resreg.Action("cancel"):   "Permanently delete a scheduled task. Use when the task is no longer wanted. Not for temporary suspension (pause_task) — this cannot be undone.",
	resreg.ActionList:         "Return scheduled tasks visible to this group. Use for a plain task dump; prefer inspect_tasks when you also want task_run_logs or per-task history.",
}

// ScheduledTasksMCPArgs is the explicit per-action arg list. The agent face carries
// {targetJid, prompt, cron, contextMode} / {taskId}, NOT the RowType-reflected
// columns, so this overrides RowType reflection for the derived agent/browser tools.
var ScheduledTasksMCPArgs = map[resreg.Action][]resreg.MCPArg{
	resreg.Action("schedule"): {
		{Name: "targetJid", Type: "string", Required: true},
		{Name: "prompt", Type: "string", Required: true},
		{Name: "cron", Type: "string"},
		{Name: "contextMode", Type: "string"},
	},
	resreg.Action("pause"):  {{Name: "taskId", Type: "string", Required: true}},
	resreg.Action("resume"): {{Name: "taskId", Type: "string", Required: true}},
	resreg.Action("cancel"): {{Name: "taskId", Type: "string", Required: true}},
}

func init() {
	resreg.Register(resreg.Resource{
		Name:      "scheduled_tasks",
		Table:     "scheduled_tasks",
		RowType:   reflect.TypeFor[ScheduledTasksRow](),
		PKFields:  []string{"ID"},
		Endpoints: ScheduledTasksEndpoints,
		MCPDoc:    ScheduledTasksMCPDoc,
		MCPArgs:   ScheduledTasksMCPArgs,
		MCPNames:  ScheduledTasksMCPNames,
		// No folder scope: owner is system/user:sub and chat_jid is
		// polymorphic (folder OR typed JID, spec 5/8 §"FK posture") —
		// neither is column-equal to a folder. Apply rebuilds wholesale.
		StampedFields: []string{"Created"},
		Hooks: resreg.Hooks{
			BeforeInsert: func(_ context.Context, _ *sql.Tx, row any) error {
				r := row.(*ScheduledTasksRow)
				if r.Status == "" {
					r.Status = "active"
				}
				if r.ContextMode == "" {
					r.ContextMode = "group"
				}
				return nil
			},
			ColumnOverride: map[string]resreg.ColumnHook{
				"Cron": {
					Read:  "COALESCE(cron, '')",
					Write: nilIfEmptyString,
				},
				"NextRun": {
					Read:  "COALESCE(next_run, '')",
					Write: nilIfEmptyString,
				},
			},
		},
	})
}

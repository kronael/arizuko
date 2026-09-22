package resources

import (
	"reflect"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/resreg"
)

// ScheduledTasksRow mirrors scheduled_tasks; BeforeInsert defaults status and
// context_mode.
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

// ScheduledTasksResponses: core.Task carries NO json tags, so the wire names
// are the GO field names, and documenting off ScheduledTasksRow promised
// snake_case (BUGS L120). cancel answers {"ok"} and declares nothing.
var ScheduledTasksResponses = map[resreg.Action]reflect.Type{
	resreg.ActionList:      reflect.TypeFor[[]core.Task](),
	resreg.ActionGet:       reflect.TypeFor[core.Task](),
	resreg.Action("patch"): reflect.TypeFor[core.Task](),
}

// ScheduledTasksEndpoints is the single owner of the endpoint set; routd must
// never override it (BUGS F21). The REST path is /v1/tasks, NOT /v1/<name>:
// timed calls it across the container boundary and its fire loop shares the
// prefix. schedule/pause/resume are MCPOnly (the operator drives them through
// PATCH {status}); get/patch are REST-only with no MCPDoc entry, hence no tool.
var ScheduledTasksEndpoints = []resreg.Endpoint{
	{Action: resreg.Action("schedule"), MCPOnly: true},
	{Action: resreg.Action("pause"), MCPOnly: true},
	{Action: resreg.Action("resume"), MCPOnly: true},
	{Verb: "GET", Path: "/v1/tasks", Action: resreg.ActionList},
	{Verb: "GET", Path: "/v1/tasks/{taskId}", Action: resreg.ActionGet},
	{Verb: "PATCH", Path: "/v1/tasks/{taskId}", Action: resreg.Action("patch")},
	{Verb: "DELETE", Path: "/v1/tasks/{taskId}", Action: resreg.Action("cancel")},
}

// ScheduledTasksMCPNames maps each action to the flat tool name the live agent
// calls (spec 5/16); the REST-only `patch` verb has no entry.
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

// ScheduledTasksMCPArgs is the agent face's wire shape, overriding RowType
// reflection.
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
		Name:          ScheduledTasksName,
		Table:         "scheduled_tasks",
		DB:            resreg.SubsystemRoutd,
		RowType:       reflect.TypeFor[ScheduledTasksRow](),
		PKFields:      []string{"ID"},
		EntitySortKey: []string{"ID"},
		Endpoints:     ScheduledTasksEndpoints,
		Responses:     ScheduledTasksResponses,
		MCPDoc:        ScheduledTasksMCPDoc,
		MCPArgs:       ScheduledTasksMCPArgs,
		MCPNames:      ScheduledTasksMCPNames,
		// No folder scope: chat_jid is polymorphic (folder OR typed JID, spec
		// 5/8 §"FK posture"), so Apply rebuilds wholesale.
		StampedFields: []string{"Created"},
		Hooks: resreg.Hooks{
			BeforeInsert: func(row any) error {
				r := row.(*ScheduledTasksRow)
				if r.Status == "" {
					r.Status = "active"
				}
				if r.ContextMode == "" {
					r.ContextMode = "group"
				}
				return nil
			},
		},
	})
}

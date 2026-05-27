package resources

import (
	"context"
	"database/sql"
	"reflect"
	"time"

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

func init() {
	resreg.Register(resreg.Resource{
		Name:     "scheduled_tasks",
		Table:    "scheduled_tasks",
		RowType:  reflect.TypeOf(ScheduledTasksRow{}),
		PKFields: []string{"ID"},
		Scope:    resreg.ScopeSpec{Field: "Owner"},
		Hooks: resreg.Hooks{
			BeforeInsert: func(_ context.Context, _ *sql.Tx, row any) error {
				r := row.(*ScheduledTasksRow)
				if r.Status == "" {
					r.Status = "active"
				}
				if r.ContextMode == "" {
					r.ContextMode = "group"
				}
				if r.Created == "" {
					r.Created = time.Now().UTC().Format(time.RFC3339)
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

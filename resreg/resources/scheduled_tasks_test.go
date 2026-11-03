package resources

import (
	"testing"
	"time"

	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/store"
)

// A cron row applied without next_run (package sync, rows apply) must enter the
// due set: DueTasks skips an item with no next_run, so such a row never fired
// (sloth's macrohype-monitor, 2026-10-28). A stated next_run stays.
func TestApplySeedsNextRunFromCron(t *testing.T) {
	db := openMem(t)
	rows := []ScheduledTasksRow{
		{ID: "bare", Owner: "main", ChatJID: "main", Prompt: "p", Cron: "* * * * *"},
		{ID: "stated", Owner: "main", ChatJID: "main", Prompt: "p", Cron: "* * * * *",
			NextRun: "2030-01-01T00:00:00Z"},
		{ID: "oneshot", Owner: "main", ChatJID: "main", Prompt: "p"},
	}
	if _, err := resreg.Apply(t.Context(), resreg.On(db), resreg.SubsystemRoutd,
		routdChecksum(t, db), false, map[string]any{"scheduled_tasks": rows}, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	st := store.NewOver(db)
	bare, _ := st.GetTask("bare")
	if bare.NextRun == nil || time.Until(*bare.NextRun) > time.Minute {
		t.Errorf("bare cron row next_run = %v, want within a minute", bare.NextRun)
	}
	stated, _ := st.GetTask("stated")
	if stated.NextRun == nil || stated.NextRun.Year() != 2030 {
		t.Errorf("stated next_run = %v, want kept", stated.NextRun)
	}
	if one, _ := st.GetTask("oneshot"); one.NextRun != nil {
		t.Errorf("row without cron got next_run %v", one.NextRun)
	}
}

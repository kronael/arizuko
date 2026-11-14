package resources

import (
	"fmt"
	"testing"
	"time"

	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/store"
)

func TestApplyScheduledTaskDueIndex(t *testing.T) {
	db := openMem(t)
	now := time.Now()
	var rows []ScheduledTasksRow
	for i := range 40 {
		due := now.Add(time.Hour)
		if i == 0 {
			due = now.Add(-time.Minute)
		}
		rows = append(rows, ScheduledTasksRow{ID: fmt.Sprint(i), Owner: "main",
			ChatJID: "main", Prompt: "p", NextRun: due.Format(time.RFC3339)})
	}
	backend := resreg.On(db)
	if _, err := resreg.Apply(t.Context(), backend, resreg.SubsystemRoutd,
		routdChecksum(t, db), false, map[string]any{"scheduled_tasks": rows}, nil); err != nil {
		t.Fatal(err)
	}
	st := store.NewOver(db)
	tasks, err := st.DueTasks(now)
	if err != nil || len(tasks) != 1 || tasks[0].ID != "0" {
		t.Fatalf("applied work missing: %v %v", tasks, err)
	}
	if _, err := resreg.Apply(t.Context(), backend, resreg.SubsystemRoutd,
		routdChecksum(t, db), false, map[string]any{"scheduled_tasks": []ScheduledTasksRow{}}, nil); err != nil {
		t.Fatal(err)
	}
	if tasks, err := st.DueTasks(now.Add(2 * time.Hour)); err != nil || len(tasks) != 0 {
		t.Fatalf("deleted work remains: %v %v", tasks, err)
	}
}

// A cron row applied without next_run (package sync, rows apply) stays without
// one until the scheduler's poll fills it, so it enters the due set instead of
// never firing (sloth's macrohype-monitor, 2026-10-28). A stated next_run stays.
func TestApplyLeavesNextRunToTheScheduler(t *testing.T) {
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
	if bare, _ := st.GetTask("bare"); bare.NextRun != nil {
		t.Fatalf("Apply wrote next_run %v for a bare cron row", bare.NextRun)
	}
	if _, err := st.DueTasks(time.Now()); err != nil {
		t.Fatalf("DueTasks: %v", err)
	}
	bare, _ := st.GetTask("bare")
	if bare.NextRun == nil || time.Until(*bare.NextRun) > time.Minute {
		t.Errorf("bare cron row next_run = %v, want filled within a minute", bare.NextRun)
	}
	stated, _ := st.GetTask("stated")
	if stated.NextRun == nil || stated.NextRun.Year() != 2030 {
		t.Errorf("stated next_run = %v, want kept", stated.NextRun)
	}
	if one, _ := st.GetTask("oneshot"); one.NextRun != nil {
		t.Errorf("row without cron got next_run %v", one.NextRun)
	}
}

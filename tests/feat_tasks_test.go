// Scheduled task feature tests: create, state machine (pause/resume/cancel),
// reschedule guard (next_run must never go NULL — regression .diary/20260618),
// crash recovery, and cron fire end-to-end via the real timed→routd wire.
package tests

import (
	"database/sql"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
)

func TestFeature_Tasks(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		s, _ := openSharedDB(t)
		now := time.Now()
		next := now.Add(time.Hour)
		if err := s.CreateTask(core.Task{
			ID: "t-create", Owner: "main", ChatJID: "tg:1", Prompt: "standup",
			Cron: "0 9 * * *", NextRun: &next, Status: "active", Created: now,
		}); err != nil {
			t.Fatal(err)
		}
		got, ok := s.GetTask("t-create")
		if !ok || got.Prompt != "standup" || got.Cron != "0 9 * * *" {
			t.Fatalf("task = %+v ok=%v", got, ok)
		}
		if got.NextRun == nil {
			t.Fatal("next_run not set on create")
		}
	})

	// pause → hidden from due query; resume → visible again; cancel → deleted.
	t.Run("pause-resume-cancel", func(t *testing.T) {
		s, db := openSharedDB(t)
		now := time.Now()
		past := now.Add(-time.Minute)
		s.CreateTask(core.Task{ID: "t-sm", Owner: "main", ChatJID: "tg:1", Prompt: "p", Cron: "* * * * *", NextRun: &past, Status: "active", Created: now})

		if n := dueCount(t, db); n != 1 {
			t.Fatalf("active due = %d, want 1", n)
		}
		if err := s.SetTaskStatus("t-sm", "paused"); err != nil {
			t.Fatal(err)
		}
		if n := dueCount(t, db); n != 0 {
			t.Fatalf("paused due = %d, want 0", n)
		}
		if err := s.SetTaskStatus("t-sm", "active"); err != nil {
			t.Fatal(err)
		}
		if n := dueCount(t, db); n != 1 {
			t.Fatalf("resumed due = %d, want 1", n)
		}
		if err := s.DeleteTask("t-sm"); err != nil {
			t.Fatal(err)
		}
		if _, ok := s.GetTask("t-sm"); ok {
			t.Fatal("task still present after cancel")
		}
	})

	// Regression: rescheduling must set a concrete future next_run — a NULL or
	// empty next_run makes datetime(next_run)<=datetime(now) never true, killing
	// the task forever (.diary/20260618 krons incident).
	t.Run("reschedule-sets-future-next-run", func(t *testing.T) {
		s, db := openSharedDB(t)
		now := time.Now()
		past := now.Add(-time.Minute)
		s.CreateTask(core.Task{ID: "t-resched", Owner: "main", ChatJID: "tg:1", Prompt: "p", Cron: "0 9 * * *", NextRun: &past, Status: "active", Created: now})
		future := now.Add(24 * time.Hour).Format(time.RFC3339)
		if err := s.RescheduleTask("t-resched", future, "active"); err != nil {
			t.Fatal(err)
		}
		got, _ := s.GetTask("t-resched")
		if got.NextRun == nil || !got.NextRun.After(now) {
			t.Fatalf("next_run %v should be future", got.NextRun)
		}
		if n := dueCount(t, db); n != 0 {
			t.Fatalf("future task still due: %d", n)
		}
	})

	// A task stuck in 'firing' (crash mid-turn) is recovered back to 'active'
	// on the next timed boot — crash-safe restart.
	t.Run("firing-recovery", func(t *testing.T) {
		s, db := openSharedDB(t)
		now := time.Now()
		past := now.Add(-time.Minute)
		s.CreateTask(core.Task{ID: "t-fire", Owner: "main", ChatJID: "tg:1", Prompt: "p", Cron: "* * * * *", NextRun: &past, Status: "active", Created: now})
		db.Exec(`UPDATE scheduled_tasks SET status='firing' WHERE id=?`, "t-fire")
		if n, err := s.RecoverFiringTasks(); err != nil || n != 1 {
			t.Fatalf("recovered = %d err=%v, want 1", n, err)
		}
		got, _ := s.GetTask("t-fire")
		if got.Status != "active" {
			t.Fatalf("status after recovery = %q, want active", got.Status)
		}
	})

	// One-shot task (cron="") transitions to 'completed' after firing, not
	// rescheduled — verified end-to-end via the real timed fire loop.
	// Full cron-fire end-to-end (timed→routd) is in microservice_test.go::TestCronFiresMessage.
	t.Run("one-shot-completes", func(t *testing.T) {
		s, db := openSharedDB(t)
		now := time.Now()
		past := now.Add(-time.Second)
		s.CreateTask(core.Task{ID: "t-oneshot", Owner: "main", ChatJID: "tg:1", Prompt: "once", Cron: "", NextRun: &past, Status: "active", Created: now})
		if err := s.RescheduleTask("t-oneshot", "", "completed"); err != nil {
			t.Fatal(err)
		}
		if n := dueCount(t, db); n != 0 {
			t.Fatalf("completed task appeared due: %d", n)
		}
		got, _ := s.GetTask("t-oneshot")
		if got.Status != "completed" {
			t.Fatalf("status = %q, want completed", got.Status)
		}
	})

	// Multiple tasks with overlapping next_run are all returned by the due query.
	t.Run("due-query-returns-all-eligible", func(t *testing.T) {
		s, db := openSharedDB(t)
		now := time.Now()
		past := now.Add(-time.Minute)
		for _, id := range []string{"bulk-1", "bulk-2", "bulk-3"} {
			s.CreateTask(core.Task{ID: id, Owner: "main", ChatJID: "tg:1", Prompt: "p", Cron: "* * * * *", NextRun: &past, Status: "active", Created: now})
		}
		if n := dueCount(t, db); n != 3 {
			t.Fatalf("due count = %d, want 3", n)
		}
	})
}

func dueCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM scheduled_tasks WHERE status='active' AND datetime(next_run) <= datetime(?)`,
		time.Now().Format(time.RFC3339)).Scan(&n); err != nil {
		t.Fatalf("dueCount: %v", err)
	}
	return n
}

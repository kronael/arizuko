package store

import (
	"strconv"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
)

func TestSeedDefaultTasks(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.SeedDefaultTasks("alice", "alice"); err != nil {
		t.Fatal(err)
	}

	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM scheduled_tasks WHERE owner = ?`, "alice").Scan(&n)
	if n != len(defaultTasks) {
		t.Fatalf("want %d tasks, got %d", len(defaultTasks), n)
	}

	rows, err := s.db.Query(
		`SELECT id, chat_jid, prompt, cron, status, context_mode
		 FROM scheduled_tasks WHERE owner = ? ORDER BY id`, "alice")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var id, jid, prompt, cron, status, mode string
		if err := rows.Scan(&id, &jid, &prompt, &cron, &status, &mode); err != nil {
			t.Fatal(err)
		}
		seen[id] = true
		if jid != "alice" {
			t.Errorf("want chat_jid=alice, got %q", jid)
		}
		if status != "active" {
			t.Errorf("want status=active, got %q", status)
		}
		if mode != "isolated" {
			t.Errorf("want context_mode=isolated, got %q", mode)
		}
	}
	for i, dt := range defaultTasks {
		id := "alice-mem-" + strconv.Itoa(i)
		if !seen[id] {
			t.Errorf("missing task %s (%s)", id, dt.prompt)
		}
	}

	// Idempotent: second seed does not duplicate.
	if err := s.SeedDefaultTasks("alice", "alice"); err != nil {
		t.Fatal(err)
	}
	s.db.QueryRow(`SELECT COUNT(*) FROM scheduled_tasks WHERE owner = ?`, "alice").Scan(&n)
	if n != len(defaultTasks) {
		t.Fatalf("after re-seed want %d, got %d", len(defaultTasks), n)
	}

	// Verify prompt/cron for one entry.
	var prompt, cron string
	s.db.QueryRow(
		`SELECT prompt, cron FROM scheduled_tasks WHERE id = ?`, "alice-mem-0",
	).Scan(&prompt, &cron)
	if prompt != defaultTasks[0].prompt || cron != defaultTasks[0].cron {
		t.Errorf("alice-mem-0 got (%q,%q), want (%q,%q)",
			prompt, cron, defaultTasks[0].prompt, defaultTasks[0].cron)
	}
}

// DueTasks must claim each due task exactly once: the UPDATE...RETURNING
// flips status='active'→'firing' atomically, so a second poll over the same
// due set returns nothing (no two pollers fire the same task).
func TestDueTasks_ClaimsOnce(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	past := time.Now().Add(-time.Hour)
	if err := s.PutTaskRow(core.Task{
		ID: "t1", Owner: "alice", ChatJID: "alice", Prompt: "/ping",
		NextRun: &past, Status: core.TaskActive, Created: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	first, err := s.DueTasks(now)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || first[0].ID != "t1" {
		t.Fatalf("first claim: want [t1], got %v", first)
	}
	if first[0].Status != "firing" {
		t.Errorf("claimed task status: want firing, got %q", first[0].Status)
	}

	// Second poll of the same due set claims nothing — t1 is already 'firing'.
	second, err := s.DueTasks(now)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 0 {
		t.Fatalf("second claim: want none, got %v (double-fire)", second)
	}
}

// A task stranded in 'firing' by a mid-fire crash is re-armed by startup
// recovery; an 'active' task is left untouched.
func TestRecoverFiringTasks(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	past := time.Now().Add(-time.Hour)
	if err := s.PutTaskRow(core.Task{
		ID: "stuck", Owner: "a", ChatJID: "a", Prompt: "/ping",
		NextRun: &past, Status: core.TaskActive, Created: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	// Claim it → 'firing', then simulate a crash before reschedule.
	if _, err := s.DueTasks(time.Now()); err != nil {
		t.Fatal(err)
	}
	// Stuck: the next poll claims nothing (it's 'firing').
	if due, _ := s.DueTasks(time.Now()); len(due) != 0 {
		t.Fatalf("expected stuck task, got %v", due)
	}
	// Startup recovery re-arms it.
	n, err := s.RecoverFiringTasks()
	if err != nil || n != 1 {
		t.Fatalf("RecoverFiringTasks = %d, %v; want 1, nil", n, err)
	}
	// Now it fires again.
	due, err := s.DueTasks(time.Now())
	if err != nil || len(due) != 1 || due[0].ID != "stuck" {
		t.Fatalf("after recovery: want [stuck], got %v (err %v)", due, err)
	}
}

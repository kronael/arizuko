package store

import (
	"strconv"
	"testing"
)

func TestSeedDefaultTasks(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.SeedDefaultTasks("alice", "local:alice"); err != nil {
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
		if jid != "local:alice" {
			t.Errorf("want chat_jid=local:alice, got %q", jid)
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
	if err := s.SeedDefaultTasks("alice", "local:alice"); err != nil {
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

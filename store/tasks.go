package store

import (
	"strconv"
	"strings"
	"time"

	"github.com/onvos/arizuko/core"
)

type TaskPatch struct {
	Status  *string
	NextRun *time.Time
}

func (s *Store) CreateTask(t core.Task) error {
	var nextRun *string
	if t.NextRun != nil {
		s := t.NextRun.Format(time.RFC3339)
		nextRun = &s
	}
	cm := t.ContextMode
	if cm == "" {
		cm = "group"
	}
	_, err := s.db.Exec(
		`INSERT INTO scheduled_tasks (`+taskCols+`)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.Owner, t.ChatJID, t.Prompt, nilIfEmpty(t.Cron),
		nextRun, t.Status, t.Created.Format(time.RFC3339), cm,
	)
	return err
}

const taskCols = `id, owner, chat_jid, prompt, cron, next_run, status, created_at, context_mode`

func (s *Store) GetTask(id string) (core.Task, bool) {
	row := s.db.QueryRow(`SELECT `+taskCols+` FROM scheduled_tasks WHERE id = ?`, id)
	return scanTask(row)
}

func (s *Store) ListTasks(folder string, isRoot bool) []core.Task {
	owner := folder
	if isRoot {
		owner = ""
	}
	rows, err := s.db.Query(
		`SELECT `+taskCols+` FROM scheduled_tasks
		 WHERE (? = '' OR owner = ?) ORDER BY created_at DESC`, owner, owner)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []core.Task
	for rows.Next() {
		t, ok := scanTask(rows)
		if ok {
			out = append(out, t)
		}
	}
	return out
}

func (s *Store) UpdateTask(id string, p TaskPatch) error {
	var sets []string
	var args []any
	if p.Status != nil {
		sets = append(sets, "status = ?")
		args = append(args, *p.Status)
	}
	if p.NextRun != nil {
		sets = append(sets, "next_run = ?")
		args = append(args, p.NextRun.Format(time.RFC3339))
	}
	if len(sets) == 0 {
		return nil
	}
	args = append(args, id)
	_, err := s.db.Exec(
		"UPDATE scheduled_tasks SET "+strings.Join(sets, ", ")+" WHERE id = ?", args...)
	return err
}

func (s *Store) DeleteTask(id string) error {
	_, err := s.db.Exec(`DELETE FROM scheduled_tasks WHERE id = ?`, id)
	return err
}

func (s *Store) CountActiveTasks() int {
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM scheduled_tasks WHERE status = ?`, core.TaskActive).Scan(&n)
	return n
}

// Keep in sync with ant migration 055 which backfills legacy groups.
var defaultTasks = [...]struct{ prompt, cron string }{
	{"/compact-memories episodes day", "0 2 * * *"},
	{"/compact-memories episodes week", "0 3 * * 1"},
	{"/compact-memories episodes month", "0 4 1 * *"},
	{"/compact-memories diary week", "0 3 * * 1"},
	{"/compact-memories diary month", "0 4 1 * *"},
}

// SeedDefaultTasks inserts the 5 compact-memories tasks for a new group.
// Idempotent (INSERT OR IGNORE). New groups skip ant migration 055.
func (s *Store) SeedDefaultTasks(folder, chatJID string) error {
	now := time.Now().Format(time.RFC3339)
	for i, t := range defaultTasks {
		id := folder + "-mem-" + strconv.Itoa(i)
		_, err := s.db.Exec(
			`INSERT OR IGNORE INTO scheduled_tasks
			 (id, owner, chat_jid, prompt, cron, next_run, status, created_at, context_mode)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, folder, chatJID, t.prompt, t.cron, now, core.TaskActive, now, "isolated")
		if err != nil {
			return err
		}
	}
	return nil
}

func scanTask(r rowScanner) (core.Task, bool) {
	var t core.Task
	var cron, nextRun *string
	var created string
	err := r.Scan(&t.ID, &t.Owner, &t.ChatJID, &t.Prompt,
		&cron, &nextRun, &t.Status, &created, &t.ContextMode)
	if err != nil {
		return t, false
	}
	t.Created, _ = time.Parse(time.RFC3339, created)
	if cron != nil {
		t.Cron = *cron
	}
	if nextRun != nil {
		v, _ := time.Parse(time.RFC3339, *nextRun)
		t.NextRun = &v
	}
	if t.ContextMode == "" {
		t.ContextMode = "group"
	}
	return t, true
}

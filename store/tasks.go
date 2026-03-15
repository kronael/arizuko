package store

import (
	"database/sql"
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
	var cron *string
	if t.Cron != "" {
		cron = &t.Cron
	}
	_, err := s.db.Exec(
		`INSERT INTO scheduled_tasks
		 (id, owner, chat_jid, prompt, cron, next_run, status, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.Owner, t.ChatJID, t.Prompt, cron,
		nextRun, t.Status, t.Created.Format(time.RFC3339),
	)
	return err
}

func (s *Store) GetTask(id string) (core.Task, bool) {
	row := s.db.QueryRow(
		`SELECT id, owner, chat_jid, prompt, cron, next_run, status, created_at
		 FROM scheduled_tasks WHERE id = ?`, id)
	return scanTask(row)
}

func (s *Store) ListTasks(folder string, isRoot bool) []core.Task {
	var rows *sql.Rows
	var err error
	if isRoot {
		rows, err = s.db.Query(
			`SELECT id, owner, chat_jid, prompt, cron, next_run, status, created_at
			 FROM scheduled_tasks ORDER BY created_at DESC`)
	} else {
		rows, err = s.db.Query(
			`SELECT id, owner, chat_jid, prompt, cron, next_run, status, created_at
			 FROM scheduled_tasks WHERE owner = ? ORDER BY created_at DESC`, folder)
	}
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

func scanTask(r rowScanner) (core.Task, bool) {
	var t core.Task
	var cron, nextRun *string
	var created string
	err := r.Scan(&t.ID, &t.Owner, &t.ChatJID, &t.Prompt,
		&cron, &nextRun, &t.Status, &created)
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
	return t, true
}

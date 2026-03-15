package store

import (
	"database/sql"
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

func (s *Store) AllTasks() []core.Task {
	rows, err := s.db.Query(
		`SELECT id, owner, chat_jid, prompt, cron, next_run, status, created_at
		 FROM scheduled_tasks ORDER BY created_at DESC`)
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
	if p.Status != nil {
		if _, err := s.db.Exec(`UPDATE scheduled_tasks SET status = ? WHERE id = ?`,
			*p.Status, id); err != nil {
			return err
		}
	}
	if p.NextRun != nil {
		if _, err := s.db.Exec(`UPDATE scheduled_tasks SET next_run = ? WHERE id = ?`,
			p.NextRun.Format(time.RFC3339), id); err != nil {
			return err
		}
	}
	return nil
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

package store

import (
	"time"

	"github.com/onvos/arizuko/core"
)

type TaskPatch struct {
	Status   *string
	NextRun  *time.Time
	LastRun  *time.Time
	LastResult *string
}

func (s *Store) CreateTask(t core.Task) error {
	var nextRun *string
	if t.NextRun != nil {
		s := t.NextRun.Format(time.RFC3339)
		nextRun = &s
	}
	_, err := s.db.Exec(
		`INSERT INTO scheduled_tasks
		 (id, group_folder, chat_jid, prompt, schedule_type, schedule_value,
		  context_mode, next_run, status, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.Group, t.ChatJID, t.Prompt, t.SchedTyp, t.SchedVal,
		t.CtxMode, nextRun, t.Status, t.Created.Format(time.RFC3339),
	)
	return err
}

func (s *Store) GetTask(id string) (core.Task, bool) {
	row := s.db.QueryRow(
		`SELECT id, group_folder, chat_jid, prompt, schedule_type, schedule_value,
		        context_mode, next_run, last_run, last_result, status, created_at
		 FROM scheduled_tasks WHERE id = ?`, id)
	return scanTask(row)
}

func (s *Store) DueTasks() ([]core.Task, error) {
	now := time.Now().Format(time.RFC3339)
	rows, err := s.db.Query(
		`SELECT id, group_folder, chat_jid, prompt, schedule_type, schedule_value,
		        context_mode, next_run, last_run, last_result, status, created_at
		 FROM scheduled_tasks
		 WHERE status = 'active' AND next_run IS NOT NULL AND next_run <= ?`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []core.Task
	for rows.Next() {
		t, ok := scanTask(rows)
		if ok {
			out = append(out, t)
		}
	}
	return out, rows.Err()
}

func (s *Store) AllTasks() []core.Task {
	rows, err := s.db.Query(
		`SELECT id, group_folder, chat_jid, prompt, schedule_type, schedule_value,
		        context_mode, next_run, last_run, last_result, status, created_at
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
		s.db.Exec(`UPDATE scheduled_tasks SET status = ? WHERE id = ?`, *p.Status, id)
	}
	if p.NextRun != nil {
		s.db.Exec(`UPDATE scheduled_tasks SET next_run = ? WHERE id = ?`,
			p.NextRun.Format(time.RFC3339), id)
	}
	if p.LastRun != nil {
		s.db.Exec(`UPDATE scheduled_tasks SET last_run = ? WHERE id = ?`,
			p.LastRun.Format(time.RFC3339), id)
	}
	if p.LastResult != nil {
		s.db.Exec(`UPDATE scheduled_tasks SET last_result = ? WHERE id = ?`, *p.LastResult, id)
	}
	return nil
}

func (s *Store) DeleteTask(id string) error {
	_, err := s.db.Exec(`DELETE FROM scheduled_tasks WHERE id = ?`, id)
	return err
}

func (s *Store) LogRun(taskID string, dur time.Duration, status, result, errStr string) error {
	_, err := s.db.Exec(
		`INSERT INTO task_run_logs (task_id, run_at, duration_ms, status, result, error)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		taskID, time.Now().Format(time.RFC3339), dur.Milliseconds(),
		status, result, errStr,
	)
	return err
}

func scanTask(r rowScanner) (core.Task, bool) {
	var t core.Task
	var nextRun, lastRun, lastResult *string
	var created string
	err := r.Scan(&t.ID, &t.Group, &t.ChatJID, &t.Prompt, &t.SchedTyp,
		&t.SchedVal, &t.CtxMode, &nextRun, &lastRun, &lastResult,
		&t.Status, &created)
	if err != nil {
		return t, false
	}
	t.Created, _ = time.Parse(time.RFC3339, created)
	if nextRun != nil {
		v, _ := time.Parse(time.RFC3339, *nextRun)
		t.NextRun = &v
	}
	if lastRun != nil {
		v, _ := time.Parse(time.RFC3339, *lastRun)
		t.LastRun = &v
	}
	return t, true
}

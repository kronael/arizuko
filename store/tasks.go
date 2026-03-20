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

type TaskRun struct {
	ID         int64
	TaskID     string
	RunAt      time.Time
	DurationMs int64
	Status     string
	Result     string
	Error      string
}

func (s *Store) LogTaskRun(taskID, status, result, errorMsg string, durationMs int64) error {
	_, err := s.db.Exec(
		`INSERT INTO task_run_logs (task_id, run_at, duration_ms, status, result, error)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		taskID, time.Now().Format(time.RFC3339), durationMs, status,
		nullStr(result), nullStr(errorMsg),
	)
	return err
}

func (s *Store) ListTaskRuns(taskID string, limit int) ([]TaskRun, error) {
	rows, err := s.db.Query(
		`SELECT id, task_id, run_at, duration_ms, status, result, error
		 FROM task_run_logs WHERE task_id = ? ORDER BY run_at DESC LIMIT ?`,
		taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TaskRun
	for rows.Next() {
		var r TaskRun
		var runAt string
		var result, errStr *string
		if err := rows.Scan(&r.ID, &r.TaskID, &runAt, &r.DurationMs,
			&r.Status, &result, &errStr); err != nil {
			continue
		}
		r.RunAt, _ = time.Parse(time.RFC3339, runAt)
		if result != nil {
			r.Result = *result
		}
		if errStr != nil {
			r.Error = *errStr
		}
		out = append(out, r)
	}
	return out, nil
}

func nullStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
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
	cm := t.ContextMode
	if cm == "" {
		cm = "group"
	}
	_, err := s.db.Exec(
		`INSERT INTO scheduled_tasks
		 (id, owner, chat_jid, prompt, cron, next_run, status, created_at, context_mode)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.Owner, t.ChatJID, t.Prompt, cron,
		nextRun, t.Status, t.Created.Format(time.RFC3339), cm,
	)
	return err
}

func (s *Store) GetTask(id string) (core.Task, bool) {
	row := s.db.QueryRow(
		`SELECT id, owner, chat_jid, prompt, cron, next_run, status, created_at, context_mode
		 FROM scheduled_tasks WHERE id = ?`, id)
	return scanTask(row)
}

func (s *Store) ListTasks(folder string, isRoot bool) []core.Task {
	var rows *sql.Rows
	var err error
	if isRoot {
		rows, err = s.db.Query(
			`SELECT id, owner, chat_jid, prompt, cron, next_run, status, created_at, context_mode
			 FROM scheduled_tasks ORDER BY created_at DESC`)
	} else {
		rows, err = s.db.Query(
			`SELECT id, owner, chat_jid, prompt, cron, next_run, status, created_at, context_mode
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

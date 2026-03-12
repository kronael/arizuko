package store

import (
	"database/sql"
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

func (s *Store) ListTasks(folder string, isRoot bool) []core.Task {
	var rows *sql.Rows
	var err error
	if isRoot {
		rows, err = s.db.Query(
			`SELECT id, group_folder, chat_jid, prompt, schedule_type, schedule_value,
			        context_mode, next_run, last_run, last_result, status, created_at
			 FROM scheduled_tasks ORDER BY created_at DESC`)
	} else {
		rows, err = s.db.Query(
			`SELECT id, group_folder, chat_jid, prompt, schedule_type, schedule_value,
			        context_mode, next_run, last_run, last_result, status, created_at
			 FROM scheduled_tasks WHERE group_folder = ? ORDER BY created_at DESC`, folder)
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
		if _, err := s.db.Exec(`UPDATE scheduled_tasks SET status = ? WHERE id = ?`, *p.Status, id); err != nil {
			return err
		}
	}
	if p.NextRun != nil {
		if _, err := s.db.Exec(`UPDATE scheduled_tasks SET next_run = ? WHERE id = ?`,
			p.NextRun.Format(time.RFC3339), id); err != nil {
			return err
		}
	}
	if p.LastRun != nil {
		if _, err := s.db.Exec(`UPDATE scheduled_tasks SET last_run = ? WHERE id = ?`,
			p.LastRun.Format(time.RFC3339), id); err != nil {
			return err
		}
	}
	if p.LastResult != nil {
		if _, err := s.db.Exec(`UPDATE scheduled_tasks SET last_result = ? WHERE id = ?`, *p.LastResult, id); err != nil {
			return err
		}
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

type TaskRun struct {
	ID       int
	TaskID   string
	Prompt   string
	RunAt    time.Time
	Duration time.Duration
	Status   string
	Result   string
	Error    string
}

func (s *Store) UnreportedRuns(groupFolder string) []TaskRun {
	rows, err := s.db.Query(`
		SELECT r.id, r.task_id, t.prompt, r.run_at, r.duration_ms, r.status, r.result, r.error
		FROM task_run_logs r
		JOIN scheduled_tasks t ON r.task_id = t.id
		WHERE t.group_folder = ? AND (r.reported IS NULL OR r.reported = 0)
		ORDER BY r.run_at ASC`, groupFolder)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []TaskRun
	for rows.Next() {
		var r TaskRun
		var runAt string
		var ms int64
		var result, errStr *string
		if err := rows.Scan(&r.ID, &r.TaskID, &r.Prompt, &runAt, &ms, &r.Status, &result, &errStr); err != nil {
			continue
		}
		r.RunAt, _ = time.Parse(time.RFC3339, runAt)
		r.Duration = time.Duration(ms) * time.Millisecond
		if result != nil {
			r.Result = *result
		}
		if errStr != nil {
			r.Error = *errStr
		}
		out = append(out, r)
	}
	return out
}

func (s *Store) MarkRunsReported(ids []int) {
	for _, id := range ids {
		s.db.Exec(`UPDATE task_run_logs SET reported = 1 WHERE id = ?`, id)
	}
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

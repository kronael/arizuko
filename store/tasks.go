package store

import (
	"database/sql"
	"strconv"
	"strings"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/core"
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
	return s.runAudited(func(tx *sql.Tx) (audit.Event, error) {
		_, err := tx.Exec(
			`INSERT INTO scheduled_tasks (`+taskCols+`)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			t.ID, t.Owner, t.ChatJID, t.Prompt, nilIfEmpty(t.Cron),
			nextRun, t.Status, t.Created.Format(time.RFC3339), cm,
		)
		return audit.Event{
			Category: audit.CategoryMutation,
			Action:   "task.create",
			Actor:    "system",
			Surface:  audit.SurfaceGateway,
			Resource: "scheduled_tasks/" + t.ID,
			Folder:   t.Owner,
			Outcome:  audit.OutcomeOK,
			ParamsSummary: map[string]any{
				"cron":         t.Cron,
				"context_mode": cm,
			},
		}, err
	})
}

const taskCols = `id, owner, chat_jid, prompt, cron, next_run, status, created_at, context_mode`

// PutTaskRow inserts one scheduled task WITHOUT emitting an audit_log row — the
// audit-free twin of CreateTask for a DB that has no audit_log table (routd.db,
// which OWNS scheduled_tasks in the split topology — spec 5/5). Same INSERT as
// CreateTask; callers that own messages.db keep using the audited CreateTask.
func (s *Store) PutTaskRow(t core.Task) error {
	var nextRun *string
	if t.NextRun != nil {
		v := t.NextRun.Format(time.RFC3339)
		nextRun = &v
	}
	cm := t.ContextMode
	if cm == "" {
		cm = "group"
	}
	_, err := s.db.Exec(
		`INSERT INTO scheduled_tasks (`+taskCols+`)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.Owner, t.ChatJID, t.Prompt, nilIfEmpty(t.Cron),
		nextRun, t.Status, t.Created.Format(time.RFC3339), cm)
	return err
}

// SetTaskStatus updates one task's status WITHOUT emitting an audit_log row —
// the audit-free path behind the agent's pause_task/resume_task tools on
// routd.db. Mirrors UpdateTask(id, TaskPatch{Status}).
func (s *Store) SetTaskStatus(id, status string) error {
	_, err := s.db.Exec(`UPDATE scheduled_tasks SET status = ? WHERE id = ?`, status, id)
	return err
}

// RemoveTask deletes one task WITHOUT emitting an audit_log row — the audit-free
// twin of DeleteTask for an audit_log-less DB (routd.db). Backs cancel_task.
func (s *Store) RemoveTask(id string) error {
	_, err := s.db.Exec(`DELETE FROM scheduled_tasks WHERE id = ?`, id)
	return err
}

// DueTasks atomically claims active tasks whose next_run has passed (marking
// them 'firing' so concurrent pollers skip them) and returns them — the read
// half of timed's fire loop, moved behind GET /v1/tasks/due. datetime()
// normalizes RFC3339 offsets to UTC so a non-UTC next_run still orders right.
func (s *Store) DueTasks(now time.Time) ([]core.Task, error) {
	nowStr := now.Format(time.RFC3339)
	if _, err := s.db.Exec(
		`UPDATE scheduled_tasks SET status = 'firing'
		 WHERE status = 'active' AND datetime(next_run) <= datetime(?)`, nowStr); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(
		`SELECT ` + taskCols + ` FROM scheduled_tasks WHERE status = 'firing'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.Task
	for rows.Next() {
		if t, ok := scanTask(rows); ok {
			out = append(out, t)
		}
	}
	return out, rows.Err()
}

// RecordTaskRun appends one task_run_logs row WITHOUT emitting an audit_log row
// — the write half of timed's fire loop, moved behind POST /v1/tasks/runlog.
// Mirrors timed.logRun (NULLIF on empty error).
func (s *Store) RecordTaskRun(l TaskRunLog) error {
	_, err := s.db.Exec(
		`INSERT INTO task_run_logs (task_id, run_at, duration_ms, status, result, error)
		 VALUES (?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''))`,
		l.TaskID, time.Now().UTC().Format(time.RFC3339), l.DurationMS, l.Status, l.Result, l.Error)
	return err
}

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
	return s.runAudited(func(tx *sql.Tx) (audit.Event, error) {
		_, err := tx.Exec(
			"UPDATE scheduled_tasks SET "+strings.Join(sets, ", ")+" WHERE id = ?", args...)
		params := map[string]any{}
		if p.Status != nil {
			params["status"] = *p.Status
		}
		if p.NextRun != nil {
			params["next_run"] = p.NextRun.Format(time.RFC3339)
		}
		return audit.Event{
			Category:      audit.CategoryMutation,
			Action:        "task.update",
			Actor:         "system",
			Surface:       audit.SurfaceGateway,
			Resource:      "scheduled_tasks/" + id,
			Outcome:       audit.OutcomeOK,
			ParamsSummary: params,
		}, err
	})
}

func (s *Store) DeleteTask(id string) error {
	return s.runAudited(func(tx *sql.Tx) (audit.Event, error) {
		_, err := tx.Exec(`DELETE FROM scheduled_tasks WHERE id = ?`, id)
		return audit.Event{
			Category: audit.CategoryMutation,
			Action:   "task.delete",
			Actor:    "system",
			Surface:  audit.SurfaceGateway,
			Resource: "scheduled_tasks/" + id,
			Outcome:  audit.OutcomeOK,
		}, err
	})
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

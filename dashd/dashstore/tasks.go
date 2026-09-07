package dashstore

import (
	"context"
	"database/sql"
	"errors"

	"github.com/kronael/arizuko/audit"
)

// ErrTaskMissing reports that no scheduled task carries that id.
// ErrTaskState reports that the task is not in the state the transition starts
// from, which is how the state machine is enforced: the starting state is a
// clause in the UPDATE, so a wrong-state task matches no row and nothing moves.
// ErrInvalidCron reports a cron expression the scheduler cannot read.
var (
	ErrTaskMissing = errors.New("task not found")
	ErrTaskState   = errors.New("invalid state transition")
	ErrInvalidCron = errors.New("invalid cron expression")
)

// PauseTask moves an active task to paused.
func (s *Store) PauseTask(ctx context.Context, id string, ev audit.Event) error {
	return s.taskTransition(ctx, ev, func(tx *sql.Tx) (sql.Result, error) {
		return tx.Exec(
			`UPDATE scheduled_tasks SET status = 'paused' WHERE id = ? AND status = 'active'`, id)
	})
}

// CancelTask moves an active or paused task to cancelled.
func (s *Store) CancelTask(ctx context.Context, id string, ev audit.Event) error {
	return s.taskTransition(ctx, ev, func(tx *sql.Tx) (sql.Result, error) {
		return tx.Exec(
			`UPDATE scheduled_tasks SET status = 'cancelled' WHERE id = ? AND status IN ('active','paused')`, id)
	})
}

// ResumeTask moves a paused task back to active and sets its next fire time.
// nextRun turns the task's own cron expression into that timestamp and reports
// false for an expression the scheduler cannot read; it is consulted inside the
// transaction that read the expression, so a task cannot resume against a cron
// line that changed underneath it. ErrTaskMissing means no such task,
// ErrInvalidCron that the stored expression no longer parses.
func (s *Store) ResumeTask(ctx context.Context, id string, nextRun func(cron string) (string, bool), ev audit.Event) error {
	return s.taskTransition(ctx, ev, func(tx *sql.Tx) (sql.Result, error) {
		var cron sql.NullString
		if err := tx.QueryRow(`SELECT cron FROM scheduled_tasks WHERE id = ?`, id).Scan(&cron); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, ErrTaskMissing
			}
			return nil, err
		}
		next, ok := nextRun(cron.String)
		if !ok {
			return nil, ErrInvalidCron
		}
		return tx.Exec(
			`UPDATE scheduled_tasks SET status = 'active', next_run = ? WHERE id = ? AND status = 'paused'`,
			next, id)
	})
}

// taskTransition runs one state move and its audit row in a single
// transaction: a crash between the two cannot leave a mutation without its
// trail. A move that matches no row reports ErrTaskState and commits nothing.
func (s *Store) taskTransition(ctx context.Context, ev audit.Event, move func(*sql.Tx) (sql.Result, error)) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := move(tx)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrTaskState
	}
	if err := audit.EmitInTx(ctx, s.obj(), tx, ev); err != nil {
		return err
	}
	return tx.Commit()
}

// NewTaskID mints an id for a task the operator is about to create. The random
// half comes from the store because that is where uniqueness is decidable.
func (s *Store) NewTaskID() (string, error) {
	var id string
	if err := s.db.QueryRow(`SELECT lower(hex(randomblob(6)))`).Scan(&id); err != nil {
		return "", err
	}
	return "t-" + id, nil
}

// NewTask is a task the operator is creating: everything the row needs that is
// not the status or the created_at stamp, both of which a new task fixes.
type NewTask struct {
	ID      string
	Owner   string
	ChatJID string
	Prompt  string
	Cron    string
	NextRun string
}

// CreateTask writes the task and its audit row in one transaction. NextRun is
// computed before the insert so the task fires without waiting for timed to
// backfill it.
func (s *Store) CreateTask(ctx context.Context, t NewTask, ev audit.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(
		`INSERT INTO scheduled_tasks (id, owner, chat_jid, prompt, cron, next_run, status, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, 'active', datetime('now'))`,
		t.ID, t.Owner, t.ChatJID, t.Prompt, t.Cron, t.NextRun); err != nil {
		return err
	}
	if err := audit.EmitInTx(ctx, s.obj(), tx, ev); err != nil {
		return err
	}
	return tx.Commit()
}

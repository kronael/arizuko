package dashstore

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// ErrTaskMissing reports that no scheduled task carries that id.
// ErrTaskState reports that the task is not in the state the transition starts
// from, which is how the state machine is enforced: the starting state is a
// condition on the write, so a wrong-state task moves nothing.
// ErrInvalidCron reports a cron expression the scheduler cannot read.
var (
	ErrTaskMissing = errors.New("task not found")
	ErrTaskState   = errors.New("invalid state transition")
	ErrInvalidCron = errors.New("invalid cron expression")
)

// PauseTask moves an active task to paused.
func (s *Store) PauseTask(ctx context.Context, id string, ev audit.Event) error {
	return s.taskTransition(ctx, ev, func(ws *store.WriteSet) error {
		t, err := s.taskInState(id, core.TaskActive)
		if err != nil {
			return err
		}
		return s.st().MoveTaskInto(ws, t, core.TaskPaused, nil, ErrTaskState)
	})
}

// CancelTask moves an active or paused task to cancelled.
func (s *Store) CancelTask(ctx context.Context, id string, ev audit.Event) error {
	return s.taskTransition(ctx, ev, func(ws *store.WriteSet) error {
		t, err := s.taskInState(id, core.TaskActive, core.TaskPaused)
		if err != nil {
			return err
		}
		return s.st().MoveTaskInto(ws, t, "cancelled", nil, ErrTaskState)
	})
}

// ResumeTask moves a paused task back to active and sets its next fire time.
// nextRun turns the task's own cron expression into that timestamp and reports
// false for an expression the scheduler cannot read; the move is CONDITIONED on
// that expression still being the task's, so a task cannot resume against a
// cron line that changed underneath it. ErrTaskMissing means no such task,
// ErrInvalidCron that the stored expression does not parse.
func (s *Store) ResumeTask(ctx context.Context, id string, nextRun func(cron string) (string, bool), ev audit.Event) error {
	return s.taskTransition(ctx, ev, func(ws *store.WriteSet) error {
		t, ok, err := s.st().TaskByID(id)
		if err != nil {
			return err
		}
		if !ok {
			return ErrTaskMissing
		}
		next, ok := nextRun(t.Cron)
		if !ok {
			return ErrInvalidCron
		}
		if t.Status != core.TaskPaused {
			return ErrTaskState
		}
		return s.st().MoveTaskInto(ws, t, core.TaskActive, &next, ErrTaskState)
	})
}

// taskInState reads the task a transition starts from and refuses one that is
// not in a state the transition leaves. A task that is not there is the same
// answer as one in the wrong state, which is what a conditional UPDATE matching
// no row said.
func (s *Store) taskInState(id string, from ...string) (core.Task, error) {
	t, ok, err := s.st().TaskByID(id)
	if err != nil {
		return core.Task{}, err
	}
	if !ok || !slices.Contains(from, t.Status) {
		return core.Task{}, ErrTaskState
	}
	return t, nil
}

// taskTransition sends one state move and its audit row in a single batch: a
// crash between the two cannot leave a mutation without its trail. A move whose
// condition no longer holds writes nothing and reports ErrTaskState — the task
// moved under the caller between the read and the write.
func (s *Store) taskTransition(ctx context.Context, ev audit.Event, move func(*store.WriteSet) error) error {
	ws := &store.WriteSet{}
	if err := move(ws); err != nil {
		return err
	}
	_, rec := audit.WriteFor(ev)
	return s.st().Apply(ctx, ws, rec)
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

// CreateTask writes the task and its audit row in one batch. NextRun is
// computed before the write so the task fires without waiting for timed to
// backfill it, and it arrives as RFC3339 text in the operator's own timezone —
// which is the layout `next_run` carries, so the parse here round-trips the
// caller's own bytes rather than restating them.
func (s *Store) CreateTask(ctx context.Context, t NewTask, ev audit.Event) error {
	task := core.Task{
		ID:      t.ID,
		Owner:   t.Owner,
		ChatJID: t.ChatJID,
		Prompt:  t.Prompt,
		Cron:    t.Cron,
		Status:  core.TaskActive,
		Created: time.Now(),
	}
	if t.NextRun != "" {
		next, err := time.Parse(time.RFC3339, t.NextRun)
		if err != nil {
			return fmt.Errorf("task %s next_run %q: %w", t.ID, t.NextRun, err)
		}
		task.NextRun = &next
	}

	ws := &store.WriteSet{}
	if err := s.st().CreateTaskInto(ws, task,
		fmt.Errorf("scheduled task %q already exists", t.ID)); err != nil {
		return err
	}
	_, rec := audit.WriteFor(ev)
	return s.st().Apply(ctx, ws, rec)
}

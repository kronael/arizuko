package dashstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
var (
	ErrTaskMissing = errors.New("task not found")
	ErrTaskState   = errors.New("invalid state transition")
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

// ResumeTask moves a paused task back to active and clears its next fire time:
// the scheduler fills the next instant of the task's cron on its next poll. The
// move is CONDITIONED on the cron still being the one read, so a task cannot
// resume against a cron line that changed underneath it. ErrTaskMissing means
// no such task.
func (s *Store) ResumeTask(ctx context.Context, id string, ev audit.Event) error {
	return s.taskTransition(ctx, ev, func(ws *store.WriteSet) error {
		t, ok, err := s.st().TaskByID(id)
		if err != nil {
			return err
		}
		if !ok {
			return ErrTaskMissing
		}
		if t.Status != core.TaskPaused {
			return ErrTaskState
		}
		cleared := ""
		return s.st().MoveTaskInto(ws, t, core.TaskActive, &cleared, ErrTaskState)
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

// NewTaskID mints an id for a task the operator is about to create. Six random
// bytes, so a collision is what the create's own condition refuses rather than
// something this has to rule out.
func (s *Store) NewTaskID() (string, error) {
	var raw [6]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "t-" + hex.EncodeToString(raw[:]), nil
}

// NewTask is a task the operator is creating: everything the row needs that is
// not the status or the created_at stamp, both of which a new task fixes.
type NewTask struct {
	ID      string
	Owner   string
	ChatJID string
	Prompt  string
	Cron    string
}

// CreateTask writes the task and its audit row in one batch. The task carries
// no next_run: the scheduler fills it from the cron.
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

	ws := &store.WriteSet{}
	if err := s.st().CreateTaskInto(ws, task,
		fmt.Errorf("scheduled task %q already exists", t.ID)); err != nil {
		return err
	}
	_, rec := audit.WriteFor(ev)
	return s.st().Apply(ctx, ws, rec)
}

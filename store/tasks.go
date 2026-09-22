package store

// tasks.go holds every read and write of `scheduled_tasks`: one item per
// scheduled prompt, addressed by its id.
//
// The partition is OwnerRoutd because resreg's entity path writes these items
// too (resreg.SubsystemRoutd IS store.OwnerRoutd); a second spelling selects
// nothing, silently. The id is the coordinate, the handle every other surface
// holds; every other read scans the partition and filters (the same shape as
// route_tokens, 43 items on the largest live instance).
//
// TWO INSTANTS, TWO LAYOUTS. `created_at` is a StampedField, core.TS at 30
// bytes, the width resreg's entity path stamps. `next_run` is NOT stamped: it
// is RFC3339 text in the operator's own timezone, stored verbatim, and the due
// comparison is made on the PARSED instant, never on the bytes.
//
// A CLAIM IS ONE WRITE PER TASK: a batch of N conditional claims would be
// REFUSED whole the moment a concurrent poller took one, and this poller would
// fire nothing.

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// The attributes a task item carries. `id` is the coordinate.
const (
	attrTaskOwner       = "owner"
	attrTaskChatJID     = "chat_jid"
	attrTaskPrompt      = "prompt"
	attrTaskCron        = "cron"
	attrTaskNextRun     = "next_run"
	attrTaskStatus      = "status"
	attrTaskCreatedAt   = "created_at"
	attrTaskContextMode = "context_mode"
)

// TaskContextGroup is the context_mode a task runs under unless it says
// otherwise: the default resreg's own BeforeInsert hook fills
// (resreg/resources/scheduled_tasks.go), so the two writers choose the same
// one.
const TaskContextGroup = "group"

// TaskStatusFiring marks a task one poller has claimed. It is a transient
// state: the fire loop moves it back to active or paused, and
// RecoverFiringTasks re-arms whatever a crash left in it.
const TaskStatusFiring = "firing"

type TaskPatch struct {
	Status  *string
	NextRun *time.Time
}

// taskKey is the coordinate of one task.
func taskKey(id string) (objstore.Key, error) {
	if id == "" {
		return objstore.Key{}, fmt.Errorf("scheduled task carries no id, which is its coordinate")
	}
	return objstore.Key{Partition: OwnerRoutd, Sort: id}, nil
}

// taskFrom reads one item back. context_mode falls back to the insert's
// default, so a row written before the field existed still names one of the
// two modes the fire loop reads.
func taskFrom(it objstore.Item) core.Task {
	t := core.Task{
		ID:          it.Key.Sort,
		Owner:       it.Attrs[attrTaskOwner],
		ChatJID:     it.Attrs[attrTaskChatJID],
		Prompt:      it.Attrs[attrTaskPrompt],
		Cron:        it.Attrs[attrTaskCron],
		Status:      it.Attrs[attrTaskStatus],
		ContextMode: it.Attrs[attrTaskContextMode],
	}
	t.Created, _ = time.Parse(core.TSLayout, it.Attrs[attrTaskCreatedAt])
	if run, ok := taskNextRun(it); ok {
		t.NextRun = &run
	}
	if t.ContextMode == "" {
		t.ContextMode = TaskContextGroup
	}
	return t
}

// taskNextRun parses `next_run`, and answers false for a task that carries
// none: a one-shot that has fired. RFC3339 also reads the fixed-width form, so
// one parse serves both writers.
func taskNextRun(it objstore.Item) (time.Time, bool) {
	raw := it.Attrs[attrTaskNextRun]
	if raw == "" {
		return time.Time{}, false
	}
	run, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, false
	}
	return run, true
}

// taskAttrs renders the attributes one task carries. EVERY EMPTY STRING IS
// LEFT OFF, what resreg's own entity path does (resreg/entity.go itemFor).
// `context_mode` is the exception: the resource's BeforeInsert hook fills it,
// so both writers store the default rather than nothing.
func taskAttrs(t core.Task) map[string]string {
	created := t.Created
	if created.IsZero() {
		created = time.Now()
	}
	mode := t.ContextMode
	if mode == "" {
		mode = TaskContextGroup
	}
	attrs := map[string]string{
		attrTaskCreatedAt:   core.TS(created),
		attrTaskContextMode: mode,
	}
	setAttr(attrs, attrTaskOwner, t.Owner)
	setAttr(attrs, attrTaskChatJID, t.ChatJID)
	setAttr(attrs, attrTaskPrompt, t.Prompt)
	setAttr(attrs, attrTaskStatus, t.Status)
	setAttr(attrs, attrTaskCron, t.Cron)
	if t.NextRun != nil {
		attrs[attrTaskNextRun] = t.NextRun.Format(time.RFC3339)
	}
	return attrs
}

// allTasks is the whole table, in coordinate order.
func (s *Store) allTasks(ctx context.Context) ([]objstore.Item, error) {
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: ScheduledTasks, Partition: OwnerRoutd,
	})
	if err != nil {
		return nil, fmt.Errorf("read scheduled tasks: %w", err)
	}
	return items, nil
}

// CreateTask inserts one scheduled task: the ONE insert store owns. It emits
// no audit row, because dashd's form and routd's `/v1` resource each emit
// their own. A refusal is an ERROR: the id is a caller-chosen handle, and a
// create that silently replaced one would take over a live schedule's prompt
// and cron under the operator who owns it.
func (s *Store) CreateTask(t core.Task) error {
	w, err := taskCreate(t)
	if err != nil {
		return err
	}
	committed, err := s.TryWriteEntities(context.Background(), nil, w)
	if err != nil {
		return err
	}
	return taskCreateRefusal(committed, t.ID)
}

// taskCreate renders the create-if-absent write both creators issue.
func taskCreate(t core.Task) (objstore.Write, error) {
	key, err := taskKey(t.ID)
	if err != nil {
		return objstore.Write{}, err
	}
	return objstore.Write{
		Op: objstore.Put, Entity: ScheduledTasks, Key: key, Attrs: taskAttrs(t),
		Cond: objstore.Cond{Item: objstore.ItemAbsent},
	}, nil
}

// taskCreateRefusal names what a refused create means: the id is taken.
func taskCreateRefusal(committed bool, id string) error {
	if committed {
		return nil
	}
	return fmt.Errorf("scheduled task %q already exists", id)
}

// SetTaskStatus updates one task's status. It exists for the status half of
// the DueTasks contract (a paused task leaves the due set, a resumed one
// returns); the agent's pause and resume reach routd's `/v1` resource. A
// missing task is an ERROR: it has not been paused.
func (s *Store) SetTaskStatus(id, status string) error {
	key, err := taskKey(id)
	if err != nil {
		return err
	}
	err = s.WriteEntities(context.Background(), nil, objstore.Write{
		Op: objstore.Update, Entity: ScheduledTasks, Key: key,
		Attrs: map[string]string{attrTaskStatus: status},
	})
	return err
}

// RescheduleTask sets a fired task's next_run + status WITHOUT an audit_log
// row: the write half of timed's fire loop, behind POST
// /v1/tasks/{id}/reschedule. An empty nextRun TAKES THE ATTRIBUTE AWAY
// (one-shot completion); a non-empty value is stored verbatim.
func (s *Store) RescheduleTask(id, nextRun, status string) error {
	key, err := taskKey(id)
	if err != nil {
		return err
	}
	w := objstore.Write{
		Op: objstore.Update, Entity: ScheduledTasks, Key: key,
		Attrs: map[string]string{attrTaskStatus: status},
	}
	if nextRun == "" {
		w.Remove = []string{attrTaskNextRun}
	} else {
		w.Attrs[attrTaskNextRun] = nextRun
	}
	err = s.WriteEntities(context.Background(), nil, w)
	return err
}

// attrIs compares one attribute against the value a caller READ off the item.
// An empty value is ABSENT and not the empty string: an attribute a writer left
// off answers no Eq, so a guard spelled Eq "" would refuse every row it was
// meant to let through.
func attrIs(name, value string) objstore.Cmp {
	if value == "" {
		return objstore.Cmp{Attr: name, Op: objstore.Absent}
	}
	return objstore.Cmp{Attr: name, Op: objstore.Eq, Value: value}
}

// MoveTaskInto buffers the move of one task into `to` on the caller's write
// set: the three state transitions dashd's operator pages make, each riding
// one batch with its own audit row.
//
// `t` is the task the caller READ, and the write is CONDITIONED on the status
// the transition starts from and the cron a next_run was computed from: a task
// another surface moved in between answers false rather than being moved
// twice, and a task cannot resume against a cron line that changed underneath
// it. nextRun nil leaves the fire time alone; a non-nil empty one TAKES THE
// ATTRIBUTE AWAY. Which statuses a task may move FROM is the caller's state
// machine; refused is what the CONDITION failing means.
func (s *Store) MoveTaskInto(ws *WriteSet, t core.Task, to string,
	nextRun *string, refused error) error {
	key, err := taskKey(t.ID)
	if err != nil {
		return err
	}
	w := objstore.Write{
		Op: objstore.Update, Entity: ScheduledTasks, Key: key,
		Attrs: map[string]string{attrTaskStatus: to},
		Cond: objstore.Cond{Where: []objstore.Cmp{
			attrIs(attrTaskStatus, t.Status),
			attrIs(attrTaskCron, t.Cron),
		}},
	}
	if nextRun != nil {
		if *nextRun == "" {
			w.Remove = []string{attrTaskNextRun}
		} else {
			w.Attrs[attrTaskNextRun] = *nextRun
		}
	}
	return ws.AddGuarded(refused, w)
}

// RecoverFiringTasks re-arms tasks stranded in 'firing' by a crash between
// claim and reschedule: the poll only claims 'active' rows, so a stuck task
// never fires again (main/trading's daily compaction stayed stranded for days
// after a restart storm). Call ONLY at startup: nothing is legitimately
// mid-fire then, which is what makes the unconditional write safe.
func (s *Store) RecoverFiringTasks() (int64, error) {
	ctx := context.Background()
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: ScheduledTasks, Partition: OwnerRoutd,
		Where: []objstore.Cmp{{Attr: attrTaskStatus, Op: objstore.Eq, Value: TaskStatusFiring}},
	})
	if err != nil {
		return 0, fmt.Errorf("read firing tasks: %w", err)
	}
	writes := make([]objstore.Write, 0, len(items))
	for _, it := range items {
		writes = append(writes, objstore.Write{
			Op: objstore.Update, Entity: ScheduledTasks, Key: it.Key,
			Attrs: map[string]string{attrTaskStatus: core.TaskActive},
		})
	}
	if len(writes) == 0 {
		return 0, nil
	}
	n, err := s.applyInChunks(ctx, "recover firing tasks", writes)
	return int64(n), err
}

// DueTasks claims active tasks whose next_run has passed, marking them
// 'firing' so concurrent pollers skip them, and returns them: the read half of
// timed's fire loop, behind GET /v1/tasks/due. The claim is per task and
// CONDITIONAL on the status still being 'active', so a task another poller
// took in between is dropped from the answer rather than fired twice.
func (s *Store) DueTasks(now time.Time) ([]core.Task, error) {
	ctx := context.Background()
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: ScheduledTasks, Partition: OwnerRoutd,
		Where: []objstore.Cmp{{Attr: attrTaskStatus, Op: objstore.Eq, Value: core.TaskActive}},
	})
	if err != nil {
		return nil, fmt.Errorf("read due tasks: %w", err)
	}
	var out []core.Task
	for _, it := range items {
		run, ok := taskNextRun(it)
		if !ok || run.After(now) {
			continue
		}
		claimed, err := s.TryWriteEntities(ctx, nil, objstore.Write{
			Op: objstore.Update, Entity: ScheduledTasks, Key: it.Key,
			Attrs: map[string]string{attrTaskStatus: TaskStatusFiring},
			Cond: objstore.Cond{Where: []objstore.Cmp{
				{Attr: attrTaskStatus, Op: objstore.Eq, Value: core.TaskActive},
			}},
		})
		if err != nil {
			return nil, fmt.Errorf("claim task %q: %w", it.Key.Sort, err)
		}
		if !claimed {
			continue
		}
		t := taskFrom(it)
		t.Status = TaskStatusFiring
		out = append(out, t)
	}
	return out, nil
}

func (s *Store) GetTask(id string) (core.Task, bool) {
	t, ok, err := s.TaskByID(id)
	if err != nil {
		return core.Task{}, false
	}
	return t, ok
}

// TaskByID answers the task one id names, and SEPARATES "no such task" from
// "the partition could not be read": an operator told "no such task" about
// one that still fires goes and creates a second.
func (s *Store) TaskByID(id string) (core.Task, bool, error) {
	key, err := taskKey(id)
	if err != nil {
		return core.Task{}, false, err
	}
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: ScheduledTasks, Partition: OwnerRoutd,
		Key: objstore.KeyPredicate{Op: objstore.ExactKey, Value: key.Sort},
	})
	if err != nil {
		return core.Task{}, false, fmt.Errorf("read task %q: %w", id, err)
	}
	if len(items) == 0 {
		return core.Task{}, false, nil
	}
	return taskFrom(items[0]), true, nil
}

// ListTasks returns one owner's tasks, newest first, or every task for a root
// caller. `owner` is an attribute, so this reads the partition and filters;
// `created_at` DESC is applied after the read.
func (s *Store) ListTasks(folder string, isRoot bool) []core.Task {
	owner := folder
	if isRoot {
		owner = ""
	}
	items, err := s.allTasks(context.Background())
	if err != nil {
		return nil
	}
	var out []core.Task
	for _, it := range items {
		if owner != "" && it.Attrs[attrTaskOwner] != owner {
			continue
		}
		out = append(out, taskFrom(it))
	}
	// Stable, so two tasks stamped in the same instant keep coordinate order.
	slices.SortStableFunc(out, func(a, b core.Task) int { return b.Created.Compare(a.Created) })
	return out
}

func (s *Store) UpdateTask(id string, p TaskPatch) error {
	key, err := taskKey(id)
	if err != nil {
		return err
	}
	attrs := map[string]string{}
	params := map[string]any{}
	if p.Status != nil {
		attrs[attrTaskStatus] = *p.Status
		params["status"] = *p.Status
	}
	if p.NextRun != nil {
		attrs[attrTaskNextRun] = p.NextRun.Format(time.RFC3339)
		params["next_run"] = attrs[attrTaskNextRun]
	}
	if len(attrs) == 0 {
		return nil
	}
	return s.writeAudited(func() ([]objstore.Write, audit.Event, error) {
		w := objstore.Write{
			Op: objstore.Update, Entity: ScheduledTasks, Key: key, Attrs: attrs,
		}
		return []objstore.Write{w}, audit.Event{
			Category:      audit.CategoryMutation,
			Action:        "task.update",
			Actor:         "system",
			Surface:       audit.SurfaceGateway,
			Resource:      "scheduled_tasks/" + id,
			Outcome:       audit.OutcomeOK,
			ParamsSummary: params,
		}, nil
	})
}

func (s *Store) DeleteTask(id string) error {
	key, err := taskKey(id)
	if err != nil {
		return err
	}
	return s.writeAudited(func() ([]objstore.Write, audit.Event, error) {
		w := objstore.Write{Op: objstore.Delete, Entity: ScheduledTasks, Key: key}
		return []objstore.Write{w}, audit.Event{
			Category: audit.CategoryMutation,
			Action:   "task.delete",
			Actor:    "system",
			Surface:  audit.SurfaceGateway,
			Resource: "scheduled_tasks/" + id,
			Outcome:  audit.OutcomeOK,
		}, nil
	})
}

func (s *Store) CountActiveTasks() int {
	items, err := s.Obj().Query(context.Background(), objstore.Query{
		Entity: ScheduledTasks, Partition: OwnerRoutd,
		Where: []objstore.Cmp{{Attr: attrTaskStatus, Op: objstore.Eq, Value: core.TaskActive}},
	})
	if err != nil {
		return 0
	}
	return len(items)
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
// Idempotent: each write is create-if-absent, so a task an operator has since
// edited survives a re-seed. One write per task rather than one batch: a batch
// is all-or-nothing, so a single task already there would refuse the four that
// are not.
func (s *Store) SeedDefaultTasks(folder, chatJID string) error {
	ctx := context.Background()
	now := time.Now()
	tz := core.Timezone()
	for i, t := range defaultTasks {
		id := folder + "-mem-" + strconv.Itoa(i)
		// The first run is the first instant the task's OWN cron names: seeding
		// it at `now` left five tasks due, and a brand-new group paid five LLM
		// turns before anyone sent it a message (BUGS L312).
		next, err := core.NextRun(t.cron, tz)
		if err != nil {
			return fmt.Errorf("seed task %s: cron %q: %w", id, t.cron, err)
		}
		task := core.Task{
			ID:          id,
			Owner:       folder,
			ChatJID:     chatJID,
			Prompt:      t.prompt,
			Cron:        t.cron,
			NextRun:     &next,
			Status:      core.TaskActive,
			Created:     now,
			ContextMode: "isolated",
		}
		key, err := taskKey(task.ID)
		if err != nil {
			return err
		}
		// A refusal is the task already being seeded: the operator's own edits
		// to it must survive the next start.
		if _, err := s.TryWriteEntities(ctx, nil, objstore.Write{
			Op: objstore.Put, Entity: ScheduledTasks, Key: key, Attrs: taskAttrs(task),
			Cond: objstore.Cond{Item: objstore.ItemAbsent},
		}); err != nil {
			return fmt.Errorf("seed task %s: %w", task.ID, err)
		}
	}
	return nil
}

// The four writers below buffer their mutation on the caller's write set, for
// routd's `/v1/tasks` face: the mutation and the one audit_log row that records
// it ride ONE batch, where the audited twins would record one act twice.

// CreateTaskInto buffers one scheduled task's creation: CreateTask's write-set
// twin, refusing a taken id for the same reason.
func (s *Store) CreateTaskInto(ws *WriteSet, t core.Task, refused error) error {
	w, err := taskCreate(t)
	if err != nil {
		return err
	}
	return ws.AddGuarded(refused, w)
}

// SetTaskStatusInto buffers one task's status flip (pause and resume). An
// Update writes nothing when the task is missing, and refused is what that
// means.
func (s *Store) SetTaskStatusInto(ws *WriteSet, id, status string, refused error) error {
	key, err := taskKey(id)
	if err != nil {
		return err
	}
	return ws.AddGuarded(refused, objstore.Write{
		Op: objstore.Update, Entity: ScheduledTasks, Key: key,
		Attrs: map[string]string{attrTaskStatus: status},
	})
}

// PatchTaskInto buffers the REST PATCH partial update: status and/or next_run,
// at least one non-empty. `next_run` is stored VERBATIM; the operator hands it
// in and the read parses it as RFC3339 (file header). A patch naming no field
// buffers nothing.
func (s *Store) PatchTaskInto(ws *WriteSet, id, status, nextRun string, refused error) error {
	key, err := taskKey(id)
	if err != nil {
		return err
	}
	attrs := map[string]string{}
	setAttr(attrs, attrTaskStatus, status)
	setAttr(attrs, attrTaskNextRun, nextRun)
	if len(attrs) == 0 {
		return nil
	}
	return ws.AddGuarded(refused, objstore.Write{
		Op: objstore.Update, Entity: ScheduledTasks, Key: key, Attrs: attrs,
	})
}

// DeleteTaskInto buffers one task's removal (cancel). Unconditional: asking
// twice is the same answer.
func (s *Store) DeleteTaskInto(ws *WriteSet, id string) error {
	key, err := taskKey(id)
	if err != nil {
		return err
	}
	ws.Add(objstore.Write{Op: objstore.Delete, Entity: ScheduledTasks, Key: key})
	return nil
}

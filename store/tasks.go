package store

// tasks.go holds every read and write of `scheduled_tasks`: one item per
// scheduled prompt, addressed by its id.
//
// THE PARTITION IS OwnerRoutd AND IT IS NOT A CHOICE THIS FILE MAKES.
// `scheduled_tasks` is a registered resreg resource, and an entity-backed
// resource's items live at its owner subsystem (resreg.SubsystemRoutd, which IS
// store.OwnerRoutd). A second spelling would put this file's items in one
// partition and the engine's read in another, and the failure is silent: a
// query on a partition nobody wrote to is a legal query that selects nothing.
//
// THE ID IS THE COORDINATE, and it is the handle every other surface already
// holds: pause_task, resume_task, cancel_task and PATCH /v1/tasks/{taskId} all
// name a task by it, and task_run_logs.task_id refers to it from a table that
// has not moved.
//
// EVERY OTHER READ IS A PARTITION SCAN, and that is measured rather than
// overlooked. `owner`, `status` and `next_run` are attributes, and an attribute
// is not a leading fragment of the key, so listing a folder's tasks, counting
// the active ones and claiming the due ones each read the partition and filter.
// It is the same shape as `route_tokens`, whose partition is 43 items on the
// largest live instance, and the fire loop runs once a tick.
//
// TWO INSTANTS, TWO LAYOUTS, AND THE SPLIT IS NOT AN ACCIDENT. `created_at` is
// a StampedField, so it is core.TS at 30 bytes — the width resreg's own entity
// path stamps one at (resreg/entity.go itemFor), and the two writers of these
// items have to agree byte for byte. `next_run` is NOT stamped: it is the instant
// core.NextRun reads out of the task's cron, written here as RFC3339 text in the
// operator's own timezone, and handed to RescheduleTask the same way by timed's
// fire loop, stored verbatim. So the read parses it as RFC3339, which accepts
// both that and the fixed-width form, and the due comparison is made on the
// PARSED instant — never on the bytes. That is what `datetime()` gave: a non-UTC
// offset still orders right.
//
// A CLAIM IS ONE WRITE PER TASK, because a batch is all-or-nothing. `UPDATE …
// SET status='firing' WHERE status='active'` skipped exactly the rows a
// concurrent poller had already taken; a batch of N conditional claims would
// instead be REFUSED whole the moment one of them was taken, and this poller
// would fire nothing. One conditional write each keeps the per-task answer.

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

// The attributes a task item carries. `id` is NOT among them: it is the
// coordinate, and storing it twice is two facts that can disagree.
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
// otherwise. It is the default resreg's own BeforeInsert hook fills
// (resreg/resources/scheduled_tasks.go), and the two writers have to choose the
// same one: a task with no mode is not one of the two answers the fire loop
// reads.
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

// taskFrom reads one item back. context_mode falls back to the same default the
// insert fills, so a row written before the field existed still names one of the
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

// taskNextRun parses `next_run`, and answers false for a task that carries none
// — a one-shot that has fired. RFC3339 is the layout because timed writes it
// (see the file header); it also reads the fixed-width form, so one parse serves
// both writers.
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

// taskAttrs renders the attributes one task carries.
//
// EVERY EMPTY STRING IS LEFT OFF, which is what resreg's own entity path does
// with one (resreg/entity.go itemFor) and therefore what the two writers of
// these items have to agree on. Absent is the only null this model has, and it
// is exactly what the nullable `cron` and `next_run` columns held for a one-shot
// task and for one that has fired. `context_mode` is the exception because the
// resource's own BeforeInsert hook fills it, so both writers store the default
// rather than nothing.
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

// CreateTask inserts one scheduled task. It is the ONE insert store owns.
// Audit belongs to the writer that a person or an agent actually reaches —
// dashd's form and routd's `/v1` resource each emit their own row on their own
// transaction — so this path emits none rather than a second, different record
// of one act.
//
// The write is create-if-absent and a refusal is an ERROR, because the id is a
// caller-chosen handle: `arizuko task add` and the agent's create both name it,
// and a create that silently replaced one would take over a live schedule's
// prompt and cron under the operator who owns it.
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

// taskCreateRefusal names what a refused create means: the id is taken. It is
// the only thing that can refuse a create-if-absent.
func taskCreateRefusal(committed bool, id string) error {
	if committed {
		return nil
	}
	return fmt.Errorf("scheduled task %q already exists", id)
}

// SetTaskStatus updates one task's status. The agent's pause and resume reach
// routd's `/v1` resource, not this; what this exists for is the status half of
// the DueTasks contract — a paused task must leave the due set and a resumed
// one must return to it — which is asserted against DueTasks, the live reader.
//
// An Update writes nothing when the task is missing, and that refusal is an
// ERROR: the caller names a task by id, and a task that is not there has not
// been paused.
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

// RescheduleTask sets a fired task's next_run + status WITHOUT emitting an
// audit_log row — the audit-free path behind POST /v1/tasks/{id}/reschedule,
// the write half of timed's fire loop. An empty nextRun TAKES THE ATTRIBUTE
// AWAY (one-shot completion), because absent is the only null this model has and
// an empty string is a value a presence test still finds; a non-empty value is
// stored verbatim.
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
// An empty value is ABSENT and not the empty string, because absent is the only
// null this model has: an attribute a writer left off answers no Eq, so a guard
// spelled Eq "" would refuse every row it was meant to let through.
func attrIs(name, value string) objstore.Cmp {
	if value == "" {
		return objstore.Cmp{Attr: name, Op: objstore.Absent}
	}
	return objstore.Cmp{Attr: name, Op: objstore.Eq, Value: value}
}

// MoveTaskInto buffers the move of one task into `to` on the caller's write set
// — the three state transitions dashd's operator pages make, each riding one
// batch with its own audit row.
//
// `t` is the task the caller READ, and the write is CONDITIONED on the two
// fields that decision rests on still holding: the status the transition starts
// from, and the cron a next_run was computed from. The first is what `UPDATE …
// WHERE id = ? AND status = 'active'` gave — a task another surface moved in
// between answers false rather than being moved twice. The second is the
// guarantee dashd's resume asked its transaction for: a task cannot resume
// against a cron line that changed underneath it.
//
// nextRun is nil for a move that does not touch the fire time. A non-nil empty
// one TAKES THE ATTRIBUTE AWAY, the way RescheduleTask does, because an empty
// string is a value a presence test still finds.
//
// Which statuses a task may move FROM is the caller's state machine and not
// this method's: dashd refuses the transition before it asks, so the operator
// is told "invalid state transition" rather than "somebody else moved it".
// refused is what the CONDITION failing means — the task moved under the caller
// between the read and the write.
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

// RecoverFiringTasks re-arms tasks stranded in 'firing' by a crash/restart
// between claim and reschedule — the poll only claims 'active' rows, so a stuck
// task never fires again (stranded main/trading's daily compaction for days
// after a restart storm). Call ONLY at startup: nothing is legitimately
// mid-fire then, which is what makes the unconditional write safe. Returns the
// count recovered.
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

// DueTasks claims active tasks whose next_run has passed, marking them 'firing'
// so concurrent pollers skip them, and returns them — the read half of timed's
// fire loop, behind GET /v1/tasks/due.
//
// The claim is per task and CONDITIONAL on the status still being 'active', so
// a task another poller took between this read and this write is dropped from
// the answer rather than fired twice. See the file header on why that is not one
// batch.
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
// "the partition could not be read" — the distinction dashd's transitions gate
// on, because an operator told "no such task" about one that still fires goes
// and creates a second.
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
// caller. `owner` is an attribute, so this reads the partition and filters —
// and the order is `created_at` DESC, applied after the read because the
// coordinate is the id.
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
	// Stable, so two tasks stamped in the same instant keep coordinate order
	// rather than an order that changes between reads of one unchanged table.
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
// Idempotent — each write is create-if-absent, so re-seeding a group leaves the
// task an operator has since edited alone. New groups skip ant migration 055.
//
// One write per task rather than one batch of five: a batch is all-or-nothing,
// so a single task that is already there would refuse the four that are not,
// which is not what `INSERT OR IGNORE` per row said.
func (s *Store) SeedDefaultTasks(folder, chatJID string) error {
	ctx := context.Background()
	now := time.Now()
	tz := core.Timezone()
	for i, t := range defaultTasks {
		id := folder + "-mem-" + strconv.Itoa(i)
		// The first run is the first instant the task's OWN cron names. Seeding
		// it at `now` left five tasks already due, so timed's next sweep fired
		// all five and a brand-new group paid five LLM turns on its root topic
		// before anyone sent it a message (BUGS L312).
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
		// A refusal is the task already being seeded, which is what a seed means:
		// the operator's own edits to it must survive the next start.
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
// it ride ONE batch. Calling the audited twins there would send a batch of their
// own and record one act twice.

// CreateTaskInto buffers one scheduled task's creation. It is CreateTask's
// write-set twin and refuses a taken id for the same reason.
func (s *Store) CreateTaskInto(ws *WriteSet, t core.Task, refused error) error {
	w, err := taskCreate(t)
	if err != nil {
		return err
	}
	return ws.AddGuarded(refused, w)
}

// SetTaskStatusInto buffers one task's status flip — pause and resume. An Update
// writes nothing when the task is missing, and refused is what that means:
// `/v1/tasks/{id}` names a task the caller believes in.
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
// at least one non-empty. `next_run` is stored VERBATIM, because this package
// does not decide its layout — the operator hands it in and the read parses it
// as RFC3339 (see the file header). A patch naming no field buffers nothing.
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

// DeleteTaskInto buffers one task's removal — cancel. Unconditional, which is
// what a delete means: asking twice is the same answer.
func (s *Store) DeleteTaskInto(ws *WriteSet, id string) error {
	key, err := taskKey(id)
	if err != nil {
		return err
	}
	ws.Add(objstore.Write{Op: objstore.Delete, Entity: ScheduledTasks, Key: key})
	return nil
}

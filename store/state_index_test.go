package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

type stateReadCounter struct {
	objstore.Store
	rows int
}

func (c *stateReadCounter) Query(ctx context.Context, q objstore.Query) ([]objstore.Item, error) {
	measured := q
	measured.Where, measured.Project = nil, nil
	if len(q.Where) > 0 {
		measured.Limit = 0
	}
	items, err := c.Store.Query(ctx, measured)
	if err != nil {
		return nil, err
	}
	c.rows += len(items)
	return c.Store.Query(ctx, q)
}

func TestDueTasksReadOnlyDueRows(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	now := time.Now()
	for i := range 40 {
		due := now.Add(time.Hour)
		if i == 0 {
			due = now.Add(-time.Minute)
		}
		if err := s.CreateTask(core.Task{ID: fmt.Sprint(i), Owner: "alice", Status: core.TaskActive, NextRun: &due}); err != nil {
			t.Fatal(err)
		}
	}
	c := &stateReadCounter{Store: s.Obj()}
	reader := NewOver(c)
	tasks, err := reader.DueTasks(now)
	if err != nil || len(tasks) != 1 || tasks[0].ID != "0" {
		t.Fatalf("due tasks = %+v, %v", tasks, err)
	}
	if c.rows > 4 {
		t.Fatalf("one due task read %d rows; want at most 4", c.rows)
	}
	c.rows = 0
	if tasks, err := reader.DueTasks(now); err != nil || len(tasks) != 0 || c.rows != 0 {
		t.Fatalf("claimed task still due: %v, %v, rows=%d", tasks, err, c.rows)
	}
}

func TestStateIndexMigrationRunsOnce(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	ctx := context.Background()
	due := time.Now().Add(-time.Minute)
	w, err := taskCreate(core.Task{ID: "legacy", Status: core.TaskActive, NextRun: &due})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.obj.GuardedBatch(ctx, nil, []objstore.Write{w}); err != nil || !ok {
		t.Fatalf("seed legacy: %v %v", ok, err)
	}
	if err := s.MigrateStateIndexes(ctx); err != nil {
		t.Fatal(err)
	}
	tasks, err := s.DueTasks(time.Now())
	if err != nil || len(tasks) != 1 || tasks[0].ID != "legacy" {
		t.Fatalf("migration lost work: %v %v", tasks, err)
	}
	c := &stateReadCounter{Store: s.obj}
	if err := NewOver(c).MigrateStateIndexes(ctx); err != nil || c.rows != 5 {
		t.Fatalf("second migration: %v, read %d rows", err, c.rows)
	}
}

type stateRace struct {
	objstore.Store
	other *Store
	fired bool
	attrs map[string]string
}

func (r *stateRace) GuardedBatch(ctx context.Context, checks []objstore.Check, writes []objstore.Write) (bool, error) {
	if !r.fired {
		r.fired = true
		attrs := r.attrs
		if attrs == nil {
			attrs = map[string]string{attrTaskStatus: core.TaskPaused}
		}
		key, _ := taskKey("race")
		if err := r.other.WriteEntities(ctx, nil, objstore.Write{
			Op: objstore.Update, Entity: ScheduledTasks, Key: key, Attrs: attrs,
		}); err != nil {
			return false, err
		}
	}
	return r.Store.GuardedBatch(ctx, checks, writes)
}

func TestDueTaskClaimKeepsConcurrentReschedule(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	now := time.Now()
	due := now.Add(-time.Minute)
	if err := s.CreateTask(core.Task{ID: "race", Status: core.TaskActive, NextRun: &due}); err != nil {
		t.Fatal(err)
	}
	race := NewOver(&stateRace{Store: s.obj, other: s,
		attrs: map[string]string{attrTaskNextRun: now.Add(time.Hour).Format(time.RFC3339)},
	})
	tasks, err := race.DueTasks(now)
	if err != nil || len(tasks) != 0 {
		t.Fatalf("claimed a rescheduled task: %v %v", tasks, err)
	}
	task, _ := s.GetTask("race")
	if task.Status != core.TaskActive || task.NextRun == nil || !task.NextRun.After(now) {
		t.Fatalf("reschedule lost: %+v", task)
	}
}

func TestStateIndexKeepsConcurrentTransition(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	due := time.Now().Add(-time.Minute)
	if err := s.CreateTask(core.Task{ID: "race", Status: core.TaskActive, NextRun: &due}); err != nil {
		t.Fatal(err)
	}
	race := NewOver(&stateRace{Store: s.obj, other: s})
	key, _ := taskKey("race")
	if err := race.WriteEntities(context.Background(), nil, objstore.Write{
		Op: objstore.Update, Entity: ScheduledTasks, Key: key,
		Attrs: map[string]string{attrTaskPrompt: "changed"},
	}); err != nil {
		t.Fatal(err)
	}
	task, ok := s.GetTask("race")
	if !ok || task.Status != core.TaskPaused || task.Prompt != "changed" {
		t.Fatalf("concurrent pause lost: %+v", task)
	}
	c := &stateReadCounter{Store: s.obj}
	tasks, err := NewOver(c).DueTasks(time.Now())
	if err != nil || len(tasks) != 0 || c.rows != 0 {
		t.Fatalf("paused task left due work: %v %v rows=%d", tasks, err, c.rows)
	}
}

func TestStateIndexWriteSemantics(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	ctx := context.Background()
	key, _ := taskKey("ops")
	write := func(w objstore.Write) {
		t.Helper()
		w.Entity, w.Key = ScheduledTasks, key
		if err := s.WriteEntities(ctx, nil, w); err != nil {
			t.Fatal(err)
		}
	}
	write(objstore.Write{Op: objstore.Merge,
		Init:  map[string]string{attrTaskStatus: core.TaskActive},
		Attrs: map[string]string{attrTaskNextRun: "2020-01-01T00:00:00Z", attrTaskPrompt: "first"}})
	write(objstore.Write{Op: objstore.Merge,
		Init:  map[string]string{attrTaskPrompt: "wrong"},
		Attrs: map[string]string{attrTaskStatus: core.TaskPaused}})
	task, ok := s.GetTask("ops")
	if !ok || task.Prompt != "first" || task.Status != core.TaskPaused {
		t.Fatalf("merge changed initial values: %+v", task)
	}
	write(objstore.Write{Op: objstore.Put, Attrs: map[string]string{
		attrTaskStatus: core.TaskActive, attrTaskNextRun: "2020-01-01T00:00:00Z"}})
	task, _ = s.GetTask("ops")
	if task.Prompt != "" {
		t.Fatalf("put retained old attributes: %+v", task)
	}
	write(objstore.Write{Op: objstore.Update, Remove: []string{attrTaskStatus}})
	c := &stateReadCounter{Store: s.obj}
	if tasks, err := NewOver(c).DueTasks(time.Now()); err != nil || len(tasks) != 0 || c.rows != 0 {
		t.Fatalf("removed state remains indexed: %v %v rows=%d", tasks, err, c.rows)
	}
	write(objstore.Write{Op: objstore.Update, Attrs: map[string]string{attrTaskStatus: core.TaskActive}})
	write(objstore.Write{Op: objstore.Delete})
	c.rows = 0
	if tasks, err := NewOver(c).DueTasks(time.Now()); err != nil || len(tasks) != 0 || c.rows != 0 {
		t.Fatalf("deleted state remains indexed: %v %v rows=%d", tasks, err, c.rows)
	}
}

type legacyTurnData struct{ objstore.Store }

func (l legacyTurnData) Query(ctx context.Context, q objstore.Query) ([]objstore.Item, error) {
	rows, err := l.Store.Query(ctx, q)
	if q.Entity == TurnContexts {
		for i := range rows {
			rows[i].Attrs[AttrTurnStartedAt] = "2020-01-01T00:00:00Z"
		}
	}
	return rows, err
}
func TestIndexUpdatePreservesUntouchedLegacyData(t *testing.T) {
	s := openTurnEntities(t)
	if ok, err := s.obj.GuardedBatch(t.Context(), nil, []objstore.Write{{Op: objstore.Put, Entity: TurnContexts,
		Key: objstore.Key{Partition: OwnerRoutd, Sort: "legacy"}, Attrs: map[string]string{
			attrTurnState: TurnRunning, AttrTurnStartedAt: "2020-01-01T00:00:00.000000000Z",
		}}}); err != nil || !ok {
		t.Fatalf("seed: %v %v", ok, err)
	}
	legacy := NewOver(legacyTurnData{s.obj})
	if err := legacy.WriteEntities(t.Context(), nil, objstore.Write{Op: objstore.Update, Entity: TurnContexts,
		Key: objstore.Key{Partition: OwnerRoutd, Sort: "legacy"}, Attrs: map[string]string{attrTurnRunReturned: flagSet}}); err != nil {
		t.Fatal(err)
	}
}

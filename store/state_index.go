package store

import (
	"context"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

type stateIndexes struct{}

func (stateIndexes) Indexed(entity string) bool { return entity == ScheduledTasks }

func (stateIndexes) Keys(entity string, it objstore.Item) []objstore.Key {
	if entity != ScheduledTasks {
		return nil
	}
	switch it.Attrs[attrTaskStatus] {
	case core.TaskActive:
		due := time.Time{}
		if at, ok := taskNextRun(it); ok {
			due = at
		}
		return []objstore.Key{{Partition: "tasks/due", Sort: core.TS(due)}}
	case TaskStatusFiring:
		return []objstore.Key{{Partition: "tasks/firing", Sort: it.Key.Sort}}
	}
	return nil
}

func (s *Store) MigrateStateIndexes(ctx context.Context) error {
	return objstore.Reindex(ctx, s.Obj(), "tasks-v1", []objstore.Query{
		{Entity: ScheduledTasks, Partition: OwnerRoutd},
	})
}

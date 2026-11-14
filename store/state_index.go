package store

import (
	"context"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

type stateIndexes struct{}

func (stateIndexes) Indexed(entity string) bool {
	switch entity {
	case ScheduledTasks, TurnContexts, TurnArchive, MessageDelivery:
		return true
	}
	return false
}

func (stateIndexes) Keys(entity string, it objstore.Item) []objstore.Key {
	if entity == TurnContexts || entity == TurnArchive {
		return turnIndexes(entity, it)
	}
	if entity == MessageDelivery {
		if it.Key.Partition != core.MessageStatusPending {
			return nil
		}
		due, _ := deliveryDueAt(it)
		return []objstore.Key{{Partition: "outbound/due", Sort: core.TS(due)}}
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
	if err := objstore.Reindex(ctx, s.Obj(), "tasks-v1", []objstore.Query{
		{Entity: ScheduledTasks, Partition: OwnerRoutd},
	}); err != nil {
		return err
	}
	if err := objstore.Reindex(ctx, s.Obj(), "turns-v1", []objstore.Query{
		{Entity: TurnContexts, Partition: OwnerRoutd}, {Entity: TurnArchive, Partition: OwnerRoutd},
	}); err != nil {
		return err
	}
	return objstore.Reindex(ctx, s.Obj(), "outbound-v1", []objstore.Query{{Entity: MessageDelivery, Partition: core.MessageStatusPending}})
}

func turnIndexes(entity string, it objstore.Item) []objstore.Key {
	a := it.Attrs
	var keys []objstore.Key
	add := func(partition, sort string) { keys = append(keys, objstore.Key{Partition: partition, Sort: sort}) }
	if at, err := time.Parse(time.RFC3339Nano, a[AttrTurnStartedAt]); err == nil {
		add(entity+"/started", core.TS(at))
		if entity == TurnContexts && a[attrTurnState] == TurnRunning &&
			(a["provider_failure_attempt"] == "" || a["provider_failure_attempt"] != a["attempt_id"]) {
			add("turns/expiry", core.TS(at))
		}
	}
	if entity == TurnArchive {
		return keys
	}
	switch a[attrTurnState] {
	case TurnRunning, TurnPendingRetry:
		add("turns/live", it.Key.Sort)
	}
	if a[attrTurnState] == TurnRunning && a[attrTurnRunReturned] == flagSet {
		add("turns/recovery", it.Key.Sort)
	}
	if a[attrTurnState] == TurnPendingRetry && a["next_attempt_at"] != "" {
		add("turns/retry", a["next_attempt_at"])
	}
	if _, owed := a["status_owed"]; owed {
		add("turns/status", it.Key.Sort)
	}
	return keys
}

func (s *Store) indexedTurns(ctx context.Context, partition string, key objstore.KeyPredicate) ([]TurnContext, error) {
	items, err := objstore.IndexItems(ctx, s.Obj(), partition, key, 0)
	if err != nil {
		return nil, err
	}
	turns := make([]TurnContext, 0, len(items))
	for _, it := range items {
		turns = append(turns, turnFrom(it))
	}
	return turns, nil
}

func (s *Store) RecoverableTurns(ctx context.Context) ([]TurnContext, error) {
	return s.indexedTurns(ctx, "turns/recovery", objstore.KeyPredicate{})
}

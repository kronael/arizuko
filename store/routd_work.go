package store

import (
	"context"
	"crypto/rand"
	"slices"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

func (stateIndexes) Companions(entity string, before, after objstore.Item) []objstore.Write {
	switch entity {
	case TurnControl:
		return []objstore.Write{routdWork("control", "singleton", time.Time{})}
	case TurnContexts:
		if before.Attrs[attrTurnState] != after.Attrs[attrTurnState] || before.Attrs[attrTurnRunReturned] != after.Attrs[attrTurnRunReturned] {
			return []objstore.Write{routdWork("control", "singleton", time.Time{})}
		}
		return nil
	case Chats:
		for _, name := range []string{AttrChatAgentCursor, attrChatInputFirst, attrChatInputLast} {
			if before.Attrs[name] != after.Attrs[name] {
				return []objstore.Write{routdWork("control", "singleton", time.Time{})}
			}
		}
		return nil
	case Routes, Groups:
		return []objstore.Write{routdWork("control", "singleton", time.Time{})}
	case PendingActions:
	default:
		return onboardingCompanions(entity, before, after)
	}
	if after.Attrs[AttrPendStatus] == "held" {
		return nil
	}
	if before.Attrs[objstore.IndexRevision] != "" && before.Attrs[AttrPendStatus] == after.Attrs[AttrPendStatus] && before.Attrs[AttrPendExpiresAt] == after.Attrs[AttrPendExpiresAt] {
		return nil
	}
	id := after.Key.Sort
	if id == "" {
		id = before.Key.Sort
	}
	if id == "" {
		return nil
	}
	return []objstore.Write{routdWork("approvals", id, time.Time{})}
}

func routdWork(kind, id string, due time.Time) objstore.Write {
	return objstore.Write{Op: objstore.Merge, Entity: RoutdWork, Key: objstore.Key{Partition: kind, Sort: id}, Attrs: map[string]string{"generation": rand.Text(), "due": core.TS(due)}}
}

func (s *Store) DueWork(ctx context.Context, kind string, now time.Time) ([]objstore.Item, error) {
	items, err := objstore.IndexItems(ctx, s.Obj(), "routd_work/"+kind, objstore.KeyPredicate{Op: objstore.LtKey, Value: core.TS(now) + "\x7f"}, 200)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(items, func(it objstore.Item) bool { return it.Attrs["due"] > core.TS(now) }), nil
}

func (s *Store) AcknowledgeWork(ctx context.Context, work objstore.Item) error {
	return s.finishWork(ctx, work, objstore.Delete, nil)
}

func (s *Store) RescheduleWork(ctx context.Context, work objstore.Item, due time.Time) error {
	return s.finishWork(ctx, work, objstore.Update, map[string]string{"due": core.TS(due)})
}

func (s *Store) finishWork(ctx context.Context, work objstore.Item, op objstore.WriteOp, attrs map[string]string) error {
	// A refused acknowledgment belongs to a newer generation, which stays pending.
	_, err := s.TryWriteEntities(ctx, nil, objstore.Write{Op: op, Entity: RoutdWork, Key: work.Key, Attrs: attrs, Cond: objstore.Cond{Item: objstore.ItemPresent, Where: []objstore.Cmp{{Attr: "generation", Op: objstore.Eq, Value: work.Attrs["generation"]}}}})
	return err
}

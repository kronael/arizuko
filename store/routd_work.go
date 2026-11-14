package store

import (
	"context"
	"crypto/rand"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

func (stateIndexes) Companions(entity string, before, after objstore.Item) []objstore.Write {
	if entity != PendingActions {
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
	return objstore.IndexItems(ctx, s.Obj(), "routd_work/"+kind, objstore.KeyPredicate{Op: objstore.LtKey, Value: core.TS(now) + "\x7f"}, 200)
}

func (s *Store) AcknowledgeWork(ctx context.Context, work objstore.Item) error {
	// A refused acknowledgment belongs to a newer generation, which stays pending.
	_, err := s.TryWriteEntities(ctx, nil, objstore.Write{Op: objstore.Delete, Entity: RoutdWork, Key: work.Key, Cond: objstore.Cond{Item: objstore.ItemPresent, Where: []objstore.Cmp{{Attr: "generation", Op: objstore.Eq, Value: work.Attrs["generation"]}}}})
	return err
}

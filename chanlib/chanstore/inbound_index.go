package chanstore

import (
	"context"
	"strconv"

	"github.com/kronael/arizuko/objstore"
)

type inboundIndex struct{}

func (inboundIndex) Indexed(entity string) bool { return entity == InboundPending }
func (inboundIndex) Keys(_ string, it objstore.Item) []objstore.Key {
	return []objstore.Key{{Partition: "inbound/due", Sort: it.Attrs[attrNextAttemptAt] + "\x1f" + it.Attrs[attrFirstSeenAt]}}
}

func (s *Store) MigrateInbound(ctx context.Context) error {
	if err := objstore.Reindex(ctx, s.s, "inbound-due-v1", []objstore.Query{{Entity: InboundPending, Partition: pendingPartition}}); err != nil {
		return err
	}
	if _, ok, err := s.point(ctx, objstore.IndexEntity, heldCountKey); err != nil || ok {
		return err
	}
	rows, err := s.s.Query(ctx, objstore.Query{Entity: InboundPending, Partition: pendingPartition, Project: []string{attrAttempts}})
	if err != nil {
		return err
	}
	_, err = s.s.GuardedBatch(ctx, nil, []objstore.Write{{Op: objstore.Put, Entity: objstore.IndexEntity, Key: heldCountKey,
		Attrs: map[string]string{"count": strconv.Itoa(len(rows))}, Cond: objstore.Cond{Item: objstore.ItemAbsent}}})
	return err
}

package googstore

import (
	"context"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

type indexes struct{}

func (indexes) Indexed(entity string) bool { return entity == PollEntity }

func (indexes) Keys(entity string, it objstore.Item) []objstore.Key {
	if it.Attrs["active"] != "1" {
		return nil
	}
	due := it.Attrs["next_poll_at"]
	if due == "" {
		due = core.TS(time.Time{})
	}
	return []objstore.Key{objstore.DueIndexKey("file_poll/due/"+it.Key.Partition, due, entity, it.Key)}
}

func Open(ctx context.Context, raw objstore.Store, channel string) (objstore.Store, error) {
	s := objstore.WithIndexes(raw, indexes{})
	err := objstore.Reindex(ctx, s, "file-poll-v1/"+channel, []objstore.Query{{Entity: PollEntity, Partition: channel}})
	return s, err
}

func Due(ctx context.Context, s objstore.Store, channel string, now time.Time) ([]objstore.Item, error) {
	return objstore.IndexItems(ctx, s, "file_poll/due/"+channel, objstore.KeyPredicate{Op: objstore.LtKey, Value: core.TS(now) + "\x7f"}, 0)
}

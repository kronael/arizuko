package audit

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

// Retention is how long an event stays in `audit_log`. It is the ONE entity
// the platform expires: messages and every other log are kept without limit.
const Retention = 90 * 24 * time.Hour

// pruneInterval is how often PruneLoop prunes.
const pruneInterval = 24 * time.Hour

// Prune deletes every event older than before and answers how many it deleted.
// It reads and deletes one batch at a time, oldest first, so an error or a
// cancelled ctx leaves the newer rows and the work already done.
func Prune(ctx context.Context, s objstore.Store, before time.Time) (int, error) {
	bound := idAt(core.TS(before), strings.Repeat("0", suffixHex))
	deleted := 0
	for {
		items, err := s.Query(ctx, objstore.Query{
			Entity: Entity, Partition: logPartition,
			Key:     objstore.KeyPredicate{Op: objstore.LtKey, Value: bound},
			Limit:   objstore.MaxBatchActions,
			Project: []string{attrCategory},
		})
		if err != nil {
			return deleted, err
		}
		if len(items) == 0 {
			return deleted, nil
		}
		writes := make([]objstore.Write, len(items))
		for i, it := range items {
			writes[i] = objstore.Write{Op: objstore.Delete, Entity: Entity, Key: it.Key}
		}
		if _, err := s.GuardedBatch(ctx, nil, writes); err != nil {
			return deleted, err
		}
		deleted += len(items)
		if len(items) < objstore.MaxBatchActions {
			return deleted, nil
		}
	}
}

// PruneLoop prunes s to Retention at once and then once a day until ctx ends.
// The daemon that owns an audit_log runs it; a daemon that only records into
// another's does not.
func PruneLoop(ctx context.Context, s objstore.Store) {
	for {
		n, err := Prune(ctx, s, time.Now().Add(-Retention))
		if err != nil && ctx.Err() == nil {
			slog.Error("prune audit_log", "deleted", n, "err", err)
		} else if n > 0 {
			slog.Info("pruned audit_log", "deleted", n)
		}
		timer := time.NewTimer(pruneInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

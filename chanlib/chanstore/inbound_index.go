package chanstore

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/retry"
)

type inboundIndex struct{}

func (inboundIndex) Indexed(entity string) bool { return entity == InboundPending }
func (inboundIndex) Keys(_ string, it objstore.Item) []objstore.Key {
	if it.Attrs[attrRetryState] == "failed" {
		return nil
	}
	return []objstore.Key{objstore.DueIndexKey("inbound/due", it.Attrs[attrNextAttemptAt]+"\x1f"+it.Attrs[attrFirstSeenAt], InboundPending, it.Key)}
}

func (s *Store) MigrateInbound(ctx context.Context) error {
	if err := objstore.Reindex(ctx, s.s, "inbound-due-v1", []objstore.Query{{Entity: InboundPending, Partition: pendingPartition}}); err != nil {
		return err
	}
	marker := objstore.Key{Partition: "migrations", Sort: "inbound-retry-v1"}
	if _, ok, err := s.point(ctx, objstore.IndexEntity, marker); err != nil || ok {
		return err
	}
	rows, err := s.s.Query(ctx, objstore.Query{Entity: InboundPending, Partition: pendingPartition})
	if err != nil {
		return err
	}
	for _, row := range rows {
		if _, ok := row.Attrs[attrRetryState]; ok {
			if _, err := parseInboundDecision(row); err != nil {
				slog.Error("inbound migration: malformed row left in place", "id", row.Key.Sort, "err", err)
			}
			continue
		}
		attempts, err := strconv.Atoi(row.Attrs[attrAttempts])
		if err != nil || attempts < 0 {
			slog.Error("inbound migration: malformed row left in place", "id", row.Key.Sort, "attempts", row.Attrs[attrAttempts])
			continue
		}
		decision := retry.Next(attempts, retry.Failure{Kind: "transient"}, time.Now(), time.Time{})
		if !decision.Stop {
			decision.NextAt, err = time.Parse(time.RFC3339Nano, row.Attrs[attrNextAttemptAt])
			if err != nil {
				slog.Error("inbound migration: malformed row left in place", "id", row.Key.Sort, "err", err)
				continue
			}
		}
		attrs, remove := retryAttrs(decision, retry.Notice(row.Attrs[attrLastError], decision))
		observed := Held{ID: row.Key.Sort, Attempts: attempts, Revision: row.Attrs[objstore.IndexRevision]}
		cond := heldCond(observed)
		cond.Where = append(cond.Where, objstore.Cmp{Attr: attrRetryState, Op: objstore.Absent})
		ok, err := s.s.GuardedBatch(ctx, nil, []objstore.Write{{Op: objstore.Update, Entity: InboundPending, Key: row.Key, Attrs: attrs, Remove: remove, Cond: cond}})
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("normalize inbound %s: row changed", row.Key.Sort)
		}
	}
	_, err = s.s.GuardedBatch(ctx, nil, []objstore.Write{{Op: objstore.Put, Entity: objstore.IndexEntity, Key: marker, Attrs: map[string]string{"done": "1"}, Cond: objstore.Cond{Item: objstore.ItemAbsent}}})
	return err
}

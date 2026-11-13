package store

// turn_archive.go holds `turn_archive`, the turns that finished: `done` or
// `expired` with no status owed. The write that finishes a turn moves its item
// here in the same batch, so `turn_context` holds only the turns still live and
// a read of that partition costs what is live, not every turn ever run (BUGS
// L823). The coordinate is the live one, OwnerRoutd and the turn id: a late
// frame, a delivery retry and `GET /v1/cost?turn_id=` find a finished turn by
// the id alone, and a turn id is a platform message id, which carries no time.

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/kronael/arizuko/objstore"
)

func init() { objstore.OrderedAttr(TurnArchive, AttrTurnStartedAt, tsWidth) }

var turnAttrs = []string{
	attrTurnFolder, attrTurnTopic, attrTurnChatJID, attrTurnTrigger, attrTurnTriggerMsg,
	attrTurnReturnTo, attrTurnRunID, attrTurnState, attrTurnRunReturned, attrTurnRetryCount,
	attrTurnGatewayKey, attrTurnAddressed, attrTurnNudged, AttrTurnStartedAt, "attempt_id",
	"input_last", "provider_failure_attempt", "input_keys", "ambient_keys",
	"next_attempt_at", "failure_cause", "status_owed", "status_chats",
}

func isFinished(attrs map[string]string) bool {
	_, returned := attrs[attrTurnRunReturned]
	return isSettled(attrs) && (returned || attrs[attrTurnState] == TurnExpired)
}

func isSettled(attrs map[string]string) bool {
	_, owed := attrs["status_owed"]
	state := attrs[attrTurnState]
	return !owed && (state == TurnDone || state == TurnExpired)
}

func unchanged(attrs map[string]string) []objstore.Cmp {
	var where []objstore.Cmp
	for _, name := range slices.Sorted(maps.Keys(attrs)) {
		// An ordered value of an old width is refused as a comparand (BUGS
		// L97); every write that moves one of these moves the state too.
		if name == AttrTurnStartedAt || name == "next_attempt_at" {
			continue
		}
		where = append(where, objstore.Cmp{Attr: name, Op: objstore.Eq, Value: attrs[name]})
	}
	for _, name := range turnAttrs {
		if _, ok := attrs[name]; !ok {
			where = append(where, objstore.Cmp{Attr: name, Op: objstore.Absent})
		}
	}
	return where
}

func moveWrites(from, to string, key objstore.Key, cond objstore.Cond, attrs map[string]string) []objstore.Write {
	return []objstore.Write{
		{Op: objstore.Delete, Entity: from, Key: key, Cond: cond},
		{Op: objstore.Put, Entity: to, Key: key, Attrs: attrs},
	}
}

func (s *Store) findTurn(ctx context.Context, key objstore.Key) (string, objstore.Item, bool, error) {
	for _, entity := range []string{TurnContexts, TurnArchive} {
		it, ok, err := s.itemAt(ctx, entity, key)
		if err != nil || ok {
			return entity, it, ok, err
		}
	}
	return "", objstore.Item{}, false, nil
}

func (s *Store) writeTurn(ctx context.Context, checks []objstore.Check, others []objstore.Write,
	w objstore.Write) (bool, error) {
	for range retryRounds {
		from, it, ok, err := s.findTurn(ctx, w.Key)
		if err != nil {
			return false, err
		}
		if !ok {
			return s.TryWriteEntities(ctx, checks, append(others, w)...)
		}
		next := maps.Clone(it.Attrs)
		maps.Copy(next, w.Attrs)
		for _, name := range w.Remove {
			delete(next, name)
		}
		to := TurnContexts
		if isFinished(next) {
			to = TurnArchive
		}
		guarded := w
		guarded.Entity = from
		guarded.Cond.Where = append(slices.Clone(w.Cond.Where), unchanged(it.Attrs)...)
		writes := append(slices.Clone(others), guarded)
		if to != from {
			writes = append(slices.Clone(others), moveWrites(from, to, w.Key, guarded.Cond, next)...)
		}
		written, err := s.TryWriteEntities(ctx, checks, writes...)
		if err != nil || written {
			return written, err
		}
		again, ok, err := s.itemAt(ctx, from, w.Key)
		if err != nil {
			return false, err
		}
		if !ok || maps.Equal(again.Attrs, it.Attrs) {
			return false, nil
		}
	}
	return false, fmt.Errorf("write turn %s: the turn changed during %d writes", w.Key.Sort, retryRounds)
}

func (s *Store) turnItem(ctx context.Context, turnID string) (objstore.Item, bool, error) {
	key, err := turnKey(turnID)
	if err != nil {
		return objstore.Item{}, false, err
	}
	_, it, ok, err := s.findTurn(ctx, key)
	return it, ok, err
}

func (s *Store) archivedTurnsSince(ctx context.Context, since string) ([]objstore.Item, error) {
	return s.Obj().Query(ctx, objstore.Query{
		Entity: TurnArchive, Partition: OwnerRoutd,
		Where: []objstore.Cmp{{Attr: AttrTurnStartedAt, Op: objstore.Ge, Value: since}},
	})
}

const archiveBatch = objstore.MaxBatchActions / 2

// MigrateTurnArchive moves the finished turns `turn_context` held before the
// archive existed, once, and records the run of each live turn in `run_turns`.
func (s *Store) MigrateTurnArchive(ctx context.Context) error {
	marker := objstore.Key{Partition: OwnerRoutd, Sort: "turn-archive"}
	_, done, err := s.itemAt(ctx, Counters, marker)
	if err != nil || done {
		return err
	}
	items, err := s.obj.Query(ctx, objstore.Query{Entity: TurnContexts, Partition: OwnerRoutd})
	if err != nil {
		return fmt.Errorf("read the turns to archive: %w", err)
	}
	var moves []objstore.Write
	for _, it := range items {
		if !isSettled(it.Attrs) {
			if run := it.Attrs[attrTurnRunID]; run != "" {
				if err := s.WriteEntities(ctx, nil, runTurnWrite(run, it)); err != nil {
					return err
				}
			}
			continue
		}
		cond := objstore.Cond{Where: unchanged(it.Attrs)}
		moves = append(moves, moveWrites(TurnContexts, TurnArchive, it.Key, cond, it.Attrs)...)
	}
	for chunk := range slices.Chunk(moves, 2*archiveBatch) {
		if err := s.WriteEntities(ctx, nil, chunk...); err != nil {
			return fmt.Errorf("archive %d finished turns: %w", len(chunk)/2, err)
		}
	}
	return s.WriteEntities(ctx, nil, objstore.Write{
		Op: objstore.Put, Entity: Counters, Key: marker,
		Attrs: map[string]string{"done": "1"}, Cond: objstore.Cond{Item: objstore.ItemAbsent},
	})
}

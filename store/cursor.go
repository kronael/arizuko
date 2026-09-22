package store

// cursor.go is the forward-only guarded transition, written once because two
// entities carry a cursor and a cursor that moves the other way re-feeds
// consumed messages.
//
// It is a guarded write and not a read-then-write (spec 6/40 §2): the engine
// picks the winner and no caller holds a lock across a read. It is THREE
// attempts because the predicate "behind OR absent OR no item" is a
// disjunction and a Cond is a conjunction: (1) the item is there and behind,
// the steady state; (2) the item has never carried a cursor, because
// PutSession writes a session id and no cursor; (3) there is no item, because
// buildAgentPrompt advances the cursor BEFORE PutSession creates the row.
// Three refusals mean the cursor is already at or past the target, or a
// writer created the item between attempts; the read tells them apart.
//
// cursor_test.go proves it never moves back by dropping the condition. The
// interleave is FORCED with conformance.SlipAt rather than raced, because
// three raced concurrency tests in this project stayed green against broken
// code.

import (
	"context"
	"fmt"
	"maps"

	"github.com/kronael/arizuko/objstore"
)

// cursorRounds bounds the retry. A round only repeats when another writer
// created the item under this one, so two are enough for any interleave; the
// bound makes an infinite loop impossible.
const cursorRounds = 4

// advanceCursor moves one item's ordered attribute to `to` and only forward,
// and reports whether this call is what moved it. seed is what the item
// carries when this call creates it; the cursor itself is added here, so a
// caller cannot seed one value and guard on another.
func (s *Store) advanceCursor(ctx context.Context, entity string, key objstore.Key,
	attr, to string, seed map[string]string) (bool, error) {
	if to == "" {
		return false, fmt.Errorf("%s/%s: a cursor may not be advanced to nothing; "+
			"absent is the only null this model has, so clearing one is a Remove",
			entity, key.Sort)
	}
	ahead := map[string]string{attr: to}
	fresh := make(map[string]string, len(seed)+1)
	maps.Copy(fresh, seed)
	fresh[attr] = to
	attempts := []objstore.Write{
		{Op: objstore.Update, Entity: entity, Key: key, Attrs: ahead,
			Cond: objstore.Cond{Where: []objstore.Cmp{{Attr: attr, Op: objstore.Lt, Value: to}}}},
		{Op: objstore.Update, Entity: entity, Key: key, Attrs: ahead,
			Cond: objstore.Cond{Where: []objstore.Cmp{{Attr: attr, Op: objstore.Absent}}}},
		{Op: objstore.Put, Entity: entity, Key: key, Attrs: fresh,
			Cond: objstore.Cond{Item: objstore.ItemAbsent}},
	}
	for range cursorRounds {
		for _, w := range attempts {
			moved, err := s.TryWriteEntities(ctx, nil, w)
			if err != nil {
				return false, fmt.Errorf("advance %s on %s/%s: %w", attr, entity, key.Sort, err)
			}
			if moved {
				return true, nil
			}
		}
		// Every shape refused: a cursor already at or past the target is the
		// idempotent re-call and is done; an item that appeared under the
		// attempts is what the next round takes.
		it, ok, err := s.itemAt(ctx, entity, key)
		if err != nil {
			return false, err
		}
		if ok && it.Attrs[attr] >= to {
			return false, nil
		}
	}
	return false, fmt.Errorf("advance %s on %s/%s to %q: %d rounds of guarded writes were "+
		"all refused, so the item is being rewritten faster than this can read it",
		attr, entity, key.Sort, to, cursorRounds)
}

// itemAt reads one item by its whole coordinate.
func (s *Store) itemAt(ctx context.Context, entity string, key objstore.Key) (objstore.Item, bool, error) {
	items, err := s.Obj().Query(ctx, objstore.Query{
		Entity: entity, Partition: key.Partition,
		Key: objstore.KeyPredicate{Op: objstore.ExactKey, Value: key.Sort},
	})
	if err != nil {
		return objstore.Item{}, false, fmt.Errorf("read %s/%s: %w", entity, key.Sort, err)
	}
	if len(items) == 0 {
		return objstore.Item{}, false, nil
	}
	return items[0], true, nil
}

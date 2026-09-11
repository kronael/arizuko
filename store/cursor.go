package store

// cursor.go is the forward-only guarded transition, written once because two
// entities carry a cursor and a cursor that moves the other way re-feeds
// consumed messages.
//
// IT IS A GUARDED WRITE AND NOT A READ-THEN-WRITE (spec 6/40 §2). The SQL it
// replaces made the decision a condition on the row the decision came from —
// `WHERE observed_cursor IS NULL OR observed_cursor < ?` — so the engine picked
// the winner and no caller held a lock across a read. That property is the whole
// point, so this keeps it: every attempt below is one conditional write whose
// row count the engine decides.
//
// IT IS THREE ATTEMPTS BECAUSE THE PREDICATE IS A DISJUNCTION AND A Cond IS A
// CONJUNCTION. The one statement says "the cursor is behind OR there is no
// cursor", and the row may not exist at all, so the three shapes are:
//
//  1. the item is there and its cursor is behind — the steady state, one write;
//  2. the item is there and has never carried one — a topic whose row was
//     created by PutSession, which writes a session id and no cursor;
//  3. there is no item — the first turn of a topic, which is why the statement
//     this replaces was an UPSERT rather than an UPDATE: buildAgentPrompt
//     advances the cursor BEFORE PutSession creates the row, and a plain UPDATE
//     matched nothing and re-included the whole observed window.
//
// The first that commits is the answer and the rest never run, so the steady
// state costs one write. Three refusals mean either the cursor is already at or
// past the target — the idempotent re-call, and the read below is what tells
// them apart — or a writer created the item between attempts 1 and 3, which the
// next round takes.
//
// WHAT IT NEVER DOES IS MOVE THE CURSOR BACK. cursor_test.go proves that by
// breaking it: with the condition dropped the same interleave walks the cursor
// backwards and the test goes red. The interleave is FORCED with
// conformance.SlipAt rather than raced, because three concurrency tests in this
// project stayed green against deliberately broken code.

import (
	"context"
	"fmt"
	"maps"

	"github.com/kronael/arizuko/objstore"
)

// cursorRounds bounds the retry. A round only repeats when another writer
// created the item under this one, so two are enough for any interleave and the
// fourth is there to make an infinite loop impossible rather than to be used.
const cursorRounds = 4

// advanceCursor moves one item's ordered attribute to `to` and only forward, and
// reports whether this call is what moved it.
//
// seed is what the item carries when this call is the one that creates it. The
// cursor itself is added here, so a caller cannot seed a row with a cursor of
// one value and then guard on another.
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
		// Every shape refused, so the read decides between the two states that
		// produce that: a cursor already at or past the target, which is the
		// idempotent re-call and is done; or an item that appeared under the
		// attempts, which the next round takes.
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
//
// It is here rather than in each entity file because the retry above is the one
// body that needs a point read it cannot name an entity for: the caller passes
// the entity, so the read has to take it too.
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

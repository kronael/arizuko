package objstore

import (
	"context"
	"fmt"
	"strconv"
)

// maxCASAttempts bounds Increment's retry. Only a LOST RACE is retried; a
// missing item, a non-number or a failed caller condition returns on the first
// answer.
const maxCASAttempts = 8

// Increment adds delta to a counted attribute and returns the new value.
//
// It is a PROTOCOL, not an interface operation: every attribute value is a
// string, and DynamoDB's in-place increment needs a Number. cond is the
// caller's own guard and rides on the same write. also carries the companion
// actions that must land WITH the counter — an invite redemption that
// committed only the counter would burn an invite with no grant. A retry
// re-sends them, which is safe because a refused batch wrote nothing.
func Increment(ctx context.Context, s Store, entity string, key Key, attr string, delta int,
	cond []Cmp, also []Write) (value int, committed bool, err error) {
	// Only a WHOLE-value declaration is a layout a number renders into; a lead
	// rule carries a tiebreak a count does not have.
	r, declared := orderedRule(entity, attr)
	fixed := declared && !r.lead
	width := r.width
	read := func() (string, int, error) {
		items, err := s.Query(ctx, Query{
			Entity: entity, Partition: key.Partition,
			Key:     KeyPredicate{Op: ExactKey, Value: key.Sort},
			Project: []string{attr},
		})
		if err != nil {
			return "", 0, err
		}
		if len(items) == 0 {
			return "", 0, fmt.Errorf("increment %s.%s on %s/%s: no such item",
				entity, attr, key.Partition, key.Sort)
		}
		was, ok := items[0].Attrs[attr]
		if !ok {
			return "", 0, fmt.Errorf("increment %s.%s on %s/%s: the item carries no "+
				"such attribute, and a counter with no zero is a modelling gap rather than a "+
				"value to invent", entity, attr, key.Partition, key.Sort)
		}
		n, err := strconv.Atoi(was)
		if err != nil {
			return "", 0, fmt.Errorf("increment %s.%s on %s/%s: %q is not a number: %w",
				entity, attr, key.Partition, key.Sort, was, err)
		}
		return was, n, nil
	}
	for range maxCASAttempts {
		was, n, err := read()
		if err != nil {
			return 0, false, err
		}
		next := n + delta
		// The width the value was WRITTEN at is the width it is compared at.
		text := strconv.Itoa(next)
		if fixed {
			text = fmt.Sprintf("%0*d", width, next)
		}
		// The read value IS the condition. Anyone who incremented in between
		// changed it, so this write refuses and the loop reads again.
		guard := append([]Cmp{{Attr: attr, Op: Eq, Value: was}}, cond...)
		writes := append([]Write{{
			Op: Update, Entity: entity, Key: key,
			Attrs: map[string]string{attr: text},
			Cond:  Cond{Where: guard},
		}}, also...)
		ok, err := s.GuardedBatch(ctx, nil, writes)
		if err != nil {
			return 0, false, err
		}
		if ok {
			return next, true, nil
		}
		// A refused batch is a lost race only if the counter MOVED; otherwise
		// the caller's own condition refused. The re-read is outside the refused
		// transaction, so a third caller in the gap costs one more attempt and
		// then answers correctly.
		again, n, err := read()
		if err != nil {
			return 0, false, err
		}
		if again == was {
			return n, false, nil
		}
	}
	return 0, false, fmt.Errorf("increment %s.%s on %s/%s lost %d races in a row",
		entity, attr, key.Partition, key.Sort, maxCASAttempts)
}

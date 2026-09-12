package objstore

// protocol.go holds what the two primitives compose into. A protocol is a
// package-level function over Query and GuardedBatch, written once and shared
// by every driver — never a third method some engine cannot carry (spec 6/40
// §"The seam is TWO primitives").

import (
	"context"
	"fmt"
	"strconv"
)

// maxCASAttempts bounds Increment's retry.
//
// A refusal here is a LOST RACE and nothing else — the condition is the value
// this call itself read one round trip ago — so it is the transient class that
// may be retried. Anything that is not a lost race (a missing item, a value
// that is not a number, a caller condition that does not hold) returns on the
// first answer with no retry at all.
const maxCASAttempts = 8

// Increment adds delta to a counted attribute and returns the new value.
//
// It is a PROTOCOL, not an interface operation, and the reason is the one
// modelling rule: every attribute value is a string. DynamoDB's in-place
// increment is `SET Price = Price + :p`, and the page says what it needs —
// "You can also use SET to add or subtract from an attribute that is of type
// Number" — as does ADD: "The ADD action supports only number and set data
// types."
// https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Expressions.UpdateExpressions.html
//
// So an atomic in-place increment does not exist for a string attribute on the
// engine the interface is measured against. Putting one on the interface would
// mean either a typed attribute model — the comparison grammar per driver that
// 6/37 refuses — or an operation only the SQL driver can honour, which is the
// exact drift the conformance suite exists to catch.
//
// What it costs to be a protocol instead is one extra round trip and a retry,
// and what it buys back is the RETURNING half for free: the caller computed the
// new value, so it does not need the engine to hand it back. store/invites.go's
// `used_count = used_count + 1 … RETURNING` gets both halves here.
//
// cond is the caller's own guard and rides on the same write, so a read
// separate from the increment cannot go stale between them — invite redemption
// conditions on `used_count < max_uses`, and that term is evaluated by the
// engine against the row this write changes.
//
// also carries the companion actions that must land WITH the counter. A
// redemption is not a counter: store/invites.go:265 increments `used_count` and
// writes the redemption, ACL and audit state in the SAME transaction, and a
// helper that committed only the counter would burn an invite with no grant —
// which is the failure store.RestoreInvite exists to compensate for across two
// databases. They ride in this protocol's own batch, so the whole set commits
// or none of it does; the batch bounds apply to them (MaxBatchActions counts
// the counter's write too, and no companion may name the counter's own item).
// A retry re-sends them, which is safe because a refused batch wrote nothing.
func Increment(ctx context.Context, s Store, entity string, key Key, attr string, delta int,
	cond []Cmp, also []Write) (value int, committed bool, err error) {
	// A counter that is declared ordered keeps its declared layout, and only a
	// WHOLE-value declaration is a layout a number renders into: a lead rule says
	// the value starts with an ordered field and carries a tiebreak after it,
	// which a count does not.
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
		// The width the value was WRITTEN at is the width it is compared at, so a
		// counter that is ordered keeps its declared layout and one that is not
		// keeps whatever its writer chose.
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
		// A refused batch is a lost race only if the counter MOVED. Anything
		// else — the caller's own cond, a companion write's condition — refused
		// on a counter this call still holds the value of, and re-reading is how
		// this protocol tells the two apart now that a refusal is one bit.
		//
		// The re-read is outside the refused transaction, so a third caller
		// incrementing in the gap makes a caller-refusal look like a lost race.
		// That costs one more attempt and then answers correctly, because every
		// attempt re-evaluates both the counter and the conditions.
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

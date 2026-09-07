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

// QueryAll walks every page of q and returns the whole selection.
//
// It exists because Query returns a PAGE, and a caller that genuinely wants the
// selection must say so rather than discover the truncation in production. Two
// things make that discovery unlikely without this: a page boundary needs more
// data than any unit fixture holds, and a filtered read can return an EMPTY page
// with more to come, which reads exactly like an empty selection.
//
// It is a protocol and not a driver method for the reason every protocol here
// is: the loop is identical on every engine, so a driver that wrote its own
// could only get it wrong.
func QueryAll(ctx context.Context, s Store, q Query) ([]Item, error) {
	var out []Item
	for {
		page, err := s.Query(ctx, q)
		if err != nil {
			return nil, err
		}
		out = append(out, page.Items...)
		if page.More == "" {
			return out, nil
		}
		q.After = page.More
	}
}

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
func Increment(ctx context.Context, s Store, entity string, key Key, attr string, delta int, cond []Cmp) (int, Outcome, error) {
	width, fixed := orderedWidth(entity, attr)
	for attempt := 0; attempt < maxCASAttempts; attempt++ {
		page, err := s.Query(ctx, Query{
			Entity: entity, Partition: key.Partition,
			Key:     KeyPredicate{Op: ExactKey, Value: key.Sort},
			Project: []string{attr},
		})
		if err != nil {
			return 0, Outcome{}, err
		}
		if len(page.Items) == 0 {
			return 0, Outcome{}, fmt.Errorf("increment %s.%s on %s/%s: no such item",
				entity, attr, key.Partition, key.Sort)
		}
		was, ok := page.Items[0].Attrs[attr]
		if !ok {
			return 0, Outcome{}, fmt.Errorf("increment %s.%s on %s/%s: the item carries no "+
				"such attribute, and a counter with no zero is a modelling gap rather than a "+
				"value to invent", entity, attr, key.Partition, key.Sort)
		}
		n, err := strconv.Atoi(was)
		if err != nil {
			return 0, Outcome{}, fmt.Errorf("increment %s.%s on %s/%s: %q is not a number: %w",
				entity, attr, key.Partition, key.Sort, was, err)
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
		out, err := s.GuardedBatch(ctx, nil, []Write{{
			Op: Update, Entity: entity, Key: key,
			Attrs: map[string]string{attr: text},
			Cond:  Cond{Where: guard},
		}})
		if err != nil {
			return 0, Outcome{}, err
		}
		if out.Committed {
			return next, out, nil
		}
		// A refusal the CALLER asked for is the answer, not a race: only a
		// changed counter is worth reading again.
		if out.Refused != nil && out.Refused.Item != nil && out.Refused.Item.Attrs[attr] == was {
			return n, out, nil
		}
	}
	return 0, Outcome{}, fmt.Errorf("increment %s.%s on %s/%s lost %d races in a row",
		entity, attr, key.Partition, key.Sort, maxCASAttempts)
}

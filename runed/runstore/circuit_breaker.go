package runstore

// circuit_breaker.go holds every read and write of `circuit_breaker`: one item
// per folder whose consecutive agent runs have failed, persisted so the streak
// survives a runed restart.
//
// THE FOLDER IS THE WHOLE COORDINATE — both halves of the key — because there
// is no second one. All three operations are point operations on one folder,
// nothing reads across folders, and no list exists or is wanted. Putting every
// folder's flag into one partition would make each increment contend with every
// other folder's for no read's benefit, which is 6/37 rule 1 read the way emaid
// reads it: partition by what a caller filters on, and when a caller filters on
// nothing but the identifier, the identifier IS the partition.
//
// THE COUNTER IS A COMPARE-AND-SWAP, and that is a bug fix. The statement this
// replaces was an upsert followed by a SEPARATE read, so a reset landing between
// the two answered the caller a count that had already been thrown away — and
// Manager.endRun compares that answer against the threshold with `==`, so the
// trip was missed rather than delayed. objstore.Increment carries the read value
// as the write's own condition and returns the value it committed, so the two
// cannot come apart.
//
// A RESET WRITES A ZERO RATHER THAN DELETING THE ITEM. Increment refuses a
// counter that is not there — "a counter with no zero is a modelling gap rather
// than a value to invent" — and a delete is what would make one vanish under a
// concurrent increment. Writing the zero is what makes the precondition hold
// always, so the increment needs no retry against its own sibling. What it
// costs is one item per folder that has ever failed, in that folder's own
// partition, which is one item per folder and not a collection that grows.
//
// `last_failure` does not survive the move. It was written by the upsert and
// read by nothing — no query, no page, no test — and an attribute with no
// reader is not state. Keeping it would also mean either a second write beside
// the counter, which is the defect above, or a companion action on the item the
// counter already claims, which one transaction may not carry.

import (
	"context"
	"fmt"
	"strconv"

	"github.com/kronael/arizuko/objstore"
)

// attrFailures is the consecutive-failure count.
const attrFailures = "failures"

// breakerKey is the coordinate of one folder's streak.
func breakerKey(folder string) objstore.Key {
	return objstore.Key{Partition: folder, Sort: folder}
}

// FailureStreak answers how many of a folder's agent runs have failed in a row.
//
// A folder with no item has no streak, which is zero — the `sql.ErrNoRows → 0`
// the statement carried, and here it is simply an absent item. A stored value
// that is not a number is NOT zero: that is a corrupt counter and the breaker
// would silently stop tripping, so it is reported.
func (s *Store) FailureStreak(ctx context.Context, folder string) (int, error) {
	it, ok, err := s.point(ctx, CircuitBreaker, breakerKey(folder), attrFailures)
	if err != nil {
		return 0, fmt.Errorf("read %s's failure streak: %w", folder, err)
	}
	if !ok {
		return 0, nil
	}
	n, err := strconv.Atoi(it.Attrs[attrFailures])
	if err != nil {
		return 0, fmt.Errorf("read %s's failure streak: %q is not a number: %w",
			folder, it.Attrs[attrFailures], err)
	}
	return n, nil
}

// RecordFailure counts one failed agent run against a folder and answers the
// streak it just became. The caller compares that against the threshold, so the
// value has to be the one this call committed and not a re-read.
func (s *Store) RecordFailure(ctx context.Context, folder string) (int, error) {
	// The first failure has no counter to increment. ItemAbsent makes the
	// create lose to a concurrent one rather than overwrite it, and the loser
	// falls through to the increment below — where the item now exists.
	committed, err := s.s.GuardedBatch(ctx, nil, []objstore.Write{{
		Op: objstore.Put, Entity: CircuitBreaker, Key: breakerKey(folder),
		Attrs: map[string]string{attrFailures: "1"},
		Cond:  objstore.Cond{Item: objstore.ItemAbsent},
	}})
	if err != nil {
		return 0, fmt.Errorf("count a failure against %s: %w", folder, err)
	}
	if committed {
		return 1, nil
	}
	n, ok, err := objstore.Increment(ctx, s.s, CircuitBreaker, breakerKey(folder),
		attrFailures, 1, nil, nil)
	if err != nil {
		return 0, fmt.Errorf("count a failure against %s: %w", folder, err)
	}
	if !ok {
		// This increment carries no condition of the caller's own, so the only
		// thing that refuses it is a lost race — which Increment retries. A
		// refusal reaching here means the counter is being written faster than
		// the protocol can follow, and answering a stale streak would let the
		// breaker miss its threshold silently.
		return 0, fmt.Errorf("count a failure against %s: the counter refused an "+
			"unconditional increment", folder)
	}
	return n, nil
}

// ClearFailures ends a folder's streak. Any clean exit calls it, and so does a
// new inbound against an open breaker.
//
// It writes a zero rather than removing the item, for the reason the file header
// states: the counter's existence is Increment's precondition, and a delete is
// the one thing that could take it away under a concurrent failure.
func (s *Store) ClearFailures(ctx context.Context, folder string) error {
	_, err := s.s.GuardedBatch(ctx, nil, []objstore.Write{{
		Op: objstore.Put, Entity: CircuitBreaker, Key: breakerKey(folder),
		Attrs: map[string]string{attrFailures: "0"},
	}})
	if err != nil {
		return fmt.Errorf("clear %s's failure streak: %w", folder, err)
	}
	return nil
}

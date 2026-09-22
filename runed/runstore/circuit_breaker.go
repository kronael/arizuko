package runstore

// circuit_breaker.go holds every read and write of `circuit_breaker`: one item
// per folder whose consecutive agent runs have failed, persisted so the streak
// survives a restart.
//
// THE FOLDER IS THE WHOLE COORDINATE: every operation is a point operation on
// one folder and no list exists. THE COUNTER IS A COMPARE-AND-SWAP: an upsert
// followed by a separate read lets a reset land between the two, and
// Manager.endRun compares against the threshold with `==`, so the trip would
// be missed. A RESET WRITES A ZERO RATHER THAN DELETING THE ITEM: Increment
// refuses a counter that is not there, and a delete under a concurrent
// increment is what would make one vanish.

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
// No item is zero. A stored value that is not a number is NOT zero: that is a
// corrupt counter and the breaker would silently stop tripping.
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
	// create lose to a concurrent one, and the loser falls through to the
	// increment below.
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
		// This increment carries no caller condition, so only a lost race
		// refuses it, and Increment retries those. A refusal here means the
		// counter moves faster than the protocol can follow; a stale streak
		// would let the breaker miss its threshold silently.
		return 0, fmt.Errorf("count a failure against %s: the counter refused an "+
			"unconditional increment", folder)
	}
	return n, nil
}

// ClearFailures ends a folder's streak: any clean exit, and a new inbound
// against an open breaker. It writes a zero rather than removing the item
// (file header).
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

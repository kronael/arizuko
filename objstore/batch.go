package objstore

import (
	"errors"
	"fmt"
)

// batch.go holds the write shape: what a caller names when it asks a driver to
// change items together or not at all.

// ItemCond requires an item to exist, or to not exist, before a batch commits.
type ItemCond uint8

const (
	// ItemAny does not look.
	ItemAny ItemCond = iota
	// ItemPresent requires the item to exist.
	ItemPresent
	// ItemAbsent requires the item to be missing. On a Put that is an insert
	// which refuses to overwrite.
	ItemAbsent
)

// Cond is what one item must look like for the batch to commit. The zero value
// asks nothing.
//
// Where's terms are conjuncts, and each one closes a different hole. authd's
// refresh claim needs both of its two: `used_at` Absent picks a single winner
// out of N concurrent redeems, and `revoked_at` Absent refuses a token whose
// family died between the caller's lookup and this write. Dropping the second
// let a logout that committed in that gap go unseen, and a family killed for
// reuse walked away with a live 30-day credential (BUGS F36).
type Cond struct {
	Item  ItemCond
	Where []Cmp
}

// WriteOp is what a write does to its item.
type WriteOp uint8

const (
	// Put stores Attrs as the item's whole attribute set, replacing whatever
	// was there.
	Put WriteOp = iota
	// Update merges Attrs into the item's existing attributes, leaving the
	// rest alone. It writes nothing when the item is missing, which its Cond
	// reports as a refusal.
	Update
	// Delete removes the item. Attrs is ignored.
	Delete
)

// Write is one mutation, with the condition it must satisfy.
type Write struct {
	Op     WriteOp
	Entity string
	Key    Key
	Attrs  map[string]string
	Cond   Cond
}

// Check is a condition on an item the batch DOES NOT modify. It is the shape
// that lets a claim on one item gate a write to another — one pairing token's
// state deciding whether a membership edge is written — without the batch
// touching the item it read.
type Check struct {
	Entity string
	Key    Key
	Cond   Cond
}

// MaxBatchActions is how many checks and writes one batch may carry.
//
// It is a portability bound, not a SQL one. DynamoDB's TransactWriteItems takes
// at most 100 actions, so a SQL driver that accepted a thousand would let a
// caller write a batch only one engine can run — the drift the conformance
// suite exists to catch (spec 6/40). The bound is declared here, with the
// interface, so a driver cannot hold its own opinion of it.
const MaxBatchActions = 100

// ValidateBatch refuses a batch no driver may accept. Every driver calls it
// before it touches an engine, so the two rules below are enforced once rather
// than per driver.
//
// The one-action-per-item rule is DynamoDB's ("Transactions cannot contain more
// than one operation per item") and it is the reason a Check is described as a
// condition on an item the batch may NOT modify. A SQL driver could happily
// check an item and then update it in the same transaction; a caller that did
// so would break on the engine nobody tested.
//
// A batch with no writes is a caller mistake rather than a read: reads go
// through Query, which needs no transaction and takes no locks.
func ValidateBatch(checks []Check, writes []Write) error {
	if len(writes) == 0 {
		return errors.New("a batch with no writes: read through Query, which takes no locks")
	}
	if n := len(checks) + len(writes); n > MaxBatchActions {
		return fmt.Errorf("batch carries %d actions, more than the %d one transaction may hold",
			n, MaxBatchActions)
	}
	seen := make(map[[3]string]bool, len(checks)+len(writes))
	claim := func(entity string, k Key) error {
		id := [3]string{entity, k.Partition, k.Sort}
		if seen[id] {
			return fmt.Errorf("batch acts twice on %s/%s/%s; one transaction holds at most "+
				"one action per item", entity, k.Partition, k.Sort)
		}
		seen[id] = true
		return nil
	}
	for _, c := range checks {
		if err := claim(c.Entity, c.Key); err != nil {
			return err
		}
	}
	for _, w := range writes {
		if err := claim(w.Entity, w.Key); err != nil {
			return err
		}
	}
	return nil
}

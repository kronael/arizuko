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
//
// A Put is the exception and carries at most ItemAbsent. DynamoDB's
// transactional Put does take a full condition expression, and a Put under one
// is a whole-item REPLACE of the item it conditions on — but arizuko writes no
// such replace. Every conditional put in production is create-if-absent
// (authd/authstore signing_keys.go, auth_users.go, refresh_tokens.go), and the
// replace shape existed only in a conformance body (6/40 §"What gets on the
// surface"). validateWrite refuses it rather than leaving a second way to
// express a compare-and-swap that Update already carries.
type Cond struct {
	Item  ItemCond
	Where []Cmp
}

// WriteOp is what a write does to its item.
type WriteOp uint8

const (
	// Put stores Attrs as the item's whole attribute set, replacing whatever
	// was there. With no Cond it is an upsert; with ItemAbsent it is the
	// create-if-absent every engine has, decided by the engine's own uniqueness.
	Put WriteOp = iota
	// Update merges Attrs into the item's existing attributes and takes the
	// attributes named in Remove away, leaving the rest alone. It writes
	// nothing when the item is missing, which its Cond reports as a refusal.
	//
	// Must-exist is the POINT: the refusal is the compare-and-swap every
	// conditional caller here depends on. Create-or-merge is Merge.
	Update
	// Merge is Update without must-exist: it merges Attrs into the item, takes
	// Remove away, writes Init only if it CREATES the item, and never refuses.
	// It is DynamoDB's UpdateItem, whose default is to create the item the
	// expression addresses, with Init as its `if_not_exists(attr, :v)` half.
	//
	// It exists because create-or-merge had no spelling, so five call sites
	// wrote it as two actions on ONE item — which ValidateBatch refuses, so each
	// of them held a transaction to run the pair (spec 6/40 §"Merge, the write
	// op both engines have and the surface had no name for"). What it makes
	// impossible is a half-upserted item, and transitively it is what lets an
	// edge write ride in someone else's batch: a batch may name each item once,
	// so a create-if-absent that REFUSES cannot be a companion write.
	//
	// NOT A Put. A Put is a whole-item replace, so an unconditional one erases
	// every attribute the writer did not name — and a secrets item carries six
	// of which surrogate OAuth owns two, none of them the metadata writer's.
	// Merge is the op that has no such trap.
	Merge
	// Delete removes the item. Attrs, Init and Remove are ignored.
	Delete
)

// Write is one mutation, with the condition it must satisfy.
//
// Attrs and Remove are the two halves of an Update, and DynamoDB writes them as
// one expression: "If you want to modify an attribute's value and completely
// remove another attribute, you could use a SET and a REMOVE action in a single
// statement."
// https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Expressions.UpdateExpressions.html
//
// One statement is the point. store.RepromptOnboarding clears five attributes
// together and its comment says why: after the fold the observer only advances a
// row with `user_sub IS NULL`, so a reset that left one of the five behind would
// re-greet the chat forever and never admit it (BUGS O1). Five writes cannot
// give that; one can.
type Write struct {
	Op     WriteOp
	Entity string
	Key    Key
	// Attrs are the attributes to set. On a Put they are the item's whole
	// attribute set.
	Attrs map[string]string
	// Init are the attributes an item is BORN with: written when this write
	// creates the item, and never written when it merges into one that is
	// already there. Only a Merge has them.
	//
	// It is what separates the two halves of an upsert that today needs two
	// writes: a group's `added_at` is the instant the folder appeared and an
	// edge's `added_by` is who first drew it, so a merge that restated either
	// would overwrite a fact with the instant of the latest write. DynamoDB
	// spells it `if_not_exists(added_at, :t)` in the same update expression.
	//
	// A Merge carrying ONLY Init is create-if-absent that does not refuse —
	// `INSERT ... ON CONFLICT DO NOTHING`, which is what `INSERT OR IGNORE`
	// said before a guarded Put made its row count an answer.
	Init map[string]string
	// Remove names attributes to take away, and an Update or a Merge has them.
	//
	// Taking an attribute AWAY is the only way to make it missing, because
	// absent is the only null: writing "" leaves an attribute that Present
	// still finds and that a tombstone test still reads as set. DynamoDB's
	// REMOVE is the same operation and it is equally forgiving of a name the
	// item does not carry — "if the attributes don't exist, nothing happens".
	Remove []string
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
	seen := make(itemClaims, len(checks)+len(writes))
	for _, c := range checks {
		if err := seen.claim(c.Entity, c.Key); err != nil {
			return err
		}
		if err := validateCond(c.Entity, c.Cond); err != nil {
			return err
		}
	}
	for _, w := range writes {
		if err := seen.claim(w.Entity, w.Key); err != nil {
			return err
		}
		if err := validateWrite(w); err != nil {
			return err
		}
	}
	return nil
}

// itemClaims records the items one transaction has already acted on, so the
// one-action-per-item rule has a single implementation whatever asks it.
type itemClaims map[[3]string]bool

// claim records one action on one item and refuses a second.
func (c itemClaims) claim(entity string, k Key) error {
	id := [3]string{entity, k.Partition, k.Sort}
	if c[id] {
		return fmt.Errorf("batch acts twice on %s/%s/%s; one transaction holds at most "+
			"one action per item", entity, k.Partition, k.Sort)
	}
	c[id] = true
	return nil
}

// oneActionPerItem refuses a write set that names an item twice, ahead of any
// batching. The versioned replace splits its writes across transactions, so
// which of them share one is not the caller's to see — a set that would collide
// in one batch has to be refused whatever the split turns out to be.
func oneActionPerItem(writes []Write) error {
	seen := make(itemClaims, len(writes))
	for _, w := range writes {
		if err := seen.claim(w.Entity, w.Key); err != nil {
			return err
		}
	}
	return nil
}

// validateWrite refuses one mutation no driver may accept, so the rules hold on
// every engine rather than wherever a driver happened to notice.
func validateWrite(w Write) error {
	if err := checkOrderedSort(w.Entity, w.Key.Sort); err != nil {
		return err
	}
	for attr, value := range w.Attrs {
		if err := checkOrderedAttr(w.Entity, attr, value); err != nil {
			return err
		}
	}
	if err := validateCond(w.Entity, w.Cond); err != nil {
		return err
	}
	if w.Op == Put && (len(w.Cond.Where) > 0 || w.Cond.Item == ItemPresent) {
		return fmt.Errorf("put on %s/%s conditions on the item it replaces; a Put carries at "+
			"most ItemAbsent, and a compare-and-swap on an item that must already be there "+
			"is an Update", w.Entity, w.Key.Sort)
	}
	if err := validateMerge(w); err != nil {
		return err
	}
	if len(w.Remove) == 0 {
		return nil
	}
	if w.Op != Update && w.Op != Merge {
		return fmt.Errorf("%s/%s removes attributes on a %s; only an Update or a Merge has a "+
			"REMOVE half, because a Put already replaces the whole document and a Delete takes "+
			"the item", w.Entity, w.Key.Sort, w.Op)
	}
	for _, attr := range w.Remove {
		if attr == "" {
			return fmt.Errorf("update on %s/%s removes an unnamed attribute", w.Entity, w.Key.Sort)
		}
		if _, born := w.Init[attr]; born {
			return fmt.Errorf("merge on %s/%s gives %q to the item it creates and takes it from "+
				"the item it merges into", w.Entity, w.Key.Sort, attr)
		}
		if _, both := w.Attrs[attr]; both {
			// DynamoDB refuses the overlap outright: "Two document paths overlap
			// with each other". Setting and removing one attribute in one write
			// has no answer that is obviously right, so neither engine guesses.
			return fmt.Errorf("update on %s/%s both sets and removes %q", w.Entity, w.Key.Sort, attr)
		}
	}
	return nil
}

// validateMerge holds the rules Init and Merge carry, so a driver meets a write
// it can generate rather than a shape it has to reject halfway.
//
// A Merge NEVER REFUSES, which is what makes it usable as a companion write in
// someone else's batch, so it takes no condition at all: a conditional
// create-or-merge is an Update, whose refusal is its answer.
func validateMerge(w Write) error {
	for attr, value := range w.Init {
		if err := checkOrderedAttr(w.Entity, attr, value); err != nil {
			return err
		}
		if _, both := w.Attrs[attr]; both {
			return fmt.Errorf("merge on %s/%s gives %q one value on create and another on "+
				"merge; an attribute has one writer per write", w.Entity, w.Key.Sort, attr)
		}
	}
	if w.Op != Merge {
		if len(w.Init) > 0 {
			return fmt.Errorf("%s/%s carries create-only attributes on a %s; only a Merge has "+
				"two halves, because a Put creates every time and an Update never does",
				w.Entity, w.Key.Sort, w.Op)
		}
		return nil
	}
	if w.Cond.Item != ItemAny || len(w.Cond.Where) > 0 {
		return fmt.Errorf("merge on %s/%s carries a condition; a Merge never refuses, and a "+
			"create-or-merge that must be able to refuse is an Update", w.Entity, w.Key.Sort)
	}
	// A Merge naming NO attribute is not the mistake the same shape is on an
	// Update, which would write nothing at all: it creates the item and leaves an
	// existing one alone, and the item's EXISTENCE is the whole fact where the
	// entity is a list — store.putMessage opens a chat that way, because `chats`
	// is the partition list the poll loop and the archive walk.
	return nil
}

// validateCond width-checks the values a condition compares against. A read
// that compares the wrong width is BUGS L97 on the guard rather than on the
// cursor, and it fails the same silent way.
func validateCond(entity string, c Cond) error {
	for _, cmp := range c.Where {
		if cmp.Op == Absent || cmp.Op == Present {
			continue
		}
		if err := checkOrderedAttr(entity, cmp.Attr, cmp.Value); err != nil {
			return err
		}
	}
	return nil
}

// String names a write op in an error a human reads.
func (o WriteOp) String() string {
	switch o {
	case Put:
		return "Put"
	case Update:
		return "Update"
	case Merge:
		return "Merge"
	case Delete:
		return "Delete"
	default:
		return fmt.Sprintf("WriteOp(%d)", uint8(o))
	}
}
